package sysusage

import (
	"context"
	"os"
	"testing"
	"time"
)

// Reading the process list changes nothing on the PC and needs no rights.
func TestReadSeesThisProcessAndTheProcessors(t *testing.T) {
	before, err := Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Cores < 1 || before.MemTotal <= 0 || before.MemUsed <= 0 || before.MemUsed > before.MemTotal {
		t.Fatalf("processors and memory: %+v", before)
	}
	me := -1
	for i, p := range before.Procs {
		if p.PID == os.Getpid() {
			me = i
		}
	}
	if me < 0 {
		t.Fatalf("this process is missing among %d", len(before.Procs))
	}
	p := before.Procs[me]
	if p.Parent != os.Getppid() || p.Mem <= 0 || p.Created.After(before.At) || before.At.Sub(p.Created) > time.Hour {
		t.Errorf("this process: %+v (parent %d)", p, os.Getppid())
	}

	// Work for a moment: this process's time and the PC's busy time both grow.
	for end := time.Now().Add(150 * time.Millisecond); time.Now().Before(end); {
	}
	after, err := Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Busy <= before.Busy {
		t.Errorf("busy time did not grow: %s then %s", before.Busy, after.Busy)
	}
	for _, q := range after.Procs {
		if q.PID == os.Getpid() && (q.CPU <= p.CPU || !q.Created.Equal(p.Created)) {
			t.Errorf("this process after working: %+v, before %+v", q, p)
		}
	}
}

func TestParseStopsAtTheEndOfTheBuffer(t *testing.T) {
	if got := parse(nil); got != nil {
		t.Errorf("an empty buffer: %+v", got)
	}
	if got := parse(make([]byte, 16)); got != nil {
		t.Errorf("a buffer shorter than one record: %+v", got)
	}
}
