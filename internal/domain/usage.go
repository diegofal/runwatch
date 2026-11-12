package domain

import (
	"sort"
	"time"
)

// usageWindow is the time a Usage averages over: long enough that a number
// can be read before it moves, short enough to follow a job starting.
const usageWindow = 10 * time.Second

// Proc is one process in a Reading.
type Proc struct {
	PID, Parent int
	// Created tells a process from an earlier one that had the same PID.
	Created time.Time
	CPU     time.Duration // processor time used since it started
	Mem     int64         // bytes of memory in use (working set)
}

// Reading is this PC's processors, memory and processes at one moment.
type Reading struct {
	At    time.Time
	Cores int // logical processors
	// Busy is the processor time spent not idle since the PC started, all
	// processors added up.
	Busy              time.Duration
	MemTotal, MemUsed int64
	Procs             []Proc
}

// RunnerUsage is what one runner's processes (its service and everything
// started under it) use.
type RunnerUsage struct {
	CPU   float64 `json:"cpu"` // processors' worth, averaged over the window
	Mem   int64   `json:"mem"`
	Procs int     `json:"procs"`
}

// Usage is how busy this PC is. CPU numbers are processors' worth (1.5 =
// one and a half processors busy), averaged over WindowSec.
type Usage struct {
	At        time.Time `json:"at"`
	WindowSec float64   `json:"windowSec"`
	Cores     int       `json:"cores"`
	CPU       float64   `json:"cpu"`     // everything on the PC
	Runners   float64   `json:"runners"` // the runners' share of CPU
	// Unassigned is busy time no process in two readings in a row accounts
	// for: processes that started and ended between readings, and the
	// system's own interrupt handling. It may belong to a runner or not.
	Unassigned float64 `json:"unassigned"`
	MemTotal   int64   `json:"memTotal"`
	MemUsed    int64   `json:"memUsed"`
}

// Meter turns successive Readings into Usage. Windows keeps processor time
// per process only while the process lives, so the Meter adds up what each
// process used between readings and remembers which runner a process
// belongs to after the parent that links it to the runner has ended.
type Meter struct {
	owner  map[int]owned
	last   map[int]Proc
	marks  []mark
	seen   time.Duration            // processor time of all processes, added up over the readings
	byName map[string]time.Duration // the same, per runner
}

type owned struct {
	created time.Time
	name    string
}

// mark is the running totals at one reading.
type mark struct {
	at         time.Time
	busy, seen time.Duration
	by         map[string]time.Duration
}

// Measure takes a Reading and the PID of each runner's service process
// (PID → runner name). It returns the PC's Usage and each runner's, by
// name; ok is false on the first reading, when there is nothing to compare
// with yet.
func (m *Meter) Measure(r Reading, roots map[int]string) (Usage, map[string]RunnerUsage, bool) {
	first := m.last == nil
	per := m.account(r.Procs, roots, first)

	now := mark{at: r.At, busy: r.Busy, seen: m.seen, by: make(map[string]time.Duration, len(m.byName))}
	for k, v := range m.byName {
		now.by[k] = v
	}
	m.marks = append(m.marks, now)
	// Keep the newest mark that is a whole window old, and everything after it.
	for len(m.marks) > 2 && r.At.Sub(m.marks[1].at) >= usageWindow {
		m.marks = m.marks[1:]
	}
	from := m.marks[0]
	dt := r.At.Sub(from.at)
	if first || dt <= 0 {
		return Usage{}, nil, false
	}
	rate := func(d time.Duration) float64 {
		if d < 0 {
			return 0
		}
		return float64(d) / float64(dt)
	}
	u := Usage{At: r.At, WindowSec: dt.Seconds(), Cores: r.Cores, MemTotal: r.MemTotal, MemUsed: r.MemUsed}
	busy, seen := now.busy-from.busy, now.seen-from.seen
	u.CPU = rate(busy)
	u.Unassigned = rate(busy - seen)
	for name, ru := range per {
		ru.CPU = rate(now.by[name] - from.by[name])
		per[name] = ru
		u.Runners += ru.CPU
	}
	return u, per, true
}

// account decides each process's runner, adds the processor time used since
// the last reading to the running totals, and returns each runner's memory
// and process count. The first reading only sets the starting point.
func (m *Meter) account(list []Proc, roots map[int]string, first bool) map[string]RunnerUsage {
	procs := append([]Proc(nil), list...)
	// Parents first, so a child finds its parent's owner already decided.
	sort.SliceStable(procs, func(a, b int) bool { return procs[a].Created.Before(procs[b].Created) })
	alive := make(map[int]bool, len(procs))
	for _, p := range procs {
		alive[p.PID] = true
	}
	if m.byName == nil {
		m.byName = map[string]time.Duration{}
	}
	owner := make(map[int]owned, len(m.owner))
	last := make(map[int]Proc, len(procs))
	per := map[string]RunnerUsage{}
	for _, name := range roots {
		per[name] = RunnerUsage{}
	}
	for _, p := range procs {
		last[p.PID] = p
		name := m.ownerOf(p, roots, owner, alive)
		if name != "" {
			owner[p.PID] = owned{created: p.Created, name: name}
			u := per[name]
			u.Mem += p.Mem
			u.Procs++
			per[name] = u
		}
		if first || p.PID == 0 { // PID 0 is the idle time, not work
			continue
		}
		used := p.CPU
		if old, ok := m.last[p.PID]; ok && old.Created.Equal(p.Created) {
			used -= old.CPU
		}
		if used <= 0 {
			continue
		}
		m.seen += used
		if name != "" {
			m.byName[name] += used
		}
	}
	m.owner, m.last = owner, last
	return per
}

// ownerOf is the runner a process belongs to, "" when none: the service
// process itself, a process already known, or the child of one. A parent
// that has ended is looked up in the previous reading; Created guards
// against a PID that was given to a new process.
func (m *Meter) ownerOf(p Proc, roots map[int]string, owner map[int]owned, alive map[int]bool) string {
	if name := roots[p.PID]; name != "" {
		return name
	}
	if o, ok := m.owner[p.PID]; ok && o.created.Equal(p.Created) {
		return o.name
	}
	if p.Parent == p.PID {
		return ""
	}
	if o, ok := owner[p.Parent]; ok && !o.created.After(p.Created) {
		return o.name
	}
	if o, ok := m.owner[p.Parent]; ok && !alive[p.Parent] && !o.created.After(p.Created) {
		return o.name
	}
	return ""
}
