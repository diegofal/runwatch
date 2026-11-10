// Package fakegh is a pretend GitHub plus pretend runners: an in-memory
// Actions API (runs, jobs, logs, cancel, re-run, with ETags) and runner
// folders on disk written the way the real runner writes them (.runner,
// _diag/Runner_*.log, _diag/pages). The tests, the end-to-end tests and
// `runwatch --demo` all run the real app against it, so no test ever
// touches a real repository. Every name in it is made up.
package fakegh

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Job is one job in the fake.
type Job struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	Created    time.Time
	Started    time.Time
	Completed  time.Time
	Runner     string
	Labels     []string
	Attempt    int
	Steps      []Step
	Log        []string // full log lines, with timestamps
	replaced   bool     // an older attempt: not in "latest" listings
	plan       string
	record     string
}

// Step is one step of a fake job.
type Step struct {
	Name       string
	Status     string
	Conclusion string
	Started    time.Time
	Completed  time.Time
}

// Run is one workflow run in the fake.
type Run struct {
	ID      int64
	Repo    string
	Branch  string
	SHA     string
	Actor   string
	Event   string
	Created time.Time
	Jobs    []*Job
	plan    string
}

// Runner is one pretend runner folder.
type Runner struct {
	Name string
	Repo string
	Dir  string
	log  string
	job  *Job
}

// World is the fake GitHub and its runners.
type World struct {
	// Token, when set, is the only bearer token the API accepts.
	Token string
	// Now is the fake's clock.
	Now func() time.Time

	mu      sync.Mutex
	root    string
	runs    []*Run
	runners []*Runner
	nextID  int64
	fail    map[string]int
	posts   []string
	gets    int
	notMod  int
}

// New returns an empty world whose runner folders go under root.
func New(root string) *World {
	return &World{Now: time.Now, root: root, nextID: 5000, fail: map[string]int{}}
}

// Root is the folder the runner folders are in.
func (w *World) Root() string { return w.root }

func (w *World) id() int64 {
	w.nextID++
	return w.nextID
}

func (w *World) guid() string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(w.id(), 10)))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// AddRunner creates a runner folder registered to repo and returns it.
func (w *World) AddRunner(repo, name string) (*Runner, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := filepath.Join(w.root, name)
	if err := os.MkdirAll(filepath.Join(dir, "_diag", "pages"), 0o755); err != nil {
		return nil, err
	}
	// The real runner writes .runner with a byte order mark.
	cfg := fmt.Sprintf("\xef\xbb\xbf{\n  \"agentId\": 1,\n  \"agentName\": %q,\n  \"gitHubUrl\": \"https://github.com/%s\",\n  \"workFolder\": \"_work\"\n}", name, repo)
	if err := os.WriteFile(filepath.Join(dir, ".runner"), []byte(cfg), 0o644); err != nil {
		return nil, err
	}
	r := &Runner{Name: name, Repo: repo, Dir: dir,
		log: filepath.Join(dir, "_diag", "Runner_"+w.Now().UTC().Format("20060102-150405")+"-utc.log")}
	if err := appendFile(r.log, fmt.Sprintf("[%s INFO Runner] Listening for Jobs\n", stamp(w.Now()))); err != nil {
		return nil, err
	}
	w.runners = append(w.runners, r)
	return r, nil
}

// Push creates a run on branch with one queued job per name, created at
// the fake's current time.
func (w *World) Push(repo, branch string, jobs ...string) *Run {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.Now()
	run := &Run{ID: w.id(), Repo: repo, Branch: branch, Actor: "dev-a", Event: "push", Created: now, plan: w.guid()}
	sum := sha256.Sum256([]byte(fmt.Sprint(run.ID)))
	run.SHA = hex.EncodeToString(sum[:20])
	for _, name := range jobs {
		run.Jobs = append(run.Jobs, w.newJob(run, name, 1, now))
	}
	w.runs = append(w.runs, run)
	return run
}

func (w *World) newJob(run *Run, name string, attempt int, now time.Time) *Job {
	return &Job{ID: w.id(), Name: name, Status: "queued", Created: now, Labels: []string{"self-hosted", "Windows"},
		Attempt: attempt, plan: run.plan, record: w.guid()}
}

