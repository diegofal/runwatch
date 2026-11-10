// Package domain is what Runwatch knows about the CI runners on this
// machine and the jobs GitHub has for them, plus the pure rules drawn from
// that: who is waiting, for how long, what the tray shows, what deserves a
// notification. No I/O here.
package domain

import "time"

// ServiceState is the state of a runner's OS service.
type ServiceState string

const (
	ServiceRunning ServiceState = "running"
	ServiceStopped ServiceState = "stopped"
	// ServiceUnknown: the service could not be read, or the runner is not
	// installed as one.
	ServiceUnknown ServiceState = "unknown"
)

// Job statuses and conclusions, as GitHub names them.
const (
	StatusQueued     = "queued"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"

	ConclusionSuccess   = "success"
	ConclusionFailure   = "failure"
	ConclusionCancelled = "cancelled"
)

// Runner is one self-hosted runner installed on this machine.
type Runner struct {
	Name    string       `json:"name"`    // the name GitHub knows it by (.runner agentName)
	Dir     string       `json:"dir"`     // its install folder
	Repo    string       `json:"repo"`    // owner/repo it is registered to
	Service string       `json:"service"` // OS service name, "" when none
	State   ServiceState `json:"state"`
	// Busy, JobName, JobStarted and PlanID come from the runner's own log,
	// so they are known before GitHub's API catches up.
	Busy       bool      `json:"busy"`
	JobName    string    `json:"jobName,omitempty"`
	JobStarted time.Time `json:"jobStarted,omitzero"`
	PlanID     string    `json:"-"`
	JobID      int64     `json:"jobId,omitempty"` // GitHub's id, once matched
	DiagBytes  int64     `json:"diagBytes"`
	WorkBytes  int64     `json:"workBytes"`
	// Managed is why Runwatch does not start or stop this service ("" = it does).
	Managed string `json:"managed,omitempty"`
	// Problem is why this runner's own files could not be read ("" = they were).
	Problem string `json:"problem,omitempty"`
}

// Step is one step of a job.
type Step struct {
	Number     int       `json:"number"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// Job is one job of a workflow run, with the run's facts copied in.
type Job struct {
	ID         int64     `json:"id"`
	RunID      int64     `json:"runId"`
	Repo       string    `json:"repo"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	QueuedAt   time.Time `json:"queuedAt"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Runner     string    `json:"runner,omitempty"`
	Labels     []string  `json:"labels,omitempty"`
	Attempt    int       `json:"attempt"`
	Steps      []Step    `json:"steps,omitempty"`
	URL        string    `json:"url,omitempty"`

	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
	Event    string `json:"event"`
	Actor    string `json:"actor"`
	Workflow string `json:"workflow"`
	Title    string `json:"title,omitempty"`

	// UsualSec is the median run time of this repo's last successful jobs
	// of the same name; 0 when there is no history yet.
	UsualSec int `json:"usualSec"`
}

// Started reports whether a runner ever picked the job up. GitHub gives a
// job that was cancelled while waiting a started_at too, but no runner.
func (j Job) Started() bool { return j.Runner != "" && !j.StartedAt.IsZero() }

// Waited is how long the job waited for a runner: until now while queued,
// until it ended when it never started, else until a runner picked it up.
func (j Job) Waited(now time.Time) time.Duration {
	end := now
	switch {
	case j.Started():
		end = j.StartedAt
	case j.Status == StatusCompleted:
		end = j.FinishedAt
	}
	return nonNeg(end.Sub(j.QueuedAt))
}

// Ran is how long the job has run or ran on its runner; 0 if it never started.
func (j Job) Ran(now time.Time) time.Duration {
	if !j.Started() {
		return 0
	}
	end := now
	if j.Status == StatusCompleted {
		end = j.FinishedAt
	}
	return nonNeg(end.Sub(j.StartedAt))
}

func nonNeg(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// Problem is a read that failed. It is always shown: a number Runwatch
// could not refresh is never displayed as current.
type Problem struct {
	Source string    `json:"source"` // e.g. "github example/app", "runner app-1"
	Text   string    `json:"text"`
	Since  time.Time `json:"since"`
}

// Event is one line of "what happened".
type Event struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"` // failed, cancelled, rerun, started, finished, service
	Text  string    `json:"text"`
	Repo  string    `json:"repo,omitempty"`
	JobID int64     `json:"jobId,omitempty"`
	RunID int64     `json:"runId,omitempty"`
}

// RepoSummary is one repo's line in the tray and the dashboard.
type RepoSummary struct {
	Repo       string `json:"repo"`
	Runners    int    `json:"runners"`
	Busy       int    `json:"busy"`
	Waiting    int    `json:"waiting"`
	OldestWait int    `json:"oldestWaitSec"` // 0 when nothing waits
}

// Summary is the handful of numbers the tray shows.
type Summary struct {
	Total      int           `json:"total"`
	Online     int           `json:"online"` // service not stopped
	Busy       int           `json:"busy"`
	Waiting    int           `json:"waiting"`
	OldestWait int           `json:"oldestWaitSec"`
	FailedHour int           `json:"failedHour"`
	Repos      []RepoSummary `json:"repos"`
}

// Snapshot is everything the front ends draw.
type Snapshot struct {
	At      time.Time `json:"at"`
	Runners []Runner  `json:"runners"`
	Active  []Job     `json:"active"` // queued and in progress, oldest queued first
	Recent  []Job     `json:"recent"` // finished, newest first
	Summary Summary   `json:"summary"`
	// Problems lists every read that is failing right now.
	Problems []Problem `json:"problems"`
	Events   []Event   `json:"events"`
}

// LogLine is one line of a job's output.
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Run is a workflow run's facts, copied into each of its jobs.
type Run struct {
	ID         int64
	Status     string
	Conclusion string
	Branch     string
	SHA        string
	Event      string
	Actor      string
	Workflow   string
	Title      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
