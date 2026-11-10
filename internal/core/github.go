package core

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"runwatch/internal/domain"
)

// jobFetchers is how many runs' jobs are fetched at once.
const jobFetchers = 6

// PollGitHub asks GitHub for the runs and jobs of every repo this machine
// has a runner for, stores the jobs that finished, and rebuilds the Snapshot.
func (w *Watcher) PollGitHub(ctx context.Context) {
	w.mu.Lock()
	seen := map[string]bool{}
	names := map[string]bool{}
	var repos []string
	for _, r := range w.runners {
		names[r.Name] = true
		if r.Repo != "" && !seen[r.Repo] {
			seen[r.Repo] = true
			repos = append(repos, r.Repo)
		}
	}
	for repo := range w.repos {
		if !seen[repo] {
			delete(w.repos, repo)
		}
	}
	w.mu.Unlock()
	sort.Strings(repos)

	var wg sync.WaitGroup
	for _, repo := range repos {
		wg.Go(func() { w.pollRepo(ctx, repo, names) })
	}
	wg.Wait()

	w.mu.Lock()
	w.rebuildLocked()
	w.mu.Unlock()
}

// runLists is what one poll asks GitHub to list for a repo.
type runLists struct {
	live []domain.Run // queued or in progress, each once
	past []domain.Run // finished runs, read once at start for history
}

// listRuns lists a repo's unfinished runs and, until the first poll has
// succeeded, its latest finished ones.
func (w *Watcher) listRuns(ctx context.Context, repo string, backfilled bool) (runLists, error) {
	var out runLists
	seen := map[int64]bool{}
	// A run stays "queued" while any of its jobs waits, even with others running.
	for _, status := range []string{domain.StatusQueued, domain.StatusInProgress} {
		runs, err := w.o.GitHub.Runs(ctx, repo, status, 200)
		if err != nil {
			return out, fmt.Errorf("list %s runs: %w", status, err)
		}
		for _, run := range runs {
			if !seen[run.ID] {
				seen[run.ID] = true
				out.live = append(out.live, run)
			}
		}
	}
	if !backfilled {
		runs, err := w.o.GitHub.Runs(ctx, repo, domain.StatusCompleted, backfill)
		if err != nil {
			return out, fmt.Errorf("list finished runs: %w", err)
		}
		out.past = runs
	}
	return out, nil
}

func (w *Watcher) pollRepo(ctx context.Context, repo string, names map[string]bool) {
	w.mu.Lock()
	rs := w.repos[repo]
	if rs == nil {
		rs = &repoState{runs: map[int64]*runState{}}
		w.repos[repo] = rs
	}
	prev := make(map[int64]*runState, len(rs.runs))
	for id, r := range rs.runs {
		prev[id] = r
	}
	backfilled := rs.backfilled
	w.mu.Unlock()

	lists, err := w.listRuns(ctx, repo, backfilled)
	if err != nil {
		// Nothing could be listed: keep everything as it was, beside the problem.
		w.o.Logf("github %s: %v", repo, err)
		w.mu.Lock()
		rs.problem = &domain.Problem{Source: "github " + repo, Text: err.Error(), Since: w.o.Now()}
		w.mu.Unlock()
		return
	}
	next, ended := split(lists.live, prev)
	all := append(append(append([]domain.Run{}, lists.live...), ended...), lists.past...)
	jobs, jobsErr := w.fetchJobs(ctx, repo, all)
	keepUnread(next, prev, lists.live, ended, jobs)

	var finished []domain.Job
	for _, js := range jobs {
		for _, j := range js {
			if j.Status == domain.StatusCompleted && domain.Ours(j, names) {
				finished = append(finished, j)
			}
		}
	}
	added, addErr := w.o.History.Add(finished)

	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.o.Now()
	if backfilled {
		w.newRunEvents(repo, next, prev, now)
	}
	for _, j := range added {
		if j.FinishedAt.After(w.started) { // history from before this start is not news
			w.event(finishedEvent(j, now))
		}
	}
	rs.runs = next
	rs.problem = nil
	switch {
	case jobsErr != nil:
		w.o.Logf("github %s: %v", repo, jobsErr)
		rs.problem = &domain.Problem{Source: "github " + repo, Text: jobsErr.Error(), Since: now}
	case addErr != nil:
		w.o.Logf("history: %v", addErr)
		rs.problem = &domain.Problem{Source: "history", Text: addErr.Error(), Since: now}
	default:
		rs.backfilled = true
	}
}

// split starts the next state from the runs GitHub lists as unfinished,
// and returns the runs that left that list: they have ended, and their
// jobs are read one last time.
func split(live []domain.Run, prev map[int64]*runState) (next map[int64]*runState, ended []domain.Run) {
	next = map[int64]*runState{}
	for _, run := range live {
		next[run.ID] = &runState{run: run}
	}
	for id, old := range prev {
		if next[id] == nil {
			ended = append(ended, old.run)
		}
	}
	return next, ended
}

// keepUnread fills next with the jobs just read. A run whose jobs could
// not be read keeps the ones from the last poll (the repo's problem says
// they are not fresh), and an ended run stays until its end has been read.
func keepUnread(next, prev map[int64]*runState, live, ended []domain.Run, jobs map[int64][]domain.Job) {
	for _, run := range live {
		if got, ok := jobs[run.ID]; ok {
			next[run.ID].jobs = got
		} else if old := prev[run.ID]; old != nil {
			next[run.ID].jobs = old.jobs
		}
	}
	for _, run := range ended {
		if _, ok := jobs[run.ID]; !ok {
			next[run.ID] = prev[run.ID]
		}
	}
}

// newRunEvents records the runs seen for the first time. Call with w.mu held.
func (w *Watcher) newRunEvents(repo string, next, prev map[int64]*runState, now time.Time) {
	for id, r := range next {
		if prev[id] == nil && len(r.jobs) > 0 {
			w.event(domain.Event{At: now, Kind: "queued", Repo: repo, RunID: id,
				Text: fmt.Sprintf("%s · %s: new run (%d jobs)", domain.ShortRepo(repo), r.run.Branch, len(r.jobs))})
		}
	}
}

// finishedEvent is the "what happened" line for a job that just ended.
func finishedEvent(j domain.Job, now time.Time) domain.Event {
	kind, text := "finished", "passed"
	switch j.Conclusion {
	case domain.ConclusionFailure:
		kind, text = "failed", "failed"
	case domain.ConclusionCancelled:
		kind, text = "cancelled", "was cancelled"
	}
	where := ""
	if j.Started() {
		where = fmt.Sprintf(" on %s after %s", j.Runner, domain.Dur(j.Ran(now)))
	}
	return domain.Event{At: j.FinishedAt, Kind: kind, Repo: j.Repo, JobID: j.ID, RunID: j.RunID,
		Text: fmt.Sprintf("%s · %s %s%s", domain.ShortRepo(j.Repo), j.Name, text, where)}
}

// fetchJobs reads the jobs of each run, a few runs at a time. It returns
// what it could read and the first error.
func (w *Watcher) fetchJobs(ctx context.Context, repo string, runs []domain.Run) (map[int64][]domain.Job, error) {
	var (
		mu    sync.Mutex
		out   = map[int64][]domain.Job{}
		first error
		wg    sync.WaitGroup
		slots = make(chan struct{}, jobFetchers)
	)
	for _, run := range runs {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			jobs, err := w.o.GitHub.Jobs(ctx, repo, run)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = fmt.Errorf("list jobs of run %d: %w", run.ID, err)
				}
				return
			}
			out[run.ID] = jobs
		})
	}
	wg.Wait()
	return out, first
}
