package runnerfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runwatch/internal/domain"
	"runwatch/internal/fakegh"
)

// Lines copied in shape from a real Runner_*.log (names made up).
const (
	lineRequest  = "[2026-03-02 18:46:18Z INFO JobDispatcher] Job request 0 for plan 34c87590-a413-8a94-ab1d-772fc3172d22 job 05c59ddd-dcb1-56ab-861a-ff062cb3b018 received.\r\n"
	lineRunning  = "[2026-03-02 18:46:18Z INFO Terminal] WRITE LINE: 2026-03-02 18:46:18Z: Running job: unit (unit-headless)\r\n"
	lineRenew    = "[2026-03-02 18:47:18Z INFO JobDispatcher] Successfully renew job 05c59ddd-dcb1-56ab-861a-ff062cb3b018, job is valid till 03/02/2026 18:57:17\r\n"
	lineDone     = "[2026-03-02 18:50:15Z INFO Terminal] WRITE LINE: 2026-03-02 18:50:15Z: Job unit (unit-headless) completed with result: Canceled\r\n"
	lineListen   = "[2026-03-02 18:40:00Z INFO Terminal] WRITE LINE: 2026-03-02 18:40:00Z: Listening for Jobs\r\n"
	wantPlan     = "34c87590-a413-8a94-ab1d-772fc3172d22"
	wantJobName  = "unit (unit-headless)"
	wantStartRFC = "2026-03-02T18:46:18Z"
)

