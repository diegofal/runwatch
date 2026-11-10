package fakegh

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// Demo fills a World with a believable day and then keeps it moving:
// `runwatch --demo` and the screenshots in the docs run on it.
type Demo struct {
	W     *World
	plans map[int64]*plan
	rnd   *rand.Rand
}

type plan struct {
	length time.Duration
	fail   bool
	steps  []string
}

var demoRepos = []struct {
	repo, short string
	runners     int
	branches    []string
	jobs        []string
}{
	{"example/app", "app", 5, []string{"feature/ledger-rows", "fix/lease-floor", "feature/mentions", "chore/deps-bump", "feature/board-city"},
		[]string{"changes", "core", "unit (engine)", "unit (headless)", "unit (hosts a-h)", "unit (hosts i-z)", "e2e (web board)", "e2e (scenarios)", "lint"}},
	{"example/hooks", "hooks", 1, []string{"main", "fix/exit-codes"}, []string{"test", "lint"}},
	{"acme/billing", "billing", 3, []string{"feature/invoice-pdf", "fix/tax-rounding"}, []string{"api tests", "web tests", "lint", "build"}},
}

// customLabel is what the hooks repo's workflow asks for instead of
// self-hosted (runs-on: hooks-ci): its waiting jobs are found only by
// matching the runner's labels.
const customLabel = "hooks-ci"

// push is World.Push with the repo's runs-on labels applied.
func (d *Demo) push(repo, branch string, jobs ...string) *Run {
	run := d.W.Push(repo, branch, jobs...)
	if repo == "example/hooks" {
		for _, j := range run.Jobs {
			d.W.SetLabels(j.ID, customLabel)
		}
	}
	return run
}

var demoTests = []string{"lease floor fits a core job", "ledger rows keep order", "retry backs off", "board renders city",
	"mentions resolve aliases", "hosts pick a free port", "token refresh survives restart", "tax rounding half-even",
	"invoice pdf has totals", "queue drains in order", "exit code propagates", "cache key includes lockfile"}

// NewDemo creates the runners and two hours of history, and leaves the
// first repo with a long queue, the way a busy day looks. seed fixes the
// random choices (screenshots use a constant).
func NewDemo(w *World, seed uint64) (*Demo, error) {
	d := &Demo{W: w, plans: map[int64]*plan{}, rnd: rand.New(rand.NewPCG(seed, 7))}
	real := w.Now
	defer func() { w.Now = real }()
	start := real()
	at := func(ago time.Duration) { w.Now = func() time.Time { return start.Add(-ago) } }

	at(2 * time.Hour)
	for _, r := range demoRepos {
		for i := 1; i <= r.runners; i++ {
			name := fmt.Sprintf("%s-%d", r.short, i)
			if _, err := w.AddRunner(r.repo, name); err != nil {
				return nil, err
			}
			if r.short == "hooks" {
				w.SetRunnerLabels(name, "self-hosted", "Windows", "X64", customLabel)
			}
		}
	}
	// History: finished jobs spread over the last two hours.
	for _, r := range demoRepos {
		for i := 1; i <= r.runners; i++ {
			runner := fmt.Sprintf("%s-%d", r.short, i)
			for ago := 115 * time.Minute; ago > 8*time.Minute; ago -= time.Duration(6+d.rnd.IntN(14)) * time.Minute {
				if r.short != "app" && d.rnd.IntN(3) > 0 {
					continue // the quieter repos sit idle most of the time
				}
				name := r.jobs[d.rnd.IntN(len(r.jobs))]
				at(ago + time.Duration(d.rnd.IntN(20))*time.Minute)
				run := d.push(r.repo, r.branches[d.rnd.IntN(len(r.branches))], name)
				job := run.Jobs[0]
				at(ago)
				if err := w.Start(job.ID, runner, d.stepsFor(name)...); err != nil {
					return nil, err
				}
				if err := w.Output(job.ID, d.lines(6)...); err != nil {
					return nil, err
				}
				at(ago - time.Duration(90+d.rnd.IntN(240))*time.Second)
				concl := "success"
				switch n := d.rnd.IntN(10); {
				case n == 0:
					concl = "failure"
				case n < 4 && r.short == "app":
					concl = "cancelled"
				}
				if err := w.Finish(job.ID, concl); err != nil {
					return nil, err
				}
			}
		}
	}
	// The queue: runs waiting on the busy repo, oldest first.
	app := demoRepos[0]
	for i, mins := range []int{58, 51, 34, 12, 5} {
		at(time.Duration(mins) * time.Minute)
		d.push(app.repo, app.branches[i], app.jobs...)
	}
	at(3 * time.Minute)
	d.push("example/hooks", "fix/exit-codes", demoRepos[1].jobs...)
	at(90 * time.Second)
	d.push("example/hooks", "main", demoRepos[1].jobs...)
	at(0)
	return d, d.assign()
}

