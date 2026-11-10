package domain

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	// WaitWarn is the wait after which the tray icon turns amber.
	WaitWarn = 15 * time.Minute
	// WaitAlert is the wait that earns a notification, once per run.
	WaitAlert = 45 * time.Minute
	// FailWindow is how long a failed job keeps the red dot on the icon.
	FailWindow = time.Hour
	// UsualSample is how many past successful runs "usual" is the median of.
	UsualSample = 20
)

// selfHosted is the label every self-hosted runner carries.
const selfHosted = "self-hosted"

// Ours reports whether a job belongs on this machine's runners.
//
// A job that a runner picked up is ours when that runner is one of ours.
// A job still waiting is ours the way GitHub decides it: when one of our
// runners for its repo carries every label the job asks for, compared
// without regard to case. A workflow may ask for a custom label only
// (runs-on: my-ci), so "self-hosted" need not be among them.
//
// For a runner whose labels could not be read (Labels is nil), the rule
// falls back to the one label every self-hosted runner is known to carry:
// the job is ours if it asks for "self-hosted". The caller shows a problem
// in that case, because jobs asking only for custom labels are then missed.
func Ours(j Job, runners []Runner) bool {
	if j.Runner != "" {
		for _, r := range runners {
			if r.Name == j.Runner {
				return true
			}
		}
		return false
	}
	if len(j.Labels) == 0 {
		return false
	}
	for _, r := range runners {
		if r.Repo != j.Repo {
			continue
		}
		if r.Labels == nil {
			if hasLabel(j.Labels, selfHosted) {
				return true
			}
			continue
		}
		if carriesAll(r.Labels, j.Labels) {
			return true
		}
	}
	return false
}

func hasLabel(labels []string, want string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, want) })
}

// carriesAll reports whether have holds every label in want.
func carriesAll(have, want []string) bool {
	for _, w := range want {
		if !hasLabel(have, w) {
			return false
		}
	}
	return true
}

// Summarize computes the tray's numbers. Active holds queued and running
// jobs, recent the finished ones.
func Summarize(runners []Runner, active, recent []Job, now time.Time) Summary {
	s := Summary{Total: len(runners), Repos: []RepoSummary{}}
	byRepo := map[string]*RepoSummary{}
	repo := func(name string) *RepoSummary {
		if r, ok := byRepo[name]; ok {
			return r
		}
		byRepo[name] = &RepoSummary{Repo: name}
		return byRepo[name]
	}
	for _, r := range runners {
		rs := repo(r.Repo)
		rs.Runners++
		if r.State != ServiceStopped {
			s.Online++
		}
		if r.Busy {
			s.Busy++
			rs.Busy++
		}
	}
	for _, j := range active {
		if j.Status != StatusQueued {
			continue
		}
		w := int(j.Waited(now).Seconds())
		rs := repo(j.Repo)
		rs.Waiting++
		rs.OldestWait = max(rs.OldestWait, w)
		s.Waiting++
		s.OldestWait = max(s.OldestWait, w)
	}
	for _, j := range recent {
		if j.Conclusion == ConclusionFailure && now.Sub(j.FinishedAt) < FailWindow {
			s.FailedHour++
		}
	}
	for _, r := range byRepo {
		s.Repos = append(s.Repos, *r)
	}
	sort.Slice(s.Repos, func(a, b int) bool { return s.Repos[a].Repo < s.Repos[b].Repo })
	return s
}

// Level is what the tray icon says.
type Level int

const (
	LevelIdle    Level = iota // nothing running
	LevelRunning              // jobs running, short waits
	LevelWaiting              // something has waited longer than WaitWarn
	LevelFailed               // a job failed within FailWindow
	LevelProblem              // a read is failing: the numbers are not current
)

// TrayLevel picks the icon. A failing read wins: nothing else can be trusted.
func TrayLevel(s Snapshot) Level {
	switch {
	case len(s.Problems) > 0:
		return LevelProblem
	case s.Summary.FailedHour > 0:
		return LevelFailed
	case time.Duration(s.Summary.OldestWait)*time.Second > WaitWarn:
		return LevelWaiting
	case s.Summary.Busy > 0:
		return LevelRunning
	}
	return LevelIdle
}

