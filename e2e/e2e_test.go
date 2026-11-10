// Package e2e runs the real runwatch program, built from this tree, as a
// separate process against a pretend GitHub and pretend runner folders
// (internal/fakegh), and drives it the way a user's browser does: over
// HTTP, and with a real Chrome on the dashboard and the flyout. Nothing
// here touches a real repository, a real runner or the real data folder.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"runwatch/internal/fakegh"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "runwatch-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "runwatch-e2e")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// Not stripped: Defender sometimes quarantines tiny stripped Go binaries.
	build := exec.Command("go", "build", "-o", binary, "../cmd/runwatch")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build runwatch: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// app is one running copy of the program and the world it watches.
type app struct {
	t       *testing.T
	world   *fakegh.World
	url     string
	dataDir string
	apiURL  string
	cmd     *exec.Cmd
	out     *bytes.Buffer
}

const token = "made-up-e2e-token"

// start builds a world with runners app-1, app-2 (example/app) and
// billing-1 (acme/billing) and runs the program on it.
func start(t *testing.T) *app {
	t.Helper()
	a := &app{t: t, dataDir: t.TempDir()}
	a.world = fakegh.New(filepath.Join(t.TempDir(), "runners"))
	a.world.Token = token
	for _, r := range [][2]string{{"example/app", "app-1"}, {"example/app", "app-2"}, {"acme/billing", "billing-1"}} {
		if _, err := a.world.AddRunner(r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	api := httptest.NewServer(a.world.Handler())
	t.Cleanup(api.Close)
	a.apiURL = api.URL
	cfg := `{"roots":[],"addr":"127.0.0.1:7878","managed":{"billing-*":"Managed by the billing repo's script."},"notifications":false}`
	if err := os.WriteFile(filepath.Join(a.dataDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	a.run()
	t.Cleanup(a.stop)
	return a
}

func (a *app) run() {
	a.t.Helper()
	_ = os.Remove(filepath.Join(a.dataDir, "url.txt"))
	a.out = &bytes.Buffer{}
	a.cmd = exec.Command(binary, "--headless")
	a.cmd.Env = append(os.Environ(),
		"RUNWATCH_DATA_DIR="+a.dataDir,
		"RUNWATCH_ROOTS="+a.world.Root(),
		"RUNWATCH_GITHUB_API="+a.apiURL,
		"RUNWATCH_GITHUB_TOKEN="+token,
		"RUNWATCH_ADDR=127.0.0.1:0", // any free port; the program writes the one it got to url.txt
		"RUNWATCH_POLL_MS=200",
		"RUNWATCH_SERVICES=off", // never look at the real runner services on the machine running the tests
	)
	a.cmd.Stdout, a.cmd.Stderr = a.out, a.out
	if err := a.cmd.Start(); err != nil {
		a.t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(a.dataDir, "url.txt"))
		if err == nil && strings.HasPrefix(string(raw), "http://127.0.0.1:") {
			a.url = strings.TrimSpace(string(raw))
			return
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("the program did not start: %s", a.out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (a *app) stop() {
	if a.cmd == nil || a.cmd.Process == nil {
		return
	}
	_ = a.cmd.Process.Kill() // this exact process, by its handle
	_, _ = a.cmd.Process.Wait()
	a.cmd = nil
	if a.t.Failed() {
		log, _ := os.ReadFile(filepath.Join(a.dataDir, "runwatch.log"))
		a.t.Logf("program output:\n%s\nrunwatch.log:\n%s", a.out, log)
	}
}

// state is the part of /api/state the tests read.
type state struct {
	Version string `json:"version"`
	Runners []struct {
		Name    string `json:"name"`
		Repo    string `json:"repo"`
		Busy    bool   `json:"busy"`
		JobName string `json:"jobName"`
		JobID   int64  `json:"jobId"`
		Managed string `json:"managed"`
	} `json:"runners"`
	Active  []job `json:"active"`
	Recent  []job `json:"recent"`
	Summary struct {
		Total      int `json:"total"`
		Busy       int `json:"busy"`
		Waiting    int `json:"waiting"`
		OldestWait int `json:"oldestWaitSec"`
		FailedHour int `json:"failedHour"`
	} `json:"summary"`
	Problems []struct {
		Source string `json:"source"`
		Text   string `json:"text"`
	} `json:"problems"`
	Events []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"events"`
}

type job struct {
	ID         int64    `json:"id"`
	RunID      int64    `json:"runId"`
	Repo       string   `json:"repo"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Conclusion string   `json:"conclusion"`
	Runner     string   `json:"runner"`
	Labels     []string `json:"labels"`
	Attempt    int      `json:"attempt"`
	UsualSec   int      `json:"usualSec"`
}

func (a *app) state() state {
	a.t.Helper()
	var s state
	a.getJSON("/api/state", &s)
	return s
}

func (a *app) getJSON(path string, out any) int {
	a.t.Helper()
	resp, err := http.Get(a.url + path)
	if err != nil {
		a.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test reading a response
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, out); err != nil {
		a.t.Fatalf("GET %s: %v: %s", path, err, raw)
	}
	return resp.StatusCode
}

// post sends an action the way the pages do; header false leaves out the
// X-Runwatch header, as a page on another site would have to.
func (a *app) post(path string, body any, header bool) (int, map[string]any) {
	a.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, a.url+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if header {
		req.Header.Set("X-Runwatch", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test reading a response
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// until waits for the program to show what the test just did to the world.
func (a *app) until(what string, ok func(state) bool) state {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s := a.state()
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			raw, _ := json.MarshalIndent(s, "", " ")
			a.t.Fatalf("timed out waiting for %s; state:\n%s", what, raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type logChunk struct {
	Lines []struct {
		Text string `json:"text"`
	} `json:"lines"`
	Next   int    `json:"next"`
	Source string `json:"source"`
	Done   bool   `json:"done"`
	Note   string `json:"note"`
}

func (a *app) log(repo string, jobID int64, from int) logChunk {
	a.t.Helper()
	var c logChunk
	if status := a.getJSON(fmt.Sprintf("/api/log?repo=%s&job=%d&from=%d", repo, jobID, from), &c); status != http.StatusOK {
		a.t.Fatalf("log of job %d: HTTP %d", jobID, status)
	}
	return c
}

func (a *app) must(err error) {
	a.t.Helper()
	if err != nil {
		a.t.Fatal(err)
	}
}

func hasPost(posts []string, want string) bool {
	for _, p := range posts {
		if p == want {
			return true
		}
	}
	return false
}

// TestADayOfWork walks the whole product through the API a page uses:
// discovery, the queue, a running job with its live output, stopping a run,
// the finished log, re-running, an outage, and a restart.
func TestADayOfWork(t *testing.T) {
	a := start(t)

	// 1. The runners on "this PC" are found, idle, with the billing one marked as managed elsewhere.
	s := a.until("three idle runners", func(s state) bool { return s.Summary.Total == 3 })
	if s.Summary.Busy != 0 || s.Summary.Waiting != 0 || len(s.Problems) != 0 || s.Version == "" {
		t.Fatalf("fresh start: %+v, problems %+v", s.Summary, s.Problems)
	}
	if r := s.Runners[0]; r.Name != "billing-1" || r.Repo != "acme/billing" || !strings.Contains(r.Managed, "billing repo's script") {
		t.Errorf("billing runner = %+v", r)
	}

	// 2. Someone pushes: three jobs wait.
	run := a.world.Push("example/app", "feature/ledger-rows", "unit", "lint", "e2e")
	unit, lint := run.Jobs[0].ID, run.Jobs[1].ID
	s = a.until("three jobs waiting", func(s state) bool { return s.Summary.Waiting == 3 })
	if s.Active[0].Status != "queued" || s.Active[0].RunID != run.ID {
		t.Errorf("queued job = %+v", s.Active[0])
	}

	// 3. A runner picks one up; its output can be followed while it runs.
	a.must(a.world.Start(unit, "app-1", "Set up job", "Run tests", "Complete job"))
	a.must(a.world.Output(unit, "##[group]Run tests", "  ok lease floor fits a core job"))
	s = a.until("app-1 busy with its GitHub job", func(s state) bool {
		return s.Summary.Busy == 1 && s.Summary.Waiting == 2 && s.Runners[1].JobID == unit
	})
	if r := s.Runners[1]; r.Name != "app-1" || !r.Busy || r.JobName != "unit" {
		t.Errorf("app-1 = %+v", r)
	}
	var chunk logChunk
	deadline := time.Now().Add(10 * time.Second)
	for chunk = a.log("example/app", unit, 0); len(chunk.Lines) < 2 && time.Now().Before(deadline); chunk = a.log("example/app", unit, 0) {
		time.Sleep(100 * time.Millisecond)
	}
	if chunk.Source != "live" || chunk.Done || len(chunk.Lines) != 2 || chunk.Lines[1].Text != "  ok lease floor fits a core job" {
		t.Fatalf("live log = %+v", chunk)
	}
	a.must(a.world.Output(unit, "  ok ledger rows keep order"))
	for more := a.log("example/app", unit, chunk.Next); ; more = a.log("example/app", unit, chunk.Next) {
		if len(more.Lines) == 1 && more.Lines[0].Text == "  ok ledger rows keep order" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the next live line never came: %+v", more)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 4. Only this machine's own pages may act.
	if status, _ := a.post("/api/cancel", map[string]any{"repo": "example/app", "runId": run.ID}, false); status != http.StatusForbidden {
		t.Fatalf("a request without the X-Runwatch header was accepted: %d", status)
	}
	if status, out := a.post("/api/cancel", map[string]any{"repo": "example/app", "runId": 1}, true); status != http.StatusConflict {
		t.Fatalf("stopping a run Runwatch does not watch: %d %v", status, out)
	}
	if posts := a.world.Posts(); len(posts) != 0 {
		t.Fatalf("refused requests reached GitHub: %v", posts)
	}

	// 5. Stop the run: GitHub is told, all three jobs end cancelled, the runner is free.
	status, out := a.post("/api/cancel", map[string]any{"repo": "example/app", "runId": run.ID}, true)
	if status != http.StatusOK || out["jobs"] != float64(3) {
		t.Fatalf("stop run: %d %v", status, out)
	}
	if !hasPost(a.world.Posts(), fmt.Sprintf("/repos/example/app/actions/runs/%d/cancel", run.ID)) {
		t.Fatalf("GitHub was not asked to cancel: %v", a.world.Posts())
	}
	s = a.until("the run stopped", func(s state) bool { return len(s.Active) == 0 && len(s.Recent) == 3 && s.Summary.Busy == 0 })
	for _, j := range s.Recent {
		if j.Conclusion != "cancelled" {
			t.Errorf("job %s = %s", j.Name, j.Conclusion)
		}
	}
	said := false
	for _, e := range s.Events {
		said = said || (e.Kind == "cancelled" && strings.Contains(e.Text, "you stopped run"))
	}
	if !said {
		t.Errorf("the stop is not in what happened: %+v", s.Events)
	}

	// 6. The finished job's whole log comes from GitHub; a job that never started says so.
	chunk = a.log("example/app", unit, 0)
	if chunk.Source != "github" || !chunk.Done || len(chunk.Lines) != 3 || chunk.Lines[0].Text != "##[group]Run tests" {
		t.Fatalf("finished log = %+v", chunk)
	}
	if never := a.log("example/app", lint, 0); never.Source != "none" || !strings.Contains(never.Note, "never started") {
		t.Errorf("log of a job cancelled in the queue = %+v", never)
	}

	// 7. Re-run the stopped job: a second attempt waits; it runs and passes.
	if status, out = a.post("/api/rerun-job", map[string]any{"repo": "example/app", "jobId": unit}, true); status != http.StatusOK {
		t.Fatalf("re-run: %d %v", status, out)
	}
	s = a.until("the second attempt waiting", func(s state) bool { return len(s.Active) == 1 && s.Active[0].Attempt == 2 })
	again := s.Active[0].ID
	a.must(a.world.Start(again, "app-2"))
	a.must(a.world.Output(again, "  ok second time"))
	a.until("app-2 busy", func(s state) bool { return s.Summary.Busy == 1 })
	a.must(a.world.Finish(again, "success"))
	s = a.until("the second attempt passed", func(s state) bool { return len(s.Active) == 0 && len(s.Recent) == 4 })
	if s.Recent[0].ID != again || s.Recent[0].Conclusion != "success" || s.Recent[0].Runner != "app-2" {
		t.Errorf("newest finished job = %+v", s.Recent[0])
	}
	// GitHub's own refusals reach the user in GitHub's words.
	a.world.Push("example/app", "feature/ledger-rows", "unit")
	live := a.until("a new run waiting", func(s state) bool { return len(s.Active) == 1 }).Active[0]
	a.must(a.world.Start(live.ID, "app-1"))
	a.must(a.world.Finish(live.ID, "failure"))
	s = a.until("the failure", func(s state) bool { return s.Summary.FailedHour == 1 })
	if s.Recent[0].UsualSec < 0 {
		t.Errorf("usual = %d", s.Recent[0].UsualSec)
	}
	if status, out = a.post("/api/rerun-failed", map[string]any{"repo": "example/app", "runId": live.RunID}, true); status != http.StatusOK {
		t.Fatalf("re-run failed jobs: %d %v", status, out)
	}
	a.until("the failed job waiting again", func(s state) bool { return len(s.Active) == 1 && s.Active[0].Attempt == 2 })
	if status, out = a.post("/api/rerun-failed", map[string]any{"repo": "example/app", "runId": live.RunID}, true); status != http.StatusBadGateway || !strings.Contains(fmt.Sprint(out["error"]), "already running") {
		t.Errorf("re-run on a live run: %d %v; want GitHub's refusal passed on", status, out)
	}

	// 8. Runners managed elsewhere cannot be started or stopped from here.
	if status, out = a.post("/api/service", map[string]any{"runner": "billing-1", "action": "stop"}, true); status != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "billing repo's script") {
		t.Errorf("service stop on a managed runner: %d %v", status, out)
	}

	// 9. GitHub goes down: the failure is on screen and the last numbers stay; then it clears.
	a.world.FailNext("/repos/example/app/", http.StatusServiceUnavailable)
	s = a.until("the outage to show", func(s state) bool { return len(s.Problems) > 0 })
	if s.Problems[0].Source != "github example/app" || !strings.Contains(s.Problems[0].Text, "503") || len(s.Active) != 1 {
		t.Errorf("during the outage: problems %+v, active %d", s.Problems, len(s.Active))
	}
	a.world.Heal()
	a.until("the outage to clear", func(s state) bool { return len(s.Problems) == 0 })

	// 10. Quit and start again: history is still there, and what finished before is not announced again.
	before := len(a.state().Recent)
	a.stop()
	a.run()
	s = a.until("history after a restart", func(s state) bool { return len(s.Recent) == before && s.Summary.Total == 3 })
	if len(s.Events) != 0 {
		t.Errorf("after a restart old jobs are announced as news: %+v", s.Events)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "history.jsonl")); err != nil {
		t.Errorf("history file: %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(a.dataDir, "runwatch.log"))
	if strings.Contains(string(log), token) || strings.Contains(a.out.String(), token) {
		t.Error("the GitHub token is in the log or the program's output")
	}
}

// TestTheStreamPushesChanges checks /api/events, which the pages listen to
// instead of polling.
func TestTheStreamPushesChanges(t *testing.T) {
	a := start(t)
	a.until("runners", func(s state) bool { return s.Summary.Total == 3 })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.url+"/api/events", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test reading a response
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	waiting := func() int {
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var s state
				if err := json.Unmarshal([]byte(data), &s); err != nil {
					t.Fatalf("event: %v", err)
				}
				return s.Summary.Waiting
			}
		}
		t.Fatalf("the stream ended: %v", sc.Err())
		return -1
	}
	if got := waiting(); got != 0 {
		t.Fatalf("first event: %d waiting", got)
	}
	a.world.Push("example/app", "feature/x", "unit", "lint")
	for got := waiting(); got != 2; got = waiting() {
	}
}

// TestASecondCopyRefusesToStart: two copies would both poll and both notify.
func TestASecondCopyRefusesToStart(t *testing.T) {
	a := start(t)
	second := exec.Command(binary, "--headless")
	second.Env = append(os.Environ(),
		"RUNWATCH_DATA_DIR="+t.TempDir(), "RUNWATCH_ROOTS="+a.world.Root(), "RUNWATCH_GITHUB_API="+a.apiURL,
		"RUNWATCH_GITHUB_TOKEN="+token, "RUNWATCH_ADDR="+strings.TrimPrefix(a.url, "http://"), "RUNWATCH_SERVICES=off")
	out, err := second.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "already running") {
		t.Fatalf("second copy on the same address: err %v, output %s", err, out)
	}
	if code := second.ProcessState.ExitCode(); code != 1 {
		t.Errorf("exit code %d", code)
	}
}

// TestTheDemoRunsOnItsOwn: --demo needs no runner, no GitHub and no token.
func TestTheDemoRunsOnItsOwn(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(`{"notifications":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--headless", "--demo")
	cmd.Env = append(os.Environ(), "RUNWATCH_DATA_DIR="+dataDir, "RUNWATCH_ADDR=127.0.0.1:0",
		"RUNWATCH_GITHUB_TOKEN=", "RUNWATCH_GITHUB_API=", "RUNWATCH_ROOTS=")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	a := &app{t: t, dataDir: dataDir}
	deadline := time.Now().Add(20 * time.Second)
	for a.url == "" {
		if raw, err := os.ReadFile(filepath.Join(dataDir, "url.txt")); err == nil {
			a.url = strings.TrimSpace(string(raw))
		} else if time.Now().After(deadline) {
			t.Fatalf("the demo did not start: %s", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	s := a.until("the demo's day", func(s state) bool {
		return s.Summary.Total == 9 && s.Summary.Busy >= 5 && s.Summary.Waiting > 20 && len(s.Recent) > 10
	})
	if len(s.Problems) != 0 {
		t.Errorf("demo problems: %+v", s.Problems)
	}
	// The demo's hooks repo asks for a custom label only; its queue shows because the runner carries it.
	custom := 0
	for _, j := range s.Active {
		if j.Status == "queued" && len(j.Labels) == 1 && j.Labels[0] == "hooks-ci" {
			custom++
		}
	}
	if custom == 0 {
		t.Errorf("the demo shows no waiting job that asks only for a custom label")
	}
	if s.Summary.OldestWait < 50*60 {
		t.Errorf("the demo's queue should show a long wait: %+v", s.Summary)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "history.jsonl")); err == nil {
		t.Error("the demo wrote into the data folder's history; it must keep its own")
	}
	// The demo moves: a running job prints.
	var running job
	for _, j := range s.Active {
		if j.Status == "in_progress" {
			running = j
		}
	}
	first := a.log(running.Repo, running.ID, 0)
	for more := first; more.Next <= first.Next; more = a.log(running.Repo, running.ID, 0) {
		if time.Now().After(deadline.Add(20 * time.Second)) {
			t.Fatalf("the demo's job %d printed nothing new: %+v", running.ID, more)
		}
		time.Sleep(200 * time.Millisecond)
		if st := a.state(); len(st.Active) == 0 {
			break
		}
	}
}
