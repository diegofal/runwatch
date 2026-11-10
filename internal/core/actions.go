package core

import (
	"context"
	"errors"
	"fmt"

	"runwatch/internal/domain"
)

// ErrNotAllowed is returned for an action on something Runwatch does not
// watch: the front ends can only act on runs, jobs and runners in the Snapshot.
var ErrNotAllowed = errors.New("not allowed")

// CancelRun cancels every unfinished job of a run. GitHub cannot cancel a
// single job. It returns how many of this machine's jobs the run had open.
func (w *Watcher) CancelRun(ctx context.Context, repo string, runID int64) (int, error) {
	w.mu.Lock()
	open := 0
	branch := ""
	for _, j := range w.snap.Active {
		if j.Repo == repo && j.RunID == runID {
			open++
			branch = j.Branch
		}
	}
	w.mu.Unlock()
	if open == 0 {
		return 0, fmt.Errorf("%w: run %d of %s has no unfinished jobs here", ErrNotAllowed, runID, repo)
	}
	if err := w.o.GitHub.CancelRun(ctx, repo, runID); err != nil {
		return 0, fmt.Errorf("cancel run %d: %w", runID, err)
	}
	w.acted(domain.Event{Kind: "cancelled", Repo: repo, RunID: runID,
		Text: fmt.Sprintf("%s · %s: you stopped run %d (%d jobs)", domain.ShortRepo(repo), branch, runID, open)})
	return open, nil
}

// RerunJob queues a finished job again.
func (w *Watcher) RerunJob(ctx context.Context, repo string, jobID int64) error {
	job, ok := w.findJob(repo, jobID)
	if !ok || job.Status != domain.StatusCompleted {
		return fmt.Errorf("%w: job %d of %s is not a finished job Runwatch has seen", ErrNotAllowed, jobID, repo)
	}
	if err := w.o.GitHub.RerunJob(ctx, repo, jobID); err != nil {
		return fmt.Errorf("re-run job %d: %w", jobID, err)
	}
	w.acted(domain.Event{Kind: "rerun", Repo: repo, JobID: jobID, RunID: job.RunID,
		Text: fmt.Sprintf("%s · %s: you queued it again", domain.ShortRepo(repo), job.Name)})
	return nil
}

// RerunFailed queues a finished run's failed and cancelled jobs again.
func (w *Watcher) RerunFailed(ctx context.Context, repo string, runID int64) error {
	known := false
	for _, j := range w.o.History.Jobs() {
		if j.Repo == repo && j.RunID == runID {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%w: run %d of %s is not a run Runwatch has seen finish", ErrNotAllowed, runID, repo)
	}
	if err := w.o.GitHub.RerunFailed(ctx, repo, runID); err != nil {
		return fmt.Errorf("re-run the failed jobs of run %d: %w", runID, err)
	}
	w.acted(domain.Event{Kind: "rerun", Repo: repo, RunID: runID,
		Text: fmt.Sprintf("%s: you queued the failed jobs of run %d again", domain.ShortRepo(repo), runID)})
	return nil
}

// ControlService starts or stops a runner's service. Stopping a busy runner
// fails its job.
func (w *Watcher) ControlService(ctx context.Context, runner string, start bool) error {
	w.mu.Lock()
	var found *domain.Runner
	for i := range w.runners {
		if w.runners[i].Name == runner {
			r := w.runners[i]
			found = &r
		}
	}
	w.mu.Unlock()
	switch {
	case found == nil:
		return fmt.Errorf("%w: no runner named %q on this PC", ErrNotAllowed, runner)
	case found.Managed != "":
		return fmt.Errorf("%w: %s is managed elsewhere: %s", ErrNotAllowed, runner, found.Managed)
	case found.Service == "":
		return fmt.Errorf("%w: %s is not installed as a service", ErrNotAllowed, runner)
	}
	if err := w.o.Machine.Control(ctx, found.Service, start); err != nil {
		return err
	}
	w.ScanLocal(ctx)
	return nil
}

// acted records what the user did and asks for a fresh poll.
func (w *Watcher) acted(e domain.Event) {
	e.At = w.o.Now()
	w.mu.Lock()
	w.event(e)
	w.rebuildLocked()
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}
