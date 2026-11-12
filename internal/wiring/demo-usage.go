package wiring

import (
	"context"
	"sync"
	"time"

	"runwatch/internal/core"
	"runwatch/internal/domain"
)

// demoPC stands in for the PC's processors in --demo: the demo's runners
// are folders with no process behind them, so it gives each one a process
// that uses processor time while the runner has a job.
type demoPC struct {
	core.Machine
	started time.Time

	mu      sync.Mutex
	at      time.Time
	busy    time.Duration
	used    map[int]time.Duration
	working map[int]bool
}

const (
	demoCores    = 16
	demoFirstPID = 1000
	demoMemTotal = 64 << 30
)

func newDemoPC(m core.Machine) *demoPC {
	now := time.Now()
	return &demoPC{Machine: m, started: now, at: now, used: map[int]time.Duration{}, working: map[int]bool{}}
}

// Runners gives each demo runner a process ID and notes which have a job.
func (d *demoPC) Runners(ctx context.Context) ([]domain.Runner, error) {
	runners, err := d.Machine.Runners(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.working = map[int]bool{}
	for i := range runners {
		runners[i].PID = demoFirstPID + i
		d.working[runners[i].PID] = runners[i].Busy
	}
	return runners, err
}

// Read is the demo's core.Options.Usage.
func (d *demoPC) Read(context.Context) (domain.Reading, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	dt := now.Sub(d.at)
	d.at = now
	r := domain.Reading{At: now, Cores: demoCores, MemTotal: demoMemTotal, MemUsed: demoMemTotal / 4}
	d.busy += dt * 2 // the rest of the pretend PC keeps two processors busy
	for pid, working := range d.working {
		if working {
			// Between one and two processors per job, different per runner.
			share := dt + dt*time.Duration((pid-demoFirstPID)%4)/4
			d.used[pid] += share
			d.busy += share
			r.MemUsed += 600 << 20
		}
		mem := int64(90 << 20)
		if working {
			mem = 600 << 20
		}
		r.Procs = append(r.Procs, domain.Proc{PID: pid, Created: d.started, CPU: d.used[pid], Mem: mem})
	}
	r.Busy = d.busy
	return r, nil
}
