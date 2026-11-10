package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"runwatch/internal/adapters/github"
	"runwatch/internal/adapters/history"
	"runwatch/internal/adapters/machine"
	"runwatch/internal/adapters/runnerfs"
	"runwatch/internal/adapters/winsvc"
	"runwatch/internal/domain"
	"runwatch/internal/fakegh"
)

// rig is the real core on the fake GitHub and fake runner folders, with a
// clock the test moves.
type rig struct {
	t       *testing.T
	world   *fakegh.World
	w       *Watcher
	mu      sync.Mutex
	now     time.Time
	alerts  []domain.Alert
	control []string
	root    string
}

type token string

func (t token) Token(context.Context, string) (string, error) { return string(t), nil }

func (r *rig) Notify(a domain.Alert) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, a)
	return nil
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func (r *rig) sentAlerts() []domain.Alert {
	// Notifications are sent off the core's lock; give them a moment.
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		got := append([]domain.Alert(nil), r.alerts...)
		r.mu.Unlock()
		if len(got) > 0 || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newRig builds a world with runners app-1, app-2 (example/app) and
// billing-1 (acme/billing), each installed as a service.
func newRig(t *testing.T, managed map[string]string) *rig {
	t.Helper()
	r := &rig{t: t, now: time.Now().Truncate(time.Second), root: t.TempDir()}
	r.world = fakegh.New(r.root)
	r.world.Now = r.clock
	var services []winsvc.Service
	for _, x := range [][2]string{{"example/app", "app-1"}, {"example/app", "app-2"}, {"acme/billing", "billing-1"}} {
		rn, err := r.world.AddRunner(x[0], x[1])
		if err != nil {
			t.Fatal(err)
		}
		services = append(services, winsvc.Service{Name: "actions.runner." + x[1], Dir: rn.Dir, State: domain.ServiceRunning})
	}
	api := httptest.NewServer(r.world.Handler())
	t.Cleanup(api.Close)
	store, err := history.Open(filepath.Join(t.TempDir(), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	mach := &machine.Machine{
		Roots:    []string{r.root},
		Services: func() ([]winsvc.Service, error) { return services, nil },
		ControlFn: func(_ context.Context, service string, start bool) error {
			r.control = append(r.control, service+map[bool]string{true: " start", false: " stop"}[start])
			return nil
		},
	}
	r.w = New(Options{
		GitHub: github.New(api.URL, token("t"), nil), Machine: mach, History: store, Notifier: r, Now: r.clock, Managed: managed,
		NewTail: func(dir, plan string, since time.Time) LogTail { return runnerfs.NewTail(dir, plan, since) },
	})
	return r
}

// refresh does what the watcher's loop does on its timers.
func (r *rig) refresh() domain.Snapshot {
	ctx := context.Background()
	r.w.ScanLocal(ctx)
	r.w.pollTails()
	r.w.PollGitHub(ctx)
	return r.w.Snapshot()
}

func (r *rig) must(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatal(err)
	}
}

func find(jobs []domain.Job, id int64) *domain.Job {
	for i := range jobs {
		if jobs[i].ID == id {
			return &jobs[i]
		}
	}
	return nil
}

func TestSnapshotJoinsThisPCWithGitHub(t *testing.T) {
	r := newRig(t, nil)
	if s := r.w.Snapshot(); s.Runners == nil || s.Active == nil || s.Recent == nil || s.Problems == nil || s.Events == nil {
		t.Fatalf("an empty snapshot must have empty lists, not null: %+v", s)
	}
	run := r.world.Push("example/app", "feature/x", "unit", "lint", "e2e")
	hosted := r.world.Push("example/app", "feature/x", "docs")
	r.world.SetLabels(hosted.Jobs[0].ID, "ubuntu-latest") // GitHub's own machines run this one
	r.advance(20 * time.Minute)
	r.must(r.world.Start(run.Jobs[0].ID, "app-1"))

	// This PC knows a runner is busy before GitHub is asked anything.
	r.w.ScanLocal(context.Background())
	s := r.w.Snapshot()
	if s.Summary.Total != 3 || s.Summary.Busy != 1 || s.Summary.Online != 3 || len(s.Active) != 0 {
		t.Fatalf("after the local scan: %+v", s.Summary)
	}
	if app1 := s.Runners[1]; app1.Name != "app-1" || !app1.Busy || app1.JobName != "unit" || app1.JobID != 0 || app1.Service != "actions.runner.app-1" || app1.State != domain.ServiceRunning {
		t.Fatalf("app-1 = %+v (runners sort by repo, then name: %v)", app1, s.Runners[0].Name)
	}

	r.advance(5 * time.Minute)
	s = r.refresh()
	if len(s.Problems) != 0 {
		t.Fatalf("problems: %+v", s.Problems)
	}
	if s.Summary.Waiting != 2 || s.Summary.OldestWait != 25*60 || s.Summary.Busy != 1 {
		t.Fatalf("summary = %+v; want lint and e2e waiting 25 min, not the GitHub-hosted docs job", s.Summary)
	}
	if len(s.Active) != 3 || s.Active[0].Name != "unit" {
		t.Fatalf("active = %d jobs, first %q; want oldest queued first, ties by id", len(s.Active), s.Active[0].Name)
	}
	unit := find(s.Active, run.Jobs[0].ID)
	if unit.Status != domain.StatusInProgress || unit.Runner != "app-1" || unit.Waited(s.At) != 20*time.Minute || unit.Ran(s.At) != 5*time.Minute {
		t.Errorf("unit = %+v", unit)
	}
	if s.Runners[1].JobID != unit.ID {
		t.Errorf("app-1 not matched to its GitHub job: %+v", s.Runners[1])
	}
	app := s.Summary.Repos[1]
	if app.Repo != "example/app" || app.Runners != 2 || app.Busy != 1 || app.Waiting != 2 {
		t.Errorf("repo line = %+v", app)
	}
}

func TestJobsForOtherMachinesAreNotOurs(t *testing.T) {
	r := newRig(t, nil)
	// Another PC's runner serves the same repo: GitHub knows it, this PC
	// has no such runner (its folder here is not a runner folder).
	run := r.world.Push("example/app", "feature/x", "unit")
	if _, err := r.world.AddRunner("example/app", "other-pc-1"); err != nil {
		t.Fatal(err)
	}
	r.must(os.Remove(filepath.Join(r.root, "other-pc-1", ".runner")))
	r.must(r.world.Start(run.Jobs[0].ID, "other-pc-1"))
	s := r.refresh()
	if len(s.Active) != 0 || s.Summary.Busy != 0 {
		t.Fatalf("a job running on another machine's runner is shown as ours: %+v", s.Active)
	}
	r.must(r.world.Finish(run.Jobs[0].ID, "failure"))
	if s = r.refresh(); len(s.Recent) != 0 || s.Summary.FailedHour != 0 || len(r.sentAlerts()) != 0 {
		t.Fatalf("another machine's failure is in our history: %+v", s.Recent)
	}
}

func TestFinishedJobsBecomeHistoryEventsAndUsual(t *testing.T) {
	r := newRig(t, nil)
	r.refresh()
	for i, secs := range []int{100, 300, 200} {
		run := r.world.Push("example/app", "feature/x", "unit")
		r.must(r.world.Start(run.Jobs[0].ID, "app-1"))
		r.refresh()
		r.advance(time.Duration(secs) * time.Second)
		r.must(r.world.Finish(run.Jobs[0].ID, "success"))
		s := r.refresh()
		if len(s.Recent) != i+1 || s.Recent[0].ID != run.Jobs[0].ID || len(s.Active) != 0 || s.Summary.Busy != 0 {
			t.Fatalf("round %d: recent = %d, active = %d, busy = %d", i, len(s.Recent), len(s.Active), s.Summary.Busy)
		}
	}
	run := r.world.Push("example/app", "feature/y", "unit")
	s := r.refresh()
	if got := find(s.Active, run.Jobs[0].ID).UsualSec; got != 200 {
		t.Errorf("usual = %d, want the median 200", got)
	}
	if s.Recent[0].UsualSec != 200 {
		t.Errorf("finished jobs carry usual too: %+v", s.Recent[0])
	}
	var kinds []string
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, " "); got != "queued finished queued finished queued finished queued" {
		t.Errorf("events (newest first) = %q", got)
	}
	if e := s.Events[1]; !strings.Contains(e.Text, "app · unit passed on app-1 after 3m 20s") || e.JobID == 0 {
		t.Errorf("event = %+v", e)
	}
}

func TestHistoryFromBeforeTheStartIsBackfilledQuietly(t *testing.T) {
	r := newRig(t, nil)
	old := r.world.Push("example/app", "main", "unit")
	r.must(r.world.Start(old.Jobs[0].ID, "app-1"))
	r.advance(2 * time.Minute)
	r.must(r.world.Finish(old.Jobs[0].ID, "failure"))
	r.advance(10 * time.Minute)

	// The app starts now: a fresh watcher on the same world.
	fresh := newRigOn(t, r)
	s := fresh.refresh()
	if len(s.Recent) != 1 || s.Recent[0].Conclusion != domain.ConclusionFailure || s.Summary.FailedHour != 1 {
		t.Fatalf("backfill: recent = %+v, summary = %+v", s.Recent, s.Summary)
	}
	if len(s.Events) != 0 || len(fresh.sentAlerts()) != 0 {
		t.Errorf("what finished before the app started is not news: events %+v, alerts %+v", s.Events, fresh.alerts)
	}
}

// newRigOn is a second watcher (a restart) over r's world, with its own history.
func newRigOn(t *testing.T, r *rig) *rig {
	t.Helper()
	n := &rig{t: t, world: r.world, now: r.clock(), root: r.root}
	r.world.Now = n.clock
	api := httptest.NewServer(r.world.Handler())
	t.Cleanup(api.Close)
	store, err := history.Open(filepath.Join(t.TempDir(), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	n.w = New(Options{
		GitHub: github.New(api.URL, token("t"), nil), Machine: &machine.Machine{Roots: []string{r.root}}, History: store, Notifier: n, Now: n.clock,
		NewTail: func(dir, plan string, since time.Time) LogTail { return runnerfs.NewTail(dir, plan, since) },
	})
	return n
}

func TestAFailedReadIsShownAndOldNumbersKept(t *testing.T) {
	r := newRig(t, nil)
	run := r.world.Push("example/app", "feature/x", "unit", "lint")
	r.refresh()

	r.world.FailNext("/repos/example/app/actions/runs", http.StatusBadGateway)
	r.advance(time.Minute)
	s := r.refresh()
	if len(s.Problems) != 1 || s.Problems[0].Source != "github example/app" || !strings.Contains(s.Problems[0].Text, "502") {
		t.Fatalf("problems = %+v", s.Problems)
	}
	if s.Summary.Waiting != 2 {
		t.Errorf("the last known queue must stay on screen beside the problem: %+v", s.Summary)
	}
	if domain.TrayLevel(s) != domain.LevelProblem {
		t.Error("the tray must say the numbers are not current")
	}
	since := s.Problems[0].Since
	r.advance(time.Minute)
	if s = r.refresh(); !s.Problems[0].Since.Equal(since) {
		t.Errorf("a problem keeps the time it started: %v then %v", since, s.Problems[0].Since)
	}

	// Only the jobs of one run fail: the run list is fresh, that run's jobs are the old ones.
	r.world.Heal()
	r.world.FailNext("/jobs", http.StatusInternalServerError)
	r.must(r.world.Start(run.Jobs[0].ID, "app-1"))
	s = r.refresh()
	if len(s.Problems) != 1 || !strings.Contains(s.Problems[0].Text, "list jobs of run") || len(s.Active) != 2 {
		t.Fatalf("jobs outage: problems = %+v, active = %d", s.Problems, len(s.Active))
	}
	r.world.Heal()
	if s = r.refresh(); len(s.Problems) != 0 || find(s.Active, run.Jobs[0].ID).Status != domain.StatusInProgress {
		t.Fatalf("after the outage: problems = %+v", s.Problems)
	}

	// A runner whose own files cannot be read.
	r.must(os.WriteFile(filepath.Join(r.root, "app-2", ".runner"), []byte("{broken"), 0o644))
	s = r.refresh()
	if len(s.Problems) != 1 || s.Problems[0].Source != "runner app-2" || !strings.Contains(s.Problems[0].Text, "parse .runner") {
		t.Fatalf("runner problem = %+v", s.Problems)
	}
	if s.Summary.Total != 3 {
		t.Errorf("an unreadable runner is still listed: %+v", s.Summary)
	}
}

func TestLiveLogThenTheWholeLogFromGitHub(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	run := r.world.Push("example/app", "feature/x", "unit", "lint")
	unit, lint := run.Jobs[0].ID, run.Jobs[1].ID
	r.refresh()

	if _, err := r.w.JobLog(ctx, "example/app", 424242, 0); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("unknown job: err = %v", err)
	}
	c, err := r.w.JobLog(ctx, "example/app", unit, 0)
	if err != nil || c.Source != "none" || c.Done || !strings.Contains(c.Note, "Waiting for a runner") {
		t.Fatalf("queued job: %+v, %v", c, err)
	}

	r.must(r.world.Start(unit, "app-1"))
	r.refresh()
	if c, _ = r.w.JobLog(ctx, "example/app", unit, 0); c.Source != "live" || len(c.Lines) != 0 || !strings.Contains(c.Note, "No output yet") {
		t.Fatalf("running, nothing printed: %+v; an empty terminal must say why", c)
	}
	r.must(r.world.Output(unit, "##[group]Run tests", "  ok one"))
	r.refresh()
	c, err = r.w.JobLog(ctx, "example/app", unit, 0)
	if err != nil || c.Source != "live" || c.Done || len(c.Lines) != 2 || c.Lines[1].Text != "  ok one" || c.Next != 2 {
		t.Fatalf("live: %+v, %v", c, err)
	}
	r.must(r.world.Output(unit, "  ok two"))
	r.w.pollTails()
	c, _ = r.w.JobLog(ctx, "example/app", unit, c.Next)
	if len(c.Lines) != 1 || c.Lines[0].Text != "  ok two" || c.Next != 3 {
		t.Fatalf("live, from 2: %+v", c)
	}
	if c, _ = r.w.JobLog(ctx, "example/app", unit, 99); len(c.Lines) != 0 || c.Next != 3 {
		t.Errorf("asking past the end: %+v", c)
	}

	r.must(r.world.Finish(unit, "failure"))
	r.refresh()
	c, err = r.w.JobLog(ctx, "example/app", unit, 0)
	if err != nil || c.Source != "github" || !c.Done || len(c.Lines) != 3 || c.Lines[0].Text != "##[group]Run tests" || c.Lines[0].At.IsZero() {
		t.Fatalf("finished: %+v, %v", c, err)
	}
	if c, _ = r.w.JobLog(ctx, "example/app", unit, 2); len(c.Lines) != 1 {
		t.Errorf("finished, from 2: %+v", c)
	}

	// A job cancelled in the queue has no output; that is said, not an error.
	_, err = r.w.CancelRun(ctx, "example/app", run.ID)
	r.must(err)
	r.refresh()
	c, err = r.w.JobLog(ctx, "example/app", lint, 0)
	if err != nil || c.Source != "none" || !c.Done || !strings.Contains(c.Note, "never started") {
		t.Fatalf("never-started job: %+v, %v", c, err)
	}

	// The next job on the same runner starts a fresh buffer.
	next := r.world.Push("example/app", "feature/z", "unit")
	r.advance(5 * time.Second)
	r.must(r.world.Start(next.Jobs[0].ID, "app-1"))
	r.must(r.world.Output(next.Jobs[0].ID, "fresh"))
	r.refresh()
	if c, _ = r.w.JobLog(ctx, "example/app", next.Jobs[0].ID, 0); len(c.Lines) != 1 || c.Lines[0].Text != "fresh" {
		t.Fatalf("second job on the runner: %+v", c)
	}
}

func TestLiveLogSaysWhenEarlierOutputIsGone(t *testing.T) {
	r := newRig(t, nil)
	run := r.world.Push("example/app", "feature/x", "unit")
	r.must(r.world.Start(run.Jobs[0].ID, "app-1"))
	r.advance(10 * time.Minute)
	late := newRigOn(t, r) // Runwatch starts ten minutes into the job
	r.must(r.world.Output(run.Jobs[0].ID, "still running"))
	late.refresh()
	c, err := late.w.JobLog(context.Background(), "example/app", run.Jobs[0].ID, 0)
	if err != nil || len(c.Lines) != 1 || !strings.Contains(c.Note, "started before Runwatch did") {
		t.Fatalf("chunk = %+v, %v", c, err)
	}
}

func TestLiveLogDropsTheOldestLinesButKeepsIndexes(t *testing.T) {
	l := &liveLog{}
	w := &Watcher{live: map[string]*liveLog{"app-1": l}}
	for i := range liveCap {
		l.lines = append(l.lines, domain.LogLine{Text: string(rune('a' + i%26))})
	}
	l.lines = l.lines[10:] // as pollTails does when over the cap
	l.dropped = 10
	c := w.liveChunk(domain.Job{Runner: "app-1"}, liveCap-2)
	if len(c.Lines) != 2 || c.Next != liveCap {
		t.Fatalf("chunk = %d lines, next %d", len(c.Lines), c.Next)
	}
	if c = w.liveChunk(domain.Job{Runner: "app-1"}, 3); len(c.Lines) != liveCap-10 {
		t.Errorf("asking for dropped lines gives what is left: %d", len(c.Lines))
	}
	if c = w.liveChunk(domain.Job{Runner: "nope"}, 0); c.Note == "" || c.Lines == nil {
		t.Errorf("a job on a runner that is not here: %+v", c)
	}
}

func TestStopAndRerun(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	run := r.world.Push("example/app", "feature/x", "unit", "lint")
	unit := run.Jobs[0].ID
	r.must(r.world.Start(unit, "app-1"))
	r.refresh()

	if _, err := r.w.CancelRun(ctx, "example/app", 999); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a run Runwatch does not watch: err = %v", err)
	}
	if _, err := r.w.CancelRun(ctx, "acme/billing", run.ID); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("the right run in the wrong repo: err = %v", err)
	}
	if err := r.w.RerunJob(ctx, "example/app", unit); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("re-running a job that is still running: err = %v", err)
	}
	if err := r.w.RerunFailed(ctx, "example/app", run.ID); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("re-run failed on a run with nothing finished: err = %v", err)
	}
	if got := r.world.Posts(); len(got) != 0 {
		t.Fatalf("a refused action must not reach GitHub: %v", got)
	}

	n, err := r.w.CancelRun(ctx, "example/app", run.ID)
	if err != nil || n != 2 {
		t.Fatalf("cancel: %d, %v", n, err)
	}
	if e := r.w.Snapshot().Events[0]; e.Kind != "cancelled" || !strings.Contains(e.Text, "you stopped run") || !strings.Contains(e.Text, "(2 jobs)") {
		t.Errorf("event = %+v", e)
	}
	s := r.refresh()
	if len(s.Active) != 0 || len(s.Recent) != 2 || s.Summary.Busy != 0 {
		t.Fatalf("after the stop: active %d, recent %d, busy %d", len(s.Active), len(s.Recent), s.Summary.Busy)
	}

	r.must(r.w.RerunJob(ctx, "example/app", unit))
	s = r.refresh()
	if len(s.Active) != 1 || s.Active[0].Name != "unit" || s.Active[0].Attempt != 2 || s.Active[0].ID == unit {
		t.Fatalf("after the re-run: %+v", s.Active)
	}
	if len(s.Recent) != 2 {
		t.Errorf("the first attempt stays in history: %d", len(s.Recent))
	}
	// GitHub's own refusal (the run is live again) comes through with its words.
	err = r.w.RerunFailed(ctx, "example/app", run.ID)
	var se *github.StatusError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "already running") {
		t.Errorf("re-run failed on a live run: err = %v", err)
	}
	r.must(r.world.Start(s.Active[0].ID, "app-2"))
	r.must(r.world.Finish(s.Active[0].ID, "success"))
	r.refresh()
	r.must(r.w.RerunFailed(ctx, "example/app", run.ID))
	if s = r.refresh(); len(s.Active) != 1 || s.Active[0].Name != "lint" {
		t.Fatalf("re-run failed queues only what did not pass: %+v", s.Active)
	}
}