// Start puts a queued job on a runner: the API shows it in progress and the
// runner's log says "Running job", as the real one does.
func (w *World) Start(jobID int64, runner string, steps ...string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	job, _ := w.job(jobID)
	r := w.runner(runner)
	if job == nil || r == nil || job.Status != "queued" || r.job != nil {
		return fmt.Errorf("fakegh: cannot start job %d on %s", jobID, runner)
	}
	now := w.Now()
	job.Status, job.Started, job.Runner = "in_progress", now, runner
	if len(steps) == 0 {
		steps = []string{"Set up job", "Checkout", "Run tests", "Complete job"}
	}
	for i, name := range steps {
		st := Step{Name: name, Status: "pending"}
		if i == 0 {
			st.Status, st.Started = "in_progress", now
		}
		job.Steps = append(job.Steps, st)
	}
	r.job = job
	return appendFile(r.log, fmt.Sprintf(
		"[%[1]s INFO JobDispatcher] Job request 0 for plan %[2]s job %[3]s received.\n[%[1]s INFO Terminal] WRITE LINE: %[1]s: Running job: %[4]s\n",
		stamp(now), job.plan, job.record, job.Name))
}

// NextStep finishes the job's current step and starts the next one.
func (w *World) NextStep(jobID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	job, _ := w.job(jobID)
	if job == nil {
		return
	}
	now := w.Now()
	for i := range job.Steps {
		if job.Steps[i].Status == "in_progress" {
			job.Steps[i].Status, job.Steps[i].Conclusion, job.Steps[i].Completed = "completed", "success", now
			if i+1 < len(job.Steps) {
				job.Steps[i+1].Status, job.Steps[i+1].Started = "in_progress", now
			}
			return
		}
	}
}

// Output writes lines of the running job's output to the runner's pages
// folder, timestamped, and keeps them for the job's final log.
func (w *World) Output(jobID int64, lines ...string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	job, _ := w.job(jobID)
	if job == nil || job.Status != "in_progress" {
		return fmt.Errorf("fakegh: job %d is not running", jobID)
	}
	r := w.runner(job.Runner)
	var b strings.Builder
	for _, l := range lines {
		line := w.Now().UTC().Format("2006-01-02T15:04:05.0000000Z") + " " + l
		job.Log = append(job.Log, line)
		b.WriteString(line + "\r\n")
	}
	page := filepath.Join(r.Dir, "_diag", "pages", job.plan+"_"+job.record+"_1.log")
	if err := appendFile(page, b.String()); err != nil {
		return err
	}
	return os.Chtimes(page, w.Now(), w.Now()) // the file's time follows the fake's clock
}

// SetLabels replaces a job's runs-on labels.
func (w *World) SetLabels(jobID int64, labels ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if job, _ := w.job(jobID); job != nil {
		job.Labels = labels
	}
}

// Finish ends a running job with conclusion: the runner's log says so and
// its page files are removed, as after an upload.
func (w *World) Finish(jobID int64, conclusion string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	job, _ := w.job(jobID)
	if job == nil || job.Status != "in_progress" {
		return fmt.Errorf("fakegh: job %d is not running", jobID)
	}
	return w.finish(job, conclusion)
}

func (w *World) finish(job *Job, conclusion string) error {
	now := w.Now()
	wasRunning := job.Status == "in_progress"
	job.Status, job.Conclusion, job.Completed = "completed", conclusion, now
	if job.Started.IsZero() {
		job.Started = job.Created // GitHub does this for a job that never ran
	}
	for i := range job.Steps {
		st := &job.Steps[i]
		switch st.Status {
		case "in_progress":
			st.Status, st.Conclusion, st.Completed = "completed", conclusion, now
		case "pending":
			// A job that passed ran every step; one that was cut short skipped the rest.
			st.Status, st.Conclusion = "completed", "skipped"
			if conclusion == "success" {
				st.Conclusion, st.Started, st.Completed = "success", now, now
			}
		}
	}
	if !wasRunning {
		return nil
	}
	r := w.runner(job.Runner)
	r.job = nil
	result := map[string]string{"success": "Succeeded", "failure": "Failed", "cancelled": "Canceled"}[conclusion]
	pages, _ := filepath.Glob(filepath.Join(r.Dir, "_diag", "pages", job.plan+"_"+job.record+"_*.log")) // the pattern is fixed
	for _, p := range pages {
		_ = os.Remove(p) // a file already gone is fine
	}
	return appendFile(r.log, fmt.Sprintf("[%[1]s INFO Terminal] WRITE LINE: %[1]s: Job %[2]s completed with result: %[3]s\n", stamp(now), job.Name, result))
}

// FailNext makes the next n API requests whose path contains part answer
// with status, to play an outage.
func (w *World) FailNext(part string, status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fail[part] = status
}