func TestParseRunnerLog(t *testing.T) {
	start, _ := time.Parse(time.RFC3339, wantStartRFC)
	busy := LogState{Busy: true, JobName: wantJobName, Started: start, PlanID: wantPlan}
	cases := []struct {
		name string
		log  string
		want LogState
	}{
		{"empty", "", LogState{}},
		{"listening only", lineListen, LogState{}},
		{"running", lineListen + lineRequest + lineRunning + lineRenew, busy},
		{"finished", lineRequest + lineRunning + lineRenew + lineDone, LogState{}},
		{"second job after a finished one", lineRequest + lineRunning + lineDone + lineRequest + lineRunning, busy},
		// The tail can start after the request line: busy is still known, the plan is not.
		{"tail cut before the request line", lineRunning + lineRenew, LogState{Busy: true, JobName: wantJobName, Started: start}},
		{"a job name that says completed", strings.ReplaceAll(lineRequest+lineRunning, wantJobName, "Job x completed with result: y"),
			LogState{Busy: true, JobName: "Job x completed with result: y", Started: start, PlanID: wantPlan}},
	}
	for _, c := range cases {
		if got := ParseRunnerLog([]byte(c.log)); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDiscoverAndRead(t *testing.T) {
	root := t.TempDir()
	w := fakegh.New(root)
	for _, name := range []string{"app-2", "app-1"} {
		if _, err := w.AddRunner("example/app", name); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil { // not a runner
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	other, err := fakegh.New(elsewhere).AddRunner("acme/billing", "billing-1")
	if err != nil {
		t.Fatal(err)
	}

	dirs, err := Discover([]string{root}, []string{other.Dir, filepath.Join(root, "app-1")})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 3 {
		t.Fatalf("dirs = %v, want app-1, app-2 and billing-1 once each", dirs)
	}
	if _, err := Discover([]string{filepath.Join(root, "missing")}, nil); err == nil {
		t.Error("a root that cannot be listed must be an error, not an empty list")
	}

	run := w.Push("example/app", "feature/x", "unit")
	r, err := Read(filepath.Join(root, "app-1"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "app-1" || r.Repo != "example/app" || r.Busy || r.State != domain.ServiceUnknown {
		t.Fatalf("idle runner = %+v", r)
	}
	if err := w.Start(run.Jobs[0].ID, "app-1"); err != nil {
		t.Fatal(err)
	}
	r, err = Read(filepath.Join(root, "app-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Busy || r.JobName != "unit" || r.PlanID == "" || time.Since(r.JobStarted) > time.Minute {
		t.Fatalf("busy runner = %+v", r)
	}
	if err := w.Finish(run.Jobs[0].ID, "success"); err != nil {
		t.Fatal(err)
	}
	if r, _ = Read(filepath.Join(root, "app-1")); r.Busy {
		t.Fatalf("runner still busy after its job finished: %+v", r)
	}
}

func TestReadProblems(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".runner"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Read(dir); err == nil {
		t.Error("no .runner: want an error")
	}
	write("{not json")
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "parse .runner") {
		t.Errorf("bad JSON: err = %v", err)
	}
	write(`{"agentName":"org-1","gitHubUrl":"https://github.com/example"}`)
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "organization runners are not supported") {
		t.Errorf("organization runner: err = %v", err)
	}
	write(`{"agentName":"new-1","gitHubUrl":"https://github.com/example/app"}`)
	r, err := Read(dir) // registered but never started: no _diag yet
	if err != nil || r.Busy || r.Name != "new-1" {
		t.Errorf("never started: %+v, %v", r, err)
	}
}

func TestReadOnlyTheNewestLogAndItsTail(t *testing.T) {
	dir := t.TempDir()
	diag := filepath.Join(dir, "_diag")
	if err := os.MkdirAll(diag, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".runner"), []byte(`{"agentName":"app-1","gitHubUrl":"https://github.com/example/app"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The older log ends mid-job (the service was restarted): it must be ignored.
	if err := os.WriteFile(filepath.Join(diag, "Runner_20260301-100000-utc.log"), []byte(lineRequest+lineRunning), 0o644); err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat(lineRenew, tailBytes/len(lineRenew)+10)
	if err := os.WriteFile(filepath.Join(diag, "Runner_20260302-100000-utc.log"), []byte(lineRequest+lineRunning+lineDone+padding+lineListen), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Read(dir)
	if err != nil || r.Busy {
		t.Fatalf("got %+v, %v; want idle", r, err)
	}
}

func TestSizes(t *testing.T) {
	dir := t.TempDir()
	for path, n := range map[string]int{"_diag/a.log": 100, "_diag/pages/b.log": 50, "_work/repo/x": 7} {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	diag, work, err := Sizes(dir)
	if err != nil || diag != 150 || work != 7 {
		t.Errorf("Sizes = %d, %d, %v; want 150, 7", diag, work, err)
	}
	if diag, work, err = Sizes(t.TempDir()); err != nil || diag != 0 || work != 0 {
		t.Errorf("a runner with no folders yet: %d, %d, %v", diag, work, err)
	}
}

func TestTail(t *testing.T) {
	dir := t.TempDir()
	pages := filepath.Join(dir, "_diag", "pages")
	if err := os.MkdirAll(pages, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	stamp := func(sec int) string {
		return start.Add(time.Duration(sec) * time.Second).UTC().Format("2006-01-02T15:04:05.0000000Z")
	}
	appendTo := func(name, text string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(pages, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	texts := func(lines []domain.LogLine) string {
		var out []string
		for _, l := range lines {
			out = append(out, l.Text)
		}
		return strings.Join(out, "|")
	}

	// An earlier job of the same run left a file behind; another run's file is there too.
	appendTo("plan-a_old_1.log", stamp(-600)+" stale\r\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(pages, "plan-a_old_1.log"), old, old); err != nil {
		t.Fatal(err)
	}
	appendTo("plan-b_x_1.log", stamp(1)+" other run\r\n")

	tail := NewTail(dir, "plan-a", start)
	if lines, err := tail.Poll(); err != nil || len(lines) != 0 {
		t.Fatalf("nothing of this job yet: %v, %v", lines, err)
	}
	appendTo("plan-a_step1_1.log", stamp(2)+" one\r\n"+stamp(3)+" tw") // the block ends mid-line
	lines, err := tail.Poll()
	if err != nil || texts(lines) != "one" {
		t.Fatalf("first poll = %q, %v; want only the complete line", texts(lines), err)
	}
	appendTo("plan-a_step1_1.log", "o\r\n  wrapped, no stamp\r\n")
	appendTo("plan-a_step2_1.log", stamp(1)+" earlier step\r\n")
	lines, err = tail.Poll()
	if err != nil || texts(lines) != "earlier step|two|  wrapped, no stamp" {
		t.Fatalf("second poll = %q, %v; want lines in time order", texts(lines), err)
	}
	if !lines[2].At.Equal(lines[1].At) {
		t.Errorf("a line without a stamp takes the previous line's time: %v vs %v", lines[2].At, lines[1].At)
	}
	// The runner uploads and deletes a page; nothing new, no error.
	if err := os.Remove(filepath.Join(pages, "plan-a_step1_1.log")); err != nil {
		t.Fatal(err)
	}
	if lines, err = tail.Poll(); err != nil || len(lines) != 0 {
		t.Fatalf("after delete: %v, %v", lines, err)
	}
	if lines, err = NewTail(t.TempDir(), "plan-a", start).Poll(); err != nil || lines != nil {
		t.Fatalf("no pages folder: %v, %v", lines, err)
	}
}