// Tooltip is the tray icon's one-line summary.
func Tooltip(s Snapshot) string {
	sum := s.Summary
	t := fmt.Sprintf("Runwatch: %d/%d busy, %d waiting", sum.Busy, sum.Total, sum.Waiting)
	if sum.Waiting > 0 {
		t += " (oldest " + ShortDur(time.Duration(sum.OldestWait)*time.Second) + ")"
	}
	if sum.FailedHour > 0 {
		t += fmt.Sprintf(", %d failed in the last hour", sum.FailedHour)
	}
	if n := len(s.Problems); n > 0 {
		t += fmt.Sprintf(". NOT CURRENT: %d read(s) failing", n)
	}
	return t
}

// Usual is the median run time in seconds of the newest UsualSample
// successful jobs of the same repo and name in history (newest first);
// 0 when there are none.
func Usual(history []Job, repo, name string) int {
	var secs []int
	for _, j := range history {
		if j.Repo != repo || j.Name != name || j.Conclusion != ConclusionSuccess || !j.Started() {
			continue
		}
		secs = append(secs, int(j.FinishedAt.Sub(j.StartedAt).Seconds()))
		if len(secs) == UsualSample {
			break
		}
	}
	if len(secs) == 0 {
		return 0
	}
	sort.Ints(secs)
	return secs[len(secs)/2]
}

// Alert is one desktop notification.
type Alert struct {
	Key   string // what it is about; an alert is sent once per key
	Title string
	Body  string
	// Link is the dashboard fragment that shows the subject ("#job=…").
	Link string
}

// Alerts lists what deserves a notification in s: each failed job that
// finished after since, and each run with a job waiting longer than
// WaitAlert. The caller remembers the keys it has already sent.
func Alerts(s Snapshot, since time.Time) []Alert {
	var out []Alert
	for _, j := range s.Recent {
		if j.Conclusion != ConclusionFailure || !j.FinishedAt.After(since) {
			continue
		}
		out = append(out, Alert{
			Key:   fmt.Sprintf("failed:%s:%d", j.Repo, j.ID),
			Title: "Job failed: " + j.Name,
			Body:  fmt.Sprintf("%s · %s on %s after %s", ShortRepo(j.Repo), j.Branch, j.Runner, Dur(j.Ran(s.At))),
			Link:  fmt.Sprintf("#job=%s:%d", j.Repo, j.ID),
		})
	}
	seen := map[string]bool{}
	for _, j := range s.Active {
		key := fmt.Sprintf("wait:%s:%d", j.Repo, j.RunID)
		if j.Status != StatusQueued || seen[key] || j.Waited(s.At) < WaitAlert {
			continue
		}
		seen[key] = true
		out = append(out, Alert{
			Key:   key,
			Title: "Waiting more than 45 min",
			Body:  fmt.Sprintf("%s · %s (run %d) has jobs that have not started", ShortRepo(j.Repo), j.Branch, j.RunID),
			Link:  "#queue",
		})
	}
	return out
}

// ShortRepo is a repo without its owner.
func ShortRepo(repo string) string {
	if i := strings.LastIndexByte(repo, '/'); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// Dur prints a duration the way the UI does: 42s, 4m 07s, 1h 02m.
func Dur(d time.Duration) string {
	sec := max(int(d.Round(time.Second).Seconds()), 0)
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm %02ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh %02dm", sec/3600, sec%3600/60)
}

// ShortDur is Dur without the smaller unit: 42s, 4m, 1h02.
func ShortDur(d time.Duration) string {
	sec := max(int(d.Round(time.Second).Seconds()), 0)
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%dh%02d", sec/3600, sec%3600/60)
}

// ParseLogLine splits a line of job output into its timestamp and text. A
// line without a timestamp takes last, the time of the line before it.
func ParseLogLine(line string, last time.Time) LogLine {
	line = strings.TrimPrefix(line, "\xef\xbb\xbf") // the byte order mark a log file starts with
	stamp, text, _ := strings.Cut(line, " ")
	if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		return LogLine{At: at, Text: text}
	}
	return LogLine{At: last, Text: line}
}
