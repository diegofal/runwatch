// Package core joins what this machine knows (runner services, their logs)
// with what GitHub knows (runs, jobs, steps) into one Snapshot, and carries
// out the actions: cancel a run, re-run jobs, start or stop a runner
// service. The front ends (tray, dashboard) only draw the Snapshot and call
// these methods.
package core

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"runwatch/internal/domain"
)

// GitHub is the part of the Actions API the core uses.
type GitHub interface {
	Runs(ctx context.Context, repo, status string, limit int) ([]domain.Run, error)
	Jobs(ctx context.Context, repo string, run domain.Run) ([]domain.Job, error)
	JobLog(ctx context.Context, repo string, jobID int64) (string, error)
	// RunnerLabels gives the labels of a repo's runners, by runner name.
	RunnerLabels(ctx context.Context, repo string) (map[string][]string, error)
	CancelRun(ctx context.Context, repo string, runID int64) error
	RerunJob(ctx context.Context, repo string, jobID int64) error
	RerunFailed(ctx context.Context, repo string, runID int64) error
}

// Machine reads the runners installed here. A runner whose files cannot be
// read is still returned, with its Problem set.
type Machine interface {
	Runners(ctx context.Context) ([]domain.Runner, error)
	// Control starts or stops a runner's service.
	Control(ctx context.Context, service string, start bool) error
}

// LogTail follows one running job's output on disk.
type LogTail interface {
	Poll() ([]domain.LogLine, error)
}

// History stores finished jobs.
type History interface {
	Add(jobs []domain.Job) ([]domain.Job, error)
	Jobs() []domain.Job
}

// Notifier shows a desktop notification.
type Notifier interface {
	Notify(a domain.Alert) error
}

// Options are the core's collaborators and settings.
type Options struct {
	GitHub  GitHub
	Machine Machine
	History History
	// NewTail follows the job of plan started at since in a runner folder.
	NewTail  func(dir, plan string, since time.Time) LogTail
	Notifier Notifier // nil = no notifications
	// Usage reads this PC's processors, memory and processes; nil = CPU and
	// memory are not measured.
	Usage func(ctx context.Context) (domain.Reading, error)
	Now   func() time.Time
	Logf  func(format string, args ...any)
	// Managed maps a glob over a runner's folder name to the reason its
	// service is started and stopped elsewhere.
	Managed map[string]string
	// PollActive and PollIdle are how often GitHub is asked while something
	// is queued or running, and while nothing is.
	PollActive, PollIdle time.Duration
}

const (
	recentShown = 300 // finished jobs in a Snapshot
	eventsKept  = 60
	// backfill is how many finished runs per repo are read at start, so
	// History and "usual" have content on the first day.
	backfill = 20
	// localEvery and tailEvery pace the reads of the runners' own files.
	localEvery = 2 * time.Second
	tailEvery  = 300 * time.Millisecond
	// usageEvery paces the reads of the PC's processes. A process that
	// starts and ends between two reads is never seen, and tests start many
	// that live under a second, so this is as short as its cost allows
	// (one read takes a few hundredths of a second of one processor).
	usageEvery = 500 * time.Millisecond
	// labelsEvery is how often a repo's runner labels are read again: they
	// change only when a runner is reconfigured. labelsRetry is the pause
	// after a refused or failed read.
	labelsEvery = time.Hour
	labelsRetry = 10 * time.Minute
)

// Watcher holds the current Snapshot and keeps it fresh.
type Watcher struct {
	o       Options
	started time.Time

	mu       sync.Mutex
	runners  []domain.Runner
	localErr *domain.Problem
	meter    domain.Meter
	usage    *domain.Usage
	// runnerUsage is each measured runner's usage by name; nil while the
	// read fails.
	runnerUsage map[string]domain.RunnerUsage
	usageErr    *domain.Problem
	repos       map[string]*repoState
	events      []domain.Event
	snap        domain.Snapshot
	subs        map[chan struct{}]struct{}
	live        map[string]*liveLog // by runner name
	doneLogs    map[string][]domain.LogLine
	doneKeys    []string
	alerted     map[string]bool
	usual       map[string]int
	usualLen    int
	kick        chan struct{}
}

type repoState struct {
	runs       map[int64]*runState
	problem    *domain.Problem
	backfilled bool
	// labels are the repo's runners' labels by runner name, as last read;
	// nil until a read succeeds. labelsNext is when to read them again.
	labels        map[string][]string
	labelsNext    time.Time
	labelsProblem *domain.Problem
}

type runState struct {
	run  domain.Run
	jobs []domain.Job
}

