package domain

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return t0.Add(-d) }

func queued(repo string, id, run int64, waited time.Duration) Job {
	return Job{ID: id, RunID: run, Repo: repo, Name: "unit", Status: StatusQueued, QueuedAt: ago(waited), Labels: []string{"self-hosted"}, Branch: "feature/x"}
}

func done(repo, name, conclusion string, id int64, ran, endedAgo time.Duration) Job {
	return Job{ID: id, Repo: repo, Name: name, Status: StatusCompleted, Conclusion: conclusion, Runner: "app-1", Branch: "feature/x",
		QueuedAt: ago(endedAgo + ran + time.Minute), StartedAt: ago(endedAgo + ran), FinishedAt: ago(endedAgo)}
}

func TestJobWaitedAndRan(t *testing.T) {
	cases := []struct {
		name         string
		job          Job
		waited, ran  time.Duration
		wantsStarted bool
	}{
		{"queued waits until now", queued("example/app", 1, 1, 10*time.Minute), 10 * time.Minute, 0, false},
		{"running: waited until pickup, ran until now", Job{Status: StatusInProgress, Runner: "app-1", QueuedAt: ago(9 * time.Minute), StartedAt: ago(4 * time.Minute)}, 5 * time.Minute, 4 * time.Minute, true},
		{"finished", done("example/app", "unit", ConclusionSuccess, 2, 3*time.Minute, 20*time.Minute), time.Minute, 3 * time.Minute, true},
		// GitHub gives a job cancelled in the queue a started_at but no runner.
		{"cancelled while waiting never ran", Job{Status: StatusCompleted, Conclusion: ConclusionCancelled, QueuedAt: ago(30 * time.Minute), StartedAt: ago(30 * time.Minute), FinishedAt: ago(10 * time.Minute)}, 20 * time.Minute, 0, false},
		{"clock skew never goes negative", Job{Status: StatusQueued, QueuedAt: t0.Add(time.Minute)}, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.job.Waited(t0); got != c.waited {
				t.Errorf("Waited = %s, want %s", got, c.waited)
			}
			if got := c.job.Ran(t0); got != c.ran {
				t.Errorf("Ran = %s, want %s", got, c.ran)
			}
			if got := c.job.Started(); got != c.wantsStarted {
				t.Errorf("Started = %v, want %v", got, c.wantsStarted)
			}
		})
	}
}

func TestOurs(t *testing.T) {
	mine := []Runner{
		{Name: "app-1", Repo: "example/app", Labels: []string{"self-hosted", "Windows", "X64", "app-ci"}},
		{Name: "hooks-1", Repo: "example/hooks", Labels: []string{"self-hosted", "Windows", "X64"}},
		{Name: "old-1", Repo: "example/old"}, // labels could not be read
	}
	waiting := func(repo string, labels ...string) Job { return Job{Repo: repo, Labels: labels} }
	cases := []struct {
		name string
		job  Job
		want bool
	}{
		{"ran on our runner", Job{Repo: "example/app", Runner: "app-1"}, true},
		{"ran on someone else's runner", Job{Repo: "example/app", Runner: "GitHub Actions 7", Labels: []string{"self-hosted"}}, false},
		{"ran on another PC's runner that has our labels", Job{Repo: "example/app", Runner: "other-pc-1", Labels: []string{"app-ci"}}, false},

		// The workflow says `runs-on: app-ci`: the job never mentions self-hosted.
		{"waiting, asks only for a custom label our runner carries", waiting("example/app", "app-ci"), true},
		{"waiting, asks for several labels, all carried", waiting("example/app", "self-hosted", "Windows", "app-ci"), true},
		{"labels compare without regard to case", waiting("example/app", "Self-Hosted", "WINDOWS", "App-CI"), true},
		{"waiting, one label our runner lacks", waiting("example/app", "self-hosted", "Linux"), false},
		{"waiting for a GitHub-hosted runner", waiting("example/app", "ubuntu-latest"), false},
		{"a custom label carried only by another repo's runner", waiting("example/hooks", "app-ci"), false},
		{"a repo we have no runner for", waiting("example/elsewhere", "self-hosted"), false},
		{"a job with no labels matches nothing", waiting("example/app"), false},

		// Labels unread: only the label every self-hosted runner has can be trusted.
		{"labels unread: asks for self-hosted", waiting("example/old", "self-hosted", "Windows"), true},
		{"labels unread: self-hosted in another case", waiting("example/old", "SELF-HOSTED"), true},
		{"labels unread: asks only for a custom label", waiting("example/old", "old-ci"), false},
	}
	for _, c := range cases {
		if got := Ours(c.job, mine); got != c.want {
			t.Errorf("%s: Ours = %v, want %v", c.name, got, c.want)
		}
	}
	if Ours(waiting("example/app", "app-ci"), nil) {
		t.Error("with no runners nothing is ours")
	}
}