// Heal ends every outage set with FailNext.
func (w *World) Heal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fail = map[string]int{}
}

// Posts returns the paths of the POST requests received, in order.
func (w *World) Posts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.posts...)
}

// Counts returns how many GETs were answered and how many of them with 304.
func (w *World) Counts() (gets, notModified int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gets, w.notMod
}

// JobsOf returns copies of a run's jobs, old attempts included.
func (w *World) JobsOf(runID int64) []Job {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []Job
	for _, run := range w.runs {
		if run.ID == runID {
			for _, j := range run.Jobs {
				out = append(out, *j)
			}
		}
	}
	return out
}

func (w *World) job(id int64) (*Job, *Run) {
	for _, run := range w.runs {
		for _, j := range run.Jobs {
			if j.ID == id {
				return j, run
			}
		}
	}
	return nil, nil
}

func (w *World) runner(name string) *Runner {
	for _, r := range w.runners {
		if r.Name == name {
			return r
		}
	}
	return nil
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05Z") }

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close() //nolint:errcheck // the write error is the one reported
		return err
	}
	return f.Close()
}

func runStatus(run *Run) (status, conclusion string) {
	queued, running, failed, cancelled := false, false, false, false
	for _, j := range run.Jobs {
		if j.replaced {
			continue
		}
		switch {
		case j.Status == "queued":
			queued = true
		case j.Status == "in_progress":
			running = true
		case j.Conclusion == "failure":
			failed = true
		case j.Conclusion == "cancelled":
			cancelled = true
		}
	}
	switch {
	case queued:
		return "queued", ""
	case running:
		return "in_progress", ""
	case failed:
		return "completed", "failure"
	case cancelled:
		return "completed", "cancelled"
	}
	return "completed", "success"
}

// Handler is the fake API.
func (w *World) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}", func(rw http.ResponseWriter, r *http.Request) {
		w.answer(rw, r, map[string]any{"full_name": r.PathValue("owner") + "/" + r.PathValue("repo")})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", w.listRuns)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs/{run}/jobs", w.listJobs)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/jobs/{job}/logs", w.jobLog)
	mux.HandleFunc("POST /repos/{owner}/{repo}/actions/runs/{run}/cancel", w.cancel)
	mux.HandleFunc("POST /repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs", w.rerunFailed)
	mux.HandleFunc("POST /repos/{owner}/{repo}/actions/jobs/{job}/rerun", w.rerunJob)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		status := 0
		for part, st := range w.fail {
			if strings.Contains(r.URL.Path, part) {
				status = st
			}
		}
		token := w.Token
		if r.Method == http.MethodPost {
			w.posts = append(w.posts, r.URL.Path)
		}
		w.mu.Unlock()
		switch {
		case token != "" && r.Header.Get("Authorization") != "Bearer "+token:
			writeJSON(rw, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		case status != 0:
			writeJSON(rw, status, map[string]string{"message": "fakegh: simulated outage"})
		default:
			mux.ServeHTTP(rw, r)
		}
	})
}

func writeJSON(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(body) // a fake: the client going away is not an error
}

