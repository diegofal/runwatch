package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"runwatch/internal/domain"
)

const (
	// liveCap bounds one running job's lines in memory; the oldest go first.
	liveCap = 50000
	// doneCap is how many finished jobs' logs are kept after download.
	doneCap = 12
)

// liveLog is the output seen so far of the job a runner is running, or of
// the last one it ran.
type liveLog struct {
	started time.Time
	tail    LogTail
	lines   []domain.LogLine
	dropped int // lines cut from the front, so indexes stay stable
	err     string
}

// followLocked starts following a runner's job when it picks one up. The
// lines of its previous job stay until then.
func (w *Watcher) followLocked(r *domain.Runner) {
	if !r.Busy || r.PlanID == "" {
		return
	}
	if cur := w.live[r.Name]; cur != nil && cur.started.Equal(r.JobStarted) {
		return
	}
	w.live[r.Name] = &liveLog{started: r.JobStarted, tail: w.o.NewTail(r.Dir, r.PlanID, r.JobStarted)}
}

// pollTails reads what the busy runners have written since the last call.
func (w *Watcher) pollTails() {
	w.mu.Lock()
	var busy []*liveLog
	for _, r := range w.runners {
		if l := w.live[r.Name]; r.Busy && l != nil && l.started.Equal(r.JobStarted) {
			busy = append(busy, l)
		}
	}
	w.mu.Unlock()
	for _, l := range busy {
		lines, err := l.tail.Poll() // only this goroutine polls
		w.mu.Lock()
		l.lines = append(l.lines, lines...)
		if over := len(l.lines) - liveCap; over > 0 {
			l.lines = append([]domain.LogLine(nil), l.lines[over:]...)
			l.dropped += over
		}
		l.err = ""
		if err != nil {
			l.err = err.Error()
		}
		w.mu.Unlock()
	}
}

// LogChunk is part of a job's output.
type LogChunk struct {
	Lines []domain.LogLine `json:"lines"`
	// Next is the index to ask from next time.
	Next int `json:"next"`
	// Source says where the lines come from: "live" (this PC's runner
	// files, while the job runs), "github" (the whole log, once it ended)
	// or "none".
	Source string `json:"source"`
	// Done is true when no more lines will come.
	Done bool `json:"done"`
	// Note explains an empty or partial answer.
	Note string `json:"note,omitempty"`
}

// ErrUnknownJob is returned for a job that is neither active nor in history.
var ErrUnknownJob = errors.New("unknown job")

// JobLog returns a job's output from line index from on.
func (w *Watcher) JobLog(ctx context.Context, repo string, jobID int64, from int) (LogChunk, error) {
	job, ok := w.findJob(repo, jobID)
	if !ok {
		return LogChunk{}, ErrUnknownJob
	}
	from = max(from, 0)
	switch job.Status {
	case domain.StatusQueued:
		return LogChunk{Lines: []domain.LogLine{}, Next: 0, Source: "none", Note: "Waiting for a runner."}, nil
	case domain.StatusInProgress:
		return w.liveChunk(job, from), nil
	}
	if !job.Started() {
		return LogChunk{Lines: []domain.LogLine{}, Source: "none", Done: true, Note: "This job never started, so it has no output."}, nil
	}
	lines, err := w.doneLog(ctx, repo, jobID)
	if err != nil {
		return LogChunk{}, err
	}
	from = min(from, len(lines))
	return LogChunk{Lines: lines[from:], Next: len(lines), Source: "github", Done: true}, nil
}

func (w *Watcher) liveChunk(job domain.Job, from int) LogChunk {
	w.mu.Lock()
	defer w.mu.Unlock()
	l := w.live[job.Runner]
	if l == nil {
		return LogChunk{Lines: []domain.LogLine{}, Source: "live", Note: "Live output is only available for jobs running on this PC's runners."}
	}
	chunk := LogChunk{Source: "live", Note: l.err}
	switch {
	case l.err != "":
	case l.started.Before(w.started):
		// The runner deletes each block of output once it is uploaded.
		chunk.Note = "This job started before Runwatch did: its earlier output is gone from this PC. The whole log is here when the job ends."
	case len(l.lines) == 0:
		chunk.Note = "No output yet. The runner writes output to disk a few KB at a time, so a quiet step shows nothing for a while."
	}
	// from counts every line ever seen, including the ones dropped.
	start := min(max(from-l.dropped, 0), len(l.lines))
	chunk.Lines = append([]domain.LogLine{}, l.lines[start:]...)
	chunk.Next = l.dropped + len(l.lines)
	return chunk
}

func (w *Watcher) doneLog(ctx context.Context, repo string, jobID int64) ([]domain.LogLine, error) {
	key := fmt.Sprintf("%s:%d", repo, jobID)
	w.mu.Lock()
	lines, ok := w.doneLogs[key]
	w.mu.Unlock()
	if ok {
		return lines, nil
	}
	text, err := w.o.GitHub.JobLog(ctx, repo, jobID)
	if err != nil {
		return nil, fmt.Errorf("download the job's log: %w", err)
	}
	lines = []domain.LogLine{}
	var last time.Time
	for raw := range strings.SplitSeq(strings.TrimRight(text, "\r\n"), "\n") {
		line := domain.ParseLogLine(strings.TrimSuffix(raw, "\r"), last)
		last = line.At
		lines = append(lines, line)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.doneLogs[key]; !ok {
		w.doneLogs[key] = lines
		w.doneKeys = append(w.doneKeys, key)
		if len(w.doneKeys) > doneCap {
			delete(w.doneLogs, w.doneKeys[0])
			w.doneKeys = w.doneKeys[1:]
		}
	}
	return lines, nil
}

// findJob looks a job up in the Snapshot.
func (w *Watcher) findJob(repo string, jobID int64) (domain.Job, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, j := range w.snap.Active {
		if j.ID == jobID && j.Repo == repo {
			return j, true
		}
	}
	for _, j := range w.o.History.Jobs() {
		if j.ID == jobID && j.Repo == repo {
			return j, true
		}
	}
	return domain.Job{}, false
}