func (d *Demo) stepsFor(name string) []string {
	if name == "lint" {
		return []string{"Set up job", "Checkout", "Install dependencies", "Lint", "Complete job"}
	}
	return []string{"Set up job", "Checkout", "Install dependencies", "Run tests", "Upload report", "Complete job"}
}

func (d *Demo) lines(n int) []string {
	out := make([]string, 0, n)
	for range n {
		out = append(out, fmt.Sprintf("  ✓ %s (%d ms)", demoTests[d.rnd.IntN(len(demoTests))], 3+d.rnd.IntN(900)))
	}
	return out
}

// assign gives each idle runner the oldest waiting job of its repo.
func (d *Demo) assign() error {
	w := d.W
	w.mu.Lock()
	type pick struct {
		job    int64
		runner string
		name   string
	}
	var picks []pick
	taken := map[int64]bool{}
	for _, r := range w.runners {
		if r.job != nil {
			continue
		}
		var oldest *Job
		for _, run := range w.runs {
			if run.Repo != r.Repo {
				continue
			}
			for _, j := range run.Jobs {
				if j.Status == "queued" && !j.replaced && !taken[j.ID] && (oldest == nil || j.Created.Before(oldest.Created)) {
					oldest = j
				}
			}
		}
		if oldest != nil {
			taken[oldest.ID] = true
			picks = append(picks, pick{oldest.ID, r.Name, oldest.Name})
		}
	}
	w.mu.Unlock()
	for _, p := range picks {
		steps := d.stepsFor(p.name)
		d.plans[p.job] = &plan{length: time.Duration(50+d.rnd.IntN(110)) * time.Second, fail: d.rnd.IntN(8) == 0, steps: steps}
		if err := w.Start(p.job, p.runner, steps...); err != nil {
			return err
		}
		if err := w.Output(p.job, "Current runner version: '2.330.0'", "Runner name: '"+p.runner+"'", "Prepare workflow directory"); err != nil {
			return err
		}
	}
	return nil
}

// Drive keeps the world moving until ctx ends: jobs print, change step,
// finish; idle runners pick up the queue; now and then someone pushes.
func (d *Demo) Drive(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := d.step(); err != nil {
				return err
			}
		}
	}
}

func (d *Demo) step() error {
	w := d.W
	now := w.Now()
	type live struct {
		id      int64
		started time.Time
		step    int
	}
	w.mu.Lock()
	var running []live
	for _, r := range w.runners {
		if r.job == nil {
			continue
		}
		cur := 0
		for i, s := range r.job.Steps {
			if s.Status == "in_progress" {
				cur = i
			}
		}
		running = append(running, live{r.job.ID, r.job.Started, cur})
	}
	w.mu.Unlock()
	for _, l := range running {
		p := d.plans[l.id]
		if p == nil {
			p = &plan{length: 90 * time.Second, steps: d.stepsFor("")}
			d.plans[l.id] = p
		}
		elapsed := now.Sub(l.started)
		// Steps share the job's length; the test step takes most of it.
		want := min(int(float64(len(p.steps))*float64(elapsed)/float64(p.length)), len(p.steps)-1)
		for s := l.step; s < want; s++ {
			w.NextStep(l.id)
		}
		if d.rnd.IntN(3) > 0 {
			if err := w.Output(l.id, d.lines(1+d.rnd.IntN(3))...); err != nil {
				return err
			}
		}
		switch {
		case p.fail && elapsed > p.length*7/10:
			if err := w.Output(l.id, "  ✗ "+demoTests[d.rnd.IntN(len(demoTests))], "##[error]Process completed with exit code 1."); err != nil {
				return err
			}
			if err := w.Finish(l.id, "failure"); err != nil {
				return err
			}
		case elapsed > p.length:
			if err := w.Finish(l.id, "success"); err != nil {
				return err
			}
		}
	}
	if d.rnd.IntN(50) == 0 {
		r := demoRepos[d.rnd.IntN(len(demoRepos))]
		d.push(r.repo, r.branches[d.rnd.IntN(len(r.branches))], r.jobs...)
	}
	return d.assign()
}