func TestSummarize(t *testing.T) {
	runners := []Runner{
		{Name: "app-1", Repo: "example/app", State: ServiceRunning, Busy: true},
		{Name: "app-2", Repo: "example/app", State: ServiceStopped},
		{Name: "billing-1", Repo: "acme/billing", State: ServiceUnknown},
	}
	active := []Job{
		queued("example/app", 1, 10, 50*time.Minute),
		queued("example/app", 2, 10, 5*time.Minute),
		{ID: 3, Repo: "example/app", Status: StatusInProgress, Runner: "app-1", QueuedAt: ago(time.Hour), StartedAt: ago(time.Minute)},
	}
	recent := []Job{
		done("example/app", "unit", ConclusionFailure, 4, time.Minute, 10*time.Minute),
		done("example/app", "unit", ConclusionFailure, 5, time.Minute, 2*time.Hour), // too old to count
		done("example/app", "unit", ConclusionSuccess, 6, time.Minute, time.Minute),
	}
	s := Summarize(runners, active, recent, t0)
	if s.Total != 3 || s.Online != 2 || s.Busy != 1 || s.Waiting != 2 || s.OldestWait != 3000 || s.FailedHour != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if len(s.Repos) != 2 || s.Repos[0].Repo != "acme/billing" || s.Repos[1].Repo != "example/app" {
		t.Fatalf("repos not sorted by name: %+v", s.Repos)
	}
	if app := s.Repos[1]; app.Runners != 2 || app.Busy != 1 || app.Waiting != 2 || app.OldestWait != 3000 {
		t.Errorf("app = %+v", app)
	}
	if b := s.Repos[0]; b.Runners != 1 || b.Waiting != 0 || b.OldestWait != 0 {
		t.Errorf("billing = %+v", b)
	}
	if empty := Summarize(nil, nil, nil, t0); empty.Repos == nil {
		t.Error("Repos must be an empty list, not null, for the JSON the pages read")
	}
}

func TestTrayLevelAndTooltip(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
		want Level
		tip  string
	}{
		{"idle", Snapshot{Summary: Summary{Total: 9}}, LevelIdle, "Runwatch: 0/9 busy, 0 waiting"},
		{"running", Snapshot{Summary: Summary{Total: 9, Busy: 2, Waiting: 3, OldestWait: 14 * 60}}, LevelRunning, "(oldest 14m)"},
		{"waiting past the warning", Snapshot{Summary: Summary{Busy: 2, Waiting: 3, OldestWait: 15*60 + 1}}, LevelWaiting, "3 waiting"},
		{"a failure beats a long wait", Snapshot{Summary: Summary{Busy: 2, OldestWait: 3600, Waiting: 1, FailedHour: 2}}, LevelFailed, "2 failed in the last hour"},
		{"a failing read beats everything", Snapshot{Summary: Summary{FailedHour: 2}, Problems: []Problem{{Source: "github example/app", Text: "HTTP 500"}}}, LevelProblem, "NOT CURRENT: 1 read(s) failing"},
	}
	for _, c := range cases {
		if got := TrayLevel(c.snap); got != c.want {
			t.Errorf("%s: level = %d, want %d", c.name, got, c.want)
		}
		if tip := Tooltip(c.snap); !strings.Contains(tip, c.tip) {
			t.Errorf("%s: tooltip %q lacks %q", c.name, tip, c.tip)
		}
	}
}