// answer sends body as JSON with an ETag, or 304 when the caller has it.
func (w *World) answer(rw http.ResponseWriter, r *http.Request, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.mu.Lock()
	w.gets++
	hit := r.Header.Get("If-None-Match") == etag
	if hit {
		w.notMod++
	}
	w.mu.Unlock()
	rw.Header().Set("ETag", etag)
	rw.Header().Set("X-RateLimit-Remaining", "4990")
	rw.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	if hit {
		rw.WriteHeader(http.StatusNotModified)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(raw) // a fake: the client going away is not an error
}

func repoOf(r *http.Request) string { return r.PathValue("owner") + "/" + r.PathValue("repo") }

func (w *World) listRuns(rw http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("status")
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page")) // 0 means the default
	if per <= 0 {
		per = 30
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page")) // 0 means the first
	page = max(page, 1)
	w.mu.Lock()
	var all []map[string]any
	for i := len(w.runs) - 1; i >= 0; i-- { // newest first
		run := w.runs[i]
		status, conclusion := runStatus(run)
		if run.Repo != repoOf(r) || (want != "" && want != status) {
			continue
		}
		m := map[string]any{
			"id": run.ID, "name": "CI", "status": status, "head_branch": run.Branch, "head_sha": run.SHA,
			"event": run.Event, "display_title": "Change on " + run.Branch, "created_at": run.Created.UTC(),
			"updated_at": run.Created.UTC(), "actor": map[string]string{"login": run.Actor},
		}
		if conclusion != "" {
			m["conclusion"] = conclusion
		}
		all = append(all, m)
	}
	w.mu.Unlock()
	lo := min((page-1)*per, len(all))
	hi := min(lo+per, len(all))
	runs := all[lo:hi]
	if runs == nil {
		runs = []map[string]any{}
	}
	w.answer(rw, r, map[string]any{"total_count": len(all), "workflow_runs": runs})
}

func (w *World) listJobs(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("run"), 10, 64) // 0 matches no run
	w.mu.Lock()
	var run *Run
	for _, x := range w.runs {
		if x.ID == id && x.Repo == repoOf(r) {
			run = x
		}
	}
	if run == nil {
		w.mu.Unlock()
		writeJSON(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	jobs := []map[string]any{}
	for _, j := range run.Jobs {
		if j.replaced {
			continue
		}
		steps := []map[string]any{}
		for i, s := range j.Steps {
			steps = append(steps, map[string]any{"number": i + 1, "name": s.Name, "status": s.Status,
				"conclusion": null(s.Conclusion), "started_at": nullTime(s.Started), "completed_at": nullTime(s.Completed)})
		}
		started := j.Started
		if j.Status == "queued" {
			started = j.Created // as GitHub answers for a waiting job
		}
		jobs = append(jobs, map[string]any{
			"id": j.ID, "run_id": run.ID, "name": j.Name, "status": j.Status, "conclusion": null(j.Conclusion),
			"created_at": j.Created.UTC(), "started_at": nullTime(started), "completed_at": nullTime(j.Completed),
			"runner_name": null(j.Runner), "labels": j.Labels, "run_attempt": j.Attempt, "steps": steps,
			"html_url": fmt.Sprintf("https://github.com/%s/actions/runs/%d/job/%d", run.Repo, run.ID, j.ID),
		})
	}
	w.mu.Unlock()
	w.answer(rw, r, map[string]any{"total_count": len(jobs), "jobs": jobs})
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func (w *World) jobLog(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("job"), 10, 64) // 0 matches no job
	w.mu.Lock()
	job, _ := w.job(id)
	var text string
	ok := job != nil && job.Status == "completed"
	if ok {
		text = strings.Join(job.Log, "\r\n") + "\r\n"
	}
	w.mu.Unlock()
	if !ok {
		writeJSON(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = rw.Write([]byte(text)) // a fake: the client going away is not an error
}

func (w *World) cancel(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("run"), 10, 64) // 0 matches no run
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, run := range w.runs {
		if run.ID != id || run.Repo != repoOf(r) {
			continue
		}
		if status, _ := runStatus(run); status == "completed" {
			writeJSON(rw, http.StatusConflict, map[string]string{"message": "Cannot cancel a workflow run that is completed."})
			return
		}
		for _, j := range run.Jobs {
			if j.Status != "completed" {
				if err := w.finish(j, "cancelled"); err != nil {
					writeJSON(rw, http.StatusInternalServerError, map[string]string{"message": err.Error()})
					return
				}
			}
		}
		writeJSON(rw, http.StatusAccepted, map[string]any{})
		return
	}
	writeJSON(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func (w *World) rerunJob(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("job"), 10, 64) // 0 matches no job
	w.mu.Lock()
	defer w.mu.Unlock()
	job, run := w.job(id)
	if job == nil || run.Repo != repoOf(r) {
		writeJSON(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	if status, _ := runStatus(run); status != "completed" {
		writeJSON(rw, http.StatusForbidden, map[string]string{"message": "The workflow run containing this job is already running"})
		return
	}
	job.replaced = true
	run.Jobs = append(run.Jobs, w.newJob(run, job.Name, job.Attempt+1, w.Now()))
	writeJSON(rw, http.StatusCreated, map[string]any{})
}

func (w *World) rerunFailed(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("run"), 10, 64) // 0 matches no run
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, run := range w.runs {
		if run.ID != id || run.Repo != repoOf(r) {
			continue
		}
		if status, _ := runStatus(run); status != "completed" {
			writeJSON(rw, http.StatusForbidden, map[string]string{"message": "This workflow is already running"})
			return
		}
		for _, j := range run.Jobs {
			if !j.replaced && j.Conclusion != "success" {
				j.replaced = true
				run.Jobs = append(run.Jobs, w.newJob(run, j.Name, j.Attempt+1, w.Now()))
			}
		}
		writeJSON(rw, http.StatusCreated, map[string]any{})
		return
	}
	writeJSON(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})
}