// New returns a Watcher with an empty Snapshot; Run fills it.
func New(o Options) *Watcher {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.PollActive <= 0 {
		o.PollActive = 10 * time.Second
	}
	if o.PollIdle <= 0 {
		o.PollIdle = 60 * time.Second
	}
	w := &Watcher{
		o: o, started: o.Now(), repos: map[string]*repoState{}, subs: map[chan struct{}]struct{}{},
		live: map[string]*liveLog{}, doneLogs: map[string][]domain.LogLine{}, alerted: map[string]bool{},
		usual: map[string]int{}, kick: make(chan struct{}, 1),
	}
	w.snap = domain.Snapshot{At: w.started, Runners: []domain.Runner{}, Active: []domain.Job{}, Recent: []domain.Job{},
		Problems: []domain.Problem{}, Events: []domain.Event{}, Summary: domain.Summary{Repos: []domain.RepoSummary{}}}
	return w
}

// Snapshot returns the current state.
func (w *Watcher) Snapshot() domain.Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snap
}

// Subscribe returns a channel that gets a value after each change of the
// Snapshot, and the function that ends the subscription.
func (w *Watcher) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.subs[ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		delete(w.subs, ch)
		w.mu.Unlock()
	}
}

// Run keeps the Snapshot fresh until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	w.ScanLocal(ctx)
	w.PollGitHub(ctx)
	local := time.NewTicker(localEvery)
	defer local.Stop()
	tail := time.NewTicker(tailEvery)
	defer tail.Stop()
	// On its own, so that a slow answer from GitHub never leaves a gap in
	// the readings: a gap is processor time nobody can be given.
	var sampler sync.WaitGroup
	defer sampler.Wait()
	sampler.Go(func() {
		usage := time.NewTicker(usageEvery)
		defer usage.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-usage.C:
				w.SampleUsage(ctx)
			}
		}
	})
	nextPoll := w.o.Now().Add(w.pollEvery())
	for {
		select {
		case <-ctx.Done():
			return
		case <-tail.C:
			w.pollTails()
		case <-local.C:
			if w.ScanLocal(ctx) {
				// A runner picked up or finished a job: GitHub has news.
				nextPoll = w.o.Now()
			}
			if !w.o.Now().Before(nextPoll) {
				w.PollGitHub(ctx)
				nextPoll = w.o.Now().Add(w.pollEvery())
			}
		case <-w.kick:
			w.PollGitHub(ctx)
			nextPoll = w.o.Now().Add(w.pollEvery())
		}
	}
}

func (w *Watcher) pollEvery() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.snap.Active) > 0 || w.snap.Summary.Busy > 0 {
		return w.o.PollActive
	}
	return w.o.PollIdle
}

// ScanLocal re-reads the runners' own files and rebuilds the Snapshot. It
// reports whether any runner started or finished a job since the last scan.
func (w *Watcher) ScanLocal(ctx context.Context) bool {
	runners, err := w.o.Machine.Runners(ctx)
	now := w.o.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := len(runners) != len(w.runners)
	prev := map[string]domain.Runner{}
	for _, r := range w.runners {
		prev[r.Name] = r
	}
	for i := range runners {
		r := &runners[i]
		for glob, why := range w.o.Managed {
			if ok, _ := path.Match(glob, filepath.Base(r.Dir)); ok { // a bad glob matches nothing
				r.Managed = why
			}
		}
		old, ok := prev[r.Name]
		if !ok || old.Busy != r.Busy || !old.JobStarted.Equal(r.JobStarted) {
			changed = true
		}
		if ok && old.State != r.State && old.State != domain.ServiceUnknown && r.State != domain.ServiceUnknown {
			w.event(domain.Event{At: now, Kind: "service", Repo: r.Repo, Text: fmt.Sprintf("%s: service %s", r.Name, r.State)})
		}
		// Sizes are measured rarely, by SetSizes; keep the last ones.
		r.DiagBytes, r.WorkBytes = old.DiagBytes, old.WorkBytes
		if rs := w.repos[r.Repo]; rs != nil {
			r.Labels = rs.labels[r.Name] // nil when unread: Ours then falls back
		}
		w.followLocked(r)
	}
	w.runners = runners
	w.localErr = nil
	if err != nil {
		w.localErr = &domain.Problem{Source: "this PC", Text: err.Error(), Since: now}
	}
	w.rebuildLocked()
	return changed
}

// SampleUsage reads this PC's processors, memory and processes and works
// out the PC's and each runner's CPU and memory. The next rebuild of the
// Snapshot shows them. A failed read keeps the PC's last numbers, with
// their time, beside the problem; the runners' are dropped, so none is
// shown as current.
func (w *Watcher) SampleUsage(ctx context.Context) {
	if w.o.Usage == nil {
		return
	}
	reading, err := w.o.Usage(ctx)
	now := w.o.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if w.usageErr == nil {
			w.usageErr = &domain.Problem{Source: "this PC's CPU", Since: now}
		}
		w.usageErr.Text = err.Error()
		w.runnerUsage = nil
		return
	}
	w.usageErr = nil
	roots := map[int]string{}
	for _, r := range w.runners {
		if r.PID > 0 {
			roots[r.PID] = r.Name
		}
	}
	if usage, per, ok := w.meter.Measure(reading, roots); ok {
		w.usage, w.runnerUsage = &usage, per
	}
}