func TestServiceControl(t *testing.T) {
	r := newRig(t, map[string]string{"billing-*": "Use the billing repo's script."})
	ctx := context.Background()
	r.refresh()
	if err := r.w.ControlService(ctx, "nope", true); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("unknown runner: %v", err)
	}
	err := r.w.ControlService(ctx, "billing-1", false)
	if !errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "Use the billing repo's script.") {
		t.Errorf("a runner managed elsewhere: %v", err)
	}
	if s := r.w.Snapshot(); s.Runners[0].Managed == "" || s.Runners[1].Managed != "" {
		t.Errorf("managed flags: %+v", s.Runners)
	}
	r.must(r.w.ControlService(ctx, "app-1", false))
	r.must(r.w.ControlService(ctx, "app-1", true))
	if got := strings.Join(r.control, ", "); got != "actions.runner.app-1 stop, actions.runner.app-1 start" {
		t.Errorf("service calls = %q", got)
	}
	if len(r.control) != 2 {
		t.Errorf("refused calls reached the service manager: %v", r.control)
	}

	plain := newRigOn(t, r) // runners found by folder only: no service to control
	plain.refresh()
	if err := plain.w.ControlService(ctx, "app-1", true); !errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "not installed as a service") {
		t.Errorf("no service: %v", err)
	}
}