func TestUsualIsTheMedianOfTheNewestSuccesses(t *testing.T) {
	var history []Job // newest first
	for i, secs := range []int{100, 300, 200} {
		history = append(history, done("example/app", "unit", ConclusionSuccess, int64(i+1), time.Duration(secs)*time.Second, time.Duration(i)*time.Hour))
	}
	history = append(history,
		done("example/app", "unit", ConclusionFailure, 10, 5*time.Second, 4*time.Hour), // failures do not count
		done("example/app", "lint", ConclusionSuccess, 11, 7*time.Second, 5*time.Hour), // another job
		done("acme/billing", "unit", ConclusionSuccess, 12, 9*time.Second, 6*time.Hour),
		Job{Repo: "example/app", Name: "unit", Status: StatusCompleted, Conclusion: ConclusionSuccess}, // never started
	)
	if got := Usual(history, "example/app", "unit"); got != 200 {
		t.Errorf("Usual = %d, want the median 200", got)
	}
	if got := Usual(history, "example/app", "nope"); got != 0 {
		t.Errorf("Usual with no history = %d, want 0", got)
	}
	var many []Job
	for i := range 30 { // 20 newest run 60 s, 10 older run an hour
		ran := time.Minute
		if i >= UsualSample {
			ran = time.Hour
		}
		many = append(many, done("example/app", "unit", ConclusionSuccess, int64(100+i), ran, time.Duration(i)*time.Hour))
	}
	if got := Usual(many, "example/app", "unit"); got != 60 {
		t.Errorf("Usual = %d, want 60: only the newest %d count", got, UsualSample)
	}
}

func TestAlerts(t *testing.T) {
	failedNew := done("example/app", "unit (engine)", ConclusionFailure, 7, 4*time.Minute, time.Minute)
	failedOld := done("example/app", "lint", ConclusionFailure, 8, time.Minute, 3*time.Hour)
	snap := Snapshot{
		At:     t0,
		Recent: []Job{failedNew, failedOld, done("example/app", "core", ConclusionSuccess, 9, time.Minute, time.Minute)},
		Active: []Job{
			queued("example/app", 1, 10, 50*time.Minute),
			queued("example/app", 2, 10, 50*time.Minute), // same run: one alert
			queued("example/app", 3, 11, 44*time.Minute), // not yet
			{ID: 4, RunID: 12, Repo: "example/app", Status: StatusInProgress, QueuedAt: ago(2 * time.Hour)},
		},
	}
	got := Alerts(snap, ago(time.Hour)) // the app started an hour ago
	if len(got) != 2 {
		t.Fatalf("alerts = %+v, want the new failure and one long wait", got)
	}
	if got[0].Key != "failed:example/app:7" || got[0].Link != "#job=example/app:7" || !strings.Contains(got[0].Body, "app · feature/x on app-1 after 4m 00s") {
		t.Errorf("failure alert = %+v", got[0])
	}
	if got[1].Key != "wait:example/app:10" || got[1].Link != "#queue" {
		t.Errorf("wait alert = %+v", got[1])
	}
}

func TestDurations(t *testing.T) {
	cases := []struct {
		d           time.Duration
		long, short string
	}{
		{-time.Second, "0s", "0s"},
		{42 * time.Second, "42s", "42s"},
		{4*time.Minute + 7*time.Second, "4m 07s", "4m"},
		{time.Hour + 2*time.Minute + 30*time.Second, "1h 02m", "1h02"},
	}
	for _, c := range cases {
		if got := Dur(c.d); got != c.long {
			t.Errorf("Dur(%s) = %q, want %q", c.d, got, c.long)
		}
		if got := ShortDur(c.d); got != c.short {
			t.Errorf("ShortDur(%s) = %q, want %q", c.d, got, c.short)
		}
	}
	if ShortRepo("example/app") != "app" || ShortRepo("app") != "app" {
		t.Error("ShortRepo")
	}
}

func TestParseLogLine(t *testing.T) {
	at := time.Date(2026, 3, 2, 18, 46, 45, 826455800, time.UTC)
	cases := []struct {
		name, line string
		want       LogLine
	}{
		{"stamped", "2026-03-02T18:46:45.8264558Z ##[group]Run tests", LogLine{At: at, Text: "##[group]Run tests"}},
		{"stamp only", "2026-03-02T18:46:45.8264558Z", LogLine{At: at}},
		{"byte order mark at the start of a file", "\xef\xbb\xbf2026-03-02T18:46:45.8264558Z first", LogLine{At: at, Text: "first"}},
		{"no stamp keeps the previous time", "  at test/queue.test.js:10:2", LogLine{At: t0, Text: "  at test/queue.test.js:10:2"}},
	}
	for _, c := range cases {
		if got := ParseLogLine(c.line, t0); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