// SetSizes records a runner's measured folder sizes.
func (w *Watcher) SetSizes(name string, diag, work int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.runners {
		if w.runners[i].Name == name {
			w.runners[i].DiagBytes, w.runners[i].WorkBytes = diag, work
		}
	}
	w.rebuildLocked()
}

func (w *Watcher) event(e domain.Event) {
	w.events = append([]domain.Event{e}, w.events...)
	if len(w.events) > eventsKept {
		w.events = w.events[:eventsKept]
	}
}

// rebuildLocked recomputes the Snapshot from the parts and tells the
// subscribers. Call with w.mu held.
func (w *Watcher) rebuildLocked() {
	now := w.o.Now()
	history := w.o.History.Jobs()
	if len(history) != w.usualLen {
		w.usual, w.usualLen = map[string]int{}, len(history)
	}
	usual := func(j *domain.Job) {
		k := j.Repo + "\x00" + j.Name
		if _, ok := w.usual[k]; !ok {
			w.usual[k] = domain.Usual(history, j.Repo, j.Name)
		}
		j.UsualSec = w.usual[k]
	}

	active, problems := w.activeLocked(usual)
	runners := w.shownRunnersLocked(active)
	for _, r := range runners {
		if r.Problem != "" {
			problems = append(problems, domain.Problem{Source: "runner " + r.Name, Text: r.Problem, Since: now})
		}
	}

	recent := history
	if len(recent) > recentShown {
		recent = recent[:recentShown]
	}
	recent = append([]domain.Job{}, recent...)
	for i := range recent {
		usual(&recent[i])
	}

	// A problem keeps the time it first appeared.
	since := map[string]time.Time{}
	for _, p := range w.snap.Problems {
		since[p.Source+"\x00"+p.Text] = p.Since
	}
	for i := range problems {
		if t, ok := since[problems[i].Source+"\x00"+problems[i].Text]; ok {
			problems[i].Since = t
		}
	}

	w.snap = domain.Snapshot{
		At: now, Runners: runners, Active: active, Recent: recent, Problems: problems,
		Events:  append([]domain.Event{}, w.events...),
		Summary: domain.Summarize(runners, active, recent, now),
		Usage:   w.usage,
	}
	w.notifyLocked()
	for ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// shownRunnersLocked is the runners as the Snapshot shows them: each with
// what its processes use and the GitHub job it is running.
func (w *Watcher) shownRunnersLocked(active []domain.Job) []domain.Runner {
	runners := append([]domain.Runner(nil), w.runners...)
	for i := range runners {
		r := &runners[i]
		if u, ok := w.runnerUsage[r.Name]; ok {
			r.Usage = &u
		}
		for _, j := range active {
			if j.Status == domain.StatusInProgress && j.Runner == r.Name {
				r.JobID = j.ID
			}
		}
	}
	return runners
}

// activeLocked gathers this machine's unfinished jobs from every repo,
// oldest queued first, and the problems of the reads behind them.
func (w *Watcher) activeLocked(usual func(*domain.Job)) ([]domain.Job, []domain.Problem) {
	active := []domain.Job{}
	problems := []domain.Problem{}
	if w.localErr != nil {
		problems = append(problems, *w.localErr)
	}
	if w.usageErr != nil {
		problems = append(problems, *w.usageErr)
	}
	repoNames := make([]string, 0, len(w.repos))
	for name := range w.repos {
		repoNames = append(repoNames, name)
	}
	sort.Strings(repoNames)
	for _, name := range repoNames {
		rs := w.repos[name]
		if rs.problem != nil {
			problems = append(problems, *rs.problem)
		}
		if rs.labelsProblem != nil {
			problems = append(problems, *rs.labelsProblem)
		}
		for _, run := range rs.runs {
			for _, j := range run.jobs {
				if j.Status != domain.StatusCompleted && domain.Ours(j, w.runners) {
					usual(&j)
					active = append(active, j)
				}
			}
		}
	}
	sort.Slice(active, func(a, b int) bool {
		if !active[a].QueuedAt.Equal(active[b].QueuedAt) {
			return active[a].QueuedAt.Before(active[b].QueuedAt)
		}
		return active[a].ID < active[b].ID
	})
	return active, problems
}

// notifyLocked sends each new alert once.
func (w *Watcher) notifyLocked() {
	if w.o.Notifier == nil {
		return
	}
	for _, a := range domain.Alerts(w.snap, w.started) {
		if w.alerted[a.Key] {
			continue
		}
		w.alerted[a.Key] = true
		go func() {
			if err := w.o.Notifier.Notify(a); err != nil {
				w.o.Logf("notify %s: %v", a.Key, err)
			}
		}()
	}
}
