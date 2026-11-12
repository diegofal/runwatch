package domain

import (
	"math"
	"testing"
	"time"
)

const sec = time.Second

// reading builds a Reading at t0+at with the PC's busy time and its processes.
func reading(at, busy time.Duration, procs ...Proc) Reading {
	return Reading{At: t0.Add(at), Cores: 8, Busy: busy, MemTotal: 64 << 30, MemUsed: 16 << 30, Procs: procs}
}

// proc is a process started at t0+born.
func proc(pid, parent int, born, cpu time.Duration, mem int64) Proc {
	return Proc{PID: pid, Parent: parent, Created: t0.Add(born), CPU: cpu, Mem: mem}
}

func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func TestMeterGivesEachRunnerItsProcessesTime(t *testing.T) {
	var m Meter
	roots := map[int]string{100: "app-1", 200: "app-2"}
	idle := proc(0, 0, -time.Hour, 500*sec, 0)
	if _, _, ok := m.Measure(reading(0, 100*sec,
		idle, proc(100, 4, -time.Hour, 5*sec, 10), proc(101, 100, -time.Minute, 20*sec, 30),
		proc(200, 4, -time.Hour, 1*sec, 10), proc(900, 4, -time.Hour, 50*sec, 99)), roots); ok {
		t.Fatal("the first reading has nothing to compare with")
	}
	// Two seconds later: app-1's worker used 3 s, a child of it started and
	// used 1 s, something else on the PC used 2 s, and 1 s nobody is seen
	// to have used. The idle time is not work.
	idle.CPU += 9 * sec
	u, per, ok := m.Measure(reading(2*sec, 107*sec,
		idle, proc(100, 4, -time.Hour, 5*sec, 10), proc(101, 100, -time.Minute, 23*sec, 30), proc(102, 101, 1*sec, 1*sec, 60),
		proc(200, 4, -time.Hour, 1*sec, 10), proc(900, 4, -time.Hour, 52*sec, 99)), roots)
	if !ok {
		t.Fatal("the second reading must give numbers")
	}
	if u.Cores != 8 || !near(u.WindowSec, 2) || !near(u.CPU, 3.5) || !near(u.Runners, 2) || !near(u.Unassigned, 0.5) || u.MemUsed != 16<<30 || u.MemTotal != 64<<30 {
		t.Errorf("the PC: %+v", u)
	}
	if a := per["app-1"]; !near(a.CPU, 2) || a.Mem != 100 || a.Procs != 3 {
		t.Errorf("app-1: %+v", a)
	}
	if b := per["app-2"]; b.CPU != 0 || b.Mem != 10 || b.Procs != 1 {
		t.Errorf("app-2 is idle: %+v", b)
	}
}

func TestMeterKeepsAProcessWithItsRunnerAfterItsParentEnds(t *testing.T) {
	var m Meter
	roots := map[int]string{100: "app-1"}
	svc := proc(100, 4, -time.Hour, 0, 1)
	m.Measure(reading(0, 0, svc), roots)
	// The service starts a shell, which starts a test.
	m.Measure(reading(2*sec, 2*sec, svc, proc(110, 100, 1*sec, 1*sec, 1), proc(111, 110, 1*sec, 0, 1)), roots)
	// The shell ends; the test goes on, and starts a child of its own
	// between two readings. A process with the old shell's PID, but
	// started before our shell, and its child are someone else's.
	u, per, _ := m.Measure(reading(4*sec, 10*sec, svc,
		proc(111, 110, 1*sec, 2*sec, 1), proc(112, 111, 3*sec, 1*sec, 1)), roots)
	if a := per["app-1"]; !near(a.CPU, 1) || a.Procs != 3 {
		t.Errorf("the orphaned test and its child stay with app-1: %+v (PC %+v)", a, u)
	}
	u, per, _ = m.Measure(reading(6*sec, 20*sec, svc,
		proc(111, 110, 1*sec, 2*sec, 1), proc(112, 111, 3*sec, 1*sec, 1),
		proc(110, 4, 5*sec, 4*sec, 1), proc(113, 110, 5*sec, 4*sec, 1)), roots)
	if a := per["app-1"]; a.Procs != 3 || !near(a.CPU, 4.0/6) {
		t.Errorf("a reused PID is not the runner's: %+v", a)
	}
	if !near(u.CPU, 20.0/6) || !near(u.Runners, 4.0/6) {
		t.Errorf("the PC over the whole window: %+v", u)
	}
}

func TestMeterDoesNotAdoptByAParentPIDFromBeforeTheChild(t *testing.T) {
	var m Meter
	roots := map[int]string{100: "app-1"}
	// PID 100 was someone else's before the service got it: a process that
	// names 100 as its parent but is older than the service is not its child.
	m.Measure(reading(0, 0, proc(100, 4, -time.Minute, 0, 1), proc(50, 100, -time.Hour, 0, 1), proc(60, 60, -time.Hour, 0, 1)), roots)
	_, per, _ := m.Measure(reading(2*sec, 4*sec, proc(100, 4, -time.Minute, 1*sec, 1), proc(50, 100, -time.Hour, 3*sec, 1), proc(60, 60, -time.Hour, 0, 1)), roots)
	if a := per["app-1"]; a.Procs != 1 || !near(a.CPU, 0.5) {
		t.Errorf("app-1: %+v", a)
	}
}

func TestMeterAveragesOverTheLastTenSeconds(t *testing.T) {
	var m Meter
	roots := map[int]string{100: "app-1"}
	var u Usage
	var per map[string]RunnerUsage
	// One processor busy for 20 s, then four.
	used := time.Duration(0)
	for i := 0; i <= 15; i++ {
		at := time.Duration(i) * 2 * sec
		if i > 0 {
			used += 2 * sec
			if i > 10 {
				used += 6 * sec
			}
		}
		u, per, _ = m.Measure(reading(at, used, proc(100, 4, -time.Hour, used, 1)), roots)
		if i == 10 && (!near(u.CPU, 1) || !near(u.WindowSec, 10)) {
			t.Errorf("after 20 s at one processor: %+v", u)
		}
	}
	if !near(u.CPU, 4) || !near(per["app-1"].CPU, 4) || !near(u.WindowSec, 10) || u.Unassigned != 0 {
		t.Errorf("10 s into four processors busy, the average is four: %+v %+v", u, per)
	}
}

func TestMeterNeverGoesNegative(t *testing.T) {
	var m Meter
	roots := map[int]string{100: "app-1"}
	m.Measure(reading(0, 50*sec, proc(100, 4, -time.Hour, 9*sec, 1)), roots)
	// A counter that went backwards, and a reading with the same time as the last.
	u, per, ok := m.Measure(reading(2*sec, 40*sec, proc(100, 4, -time.Hour, 8*sec, 1)), roots)
	if !ok || u.CPU != 0 || u.Unassigned != 0 || per["app-1"].CPU != 0 {
		t.Errorf("counters that went backwards: %+v %+v", u, per)
	}
	var again Meter
	again.Measure(reading(0, 0), nil)
	if _, _, ok := again.Measure(reading(0, 0), nil); ok {
		t.Error("two readings at the same moment cannot give a rate")
	}
}