func TestAlertsAreSentOnce(t *testing.T) {
	r := newRig(t, nil)
	run := r.world.Push("example/app", "feature/x", "unit", "lint")
	r.must(r.world.Start(run.Jobs[0].ID, "app-1"))
	r.refresh()
	r.advance(4 * time.Minute)
	r.must(r.world.Finish(run.Jobs[0].ID, "failure"))
	r.refresh()
	r.refresh()
	got := r.sentAlerts()
	if len(got) != 1 || got[0].Title != "Job failed: unit" {
		t.Fatalf("alerts = %+v, want the failure once", got)
	}
	r.advance(46 * time.Minute)
	r.refresh()
	r.refresh()
	time.Sleep(50 * time.Millisecond)
	if got = r.sentAlerts(); len(got) != 2 || got[1].Title != "Waiting more than 45 min" {
		t.Fatalf("alerts = %+v, want the long wait once more", got)
	}
}

func TestRunKeepsTheSnapshotFreshAndTellsSubscribers(t *testing.T) {
	r := newRig(t, nil)
	r.world.Now, r.w.o.Now = time.Now, time.Now // Run paces itself on real time
	r.w.o.PollActive, r.w.o.PollIdle = 50*time.Millisecond, 50*time.Millisecond
	changed, stop := r.w.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.w.Run(ctx); close(done) }()

	run := r.world.Push("example/app", "feature/x", "unit")
	waitFor := func(what string, ok func(domain.Snapshot) bool) {
		t.Helper()
		deadline := time.After(15 * time.Second)
		for !ok(r.w.Snapshot()) {
			select {
			case <-changed:
			case <-deadline:
				t.Fatalf("timed out waiting for %s; snapshot %+v", what, r.w.Snapshot().Summary)
			}
		}
	}
	waitFor("the queued job", func(s domain.Snapshot) bool { return s.Summary.Waiting == 1 })
	r.must(r.world.Start(run.Jobs[0].ID, "app-1"))
	r.must(r.world.Output(run.Jobs[0].ID, "hello"))
	waitFor("the running job with its GitHub id", func(s domain.Snapshot) bool {
		return s.Summary.Busy == 1 && len(s.Active) == 1 && s.Active[0].Runner == "app-1"
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := r.w.JobLog(ctx, "example/app", run.Jobs[0].ID, 0)
		if err == nil && len(c.Lines) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the live log never arrived: %+v, %v", c, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// An action asks for a poll at once instead of waiting for the timer.
	if _, err := r.w.CancelRun(ctx, "example/app", run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor("the stop to show", func(s domain.Snapshot) bool { return len(s.Active) == 0 && len(s.Recent) == 1 })

	stop()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when its context ended")
	}
	r.w.SetSizes("app-1", 1234, 5678)
	if s := r.w.Snapshot(); s.Runners[1].DiagBytes != 1234 || s.Runners[1].WorkBytes != 5678 {
		t.Errorf("sizes = %+v", s.Runners[1])
	}
	r.w.ScanLocal(context.Background())
	if s := r.w.Snapshot(); s.Runners[1].DiagBytes != 1234 {
		t.Error("a scan must keep the measured sizes")
	}
}
