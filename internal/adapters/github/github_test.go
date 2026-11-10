package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"runwatch/internal/domain"
	"runwatch/internal/fakegh"
)

type fixed string

func (f fixed) Token(context.Context, string) (string, error) { return string(f), nil }

func world(t *testing.T) (*fakegh.World, *Client) {
	t.Helper()
	w := fakegh.New(t.TempDir())
	w.Token = "made-up-token"
	srv := httptest.NewServer(w.Handler())
	t.Cleanup(srv.Close)
	return w, New(srv.URL, fixed("made-up-token"), nil)
}

func TestRunsAndJobs(t *testing.T) {
	w, c := world(t)
	ctx := context.Background()
	if _, err := w.AddRunner("example/app", "app-1"); err != nil {
		t.Fatal(err)
	}
	run := w.Push("example/app", "feature/x", "unit", "lint")
	w.Push("example/other", "main", "unit") // another repo: never listed
	if err := w.Start(run.Jobs[0].ID, "app-1", "Set up job", "Run tests"); err != nil {
		t.Fatal(err)
	}

	// One job still waits, so GitHub calls the whole run queued.
	runs, err := c.Runs(ctx, "example/app", domain.StatusQueued, 100)
	if err != nil || len(runs) != 1 {
		t.Fatalf("queued runs = %+v, %v", runs, err)
	}
	got := runs[0]
	if got.ID != run.ID || got.Branch != "feature/x" || got.Actor != "dev-a" || got.Event != "push" || got.Workflow != "CI" || got.SHA == "" || got.CreatedAt.IsZero() {
		t.Errorf("run = %+v", got)
	}
	if runs, err = c.Runs(ctx, "example/app", domain.StatusInProgress, 100); err != nil || len(runs) != 0 {
		t.Errorf("in-progress runs = %+v, %v; want none", runs, err)
	}

	jobs, err := c.Jobs(ctx, "example/app", got)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	unit, lint := jobs[0], jobs[1]
	if unit.Status != domain.StatusInProgress || unit.Runner != "app-1" || !unit.Started() || len(unit.Steps) != 2 ||
		unit.Steps[0].Status != "in_progress" || unit.Steps[1].Status != "pending" || unit.Branch != "feature/x" || unit.RunID != run.ID || unit.Repo != "example/app" {
		t.Errorf("running job = %+v", unit)
	}
	if lint.Status != domain.StatusQueued || lint.Started() || lint.QueuedAt.IsZero() || lint.Labels[0] != "self-hosted" || lint.URL == "" {
		t.Errorf("queued job = %+v", lint)
	}
}

func TestRunnerLabels(t *testing.T) {
	w, c := world(t)
	ctx := context.Background()
	for _, name := range []string{"app-1", "app-2"} {
		if _, err := w.AddRunner("example/app", name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.AddRunner("example/other", "other-1"); err != nil {
		t.Fatal(err)
	}
	w.SetRunnerLabels("app-2", "self-hosted", "Windows", "X64", "app-ci")
	labels, err := c.RunnerLabels(ctx, "example/app")
	if err != nil || len(labels) != 2 {
		t.Fatalf("labels = %v, %v; want this repo's two runners", labels, err)
	}
	if got := strings.Join(labels["app-1"], ","); got != "self-hosted,Windows,X64" {
		t.Errorf("app-1 = %q", got)
	}
	if got := strings.Join(labels["app-2"], ","); got != "self-hosted,Windows,X64,app-ci" {
		t.Errorf("app-2 = %q", got)
	}
	// GitHub answers 403 to an account that does not administer the repo.
	w.FailNext("/actions/runners", http.StatusForbidden)
	_, err = c.RunnerLabels(ctx, "example/app")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Errorf("refused: err = %v", err)
	}
}

func TestUnchangedAnswersCostNothing(t *testing.T) {
	w, c := world(t)
	ctx := context.Background()
	run := w.Push("example/app", "feature/x", "unit")
	for range 3 {
		runs, err := c.Runs(ctx, "example/app", domain.StatusQueued, 100)
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs = %+v, %v", runs, err)
		}
		if jobs, err := c.Jobs(ctx, "example/app", runs[0]); err != nil || len(jobs) != 1 {
			t.Fatalf("jobs = %+v, %v", jobs, err)
		}
	}
	gets, notModified := w.Counts()
	if gets != 6 || notModified != 4 {
		t.Errorf("gets = %d, 304s = %d; want 6 and 4: the second and third rounds must be conditional", gets, notModified)
	}
	if r := c.Rate(); !r.Known || r.Remaining != 4990 {
		t.Errorf("rate = %+v", r)
	}
	c.Forget("example/app", run.ID)
	if _, err := c.Jobs(ctx, "example/app", domain.Run{ID: run.ID}); err != nil {
		t.Fatal(err)
	}
	if all, after := w.Counts(); all != gets+1 || after != notModified {
		t.Errorf("after Forget: gets = %d, 304s = %d; want one full answer more and no 304", all, after)
	}
}

func TestPaging(t *testing.T) {
	w, c := world(t)
	for i := range 120 {
		w.Push("example/app", fmt.Sprintf("b%d", i), "unit")
	}
	runs, err := c.Runs(context.Background(), "example/app", domain.StatusQueued, 200)
	if err != nil || len(runs) != 120 {
		t.Fatalf("got %d runs, %v; want all 120 across two pages", len(runs), err)
	}
	seen := map[int64]bool{}
	for _, r := range runs {
		seen[r.ID] = true
	}
	if len(seen) != 120 {
		t.Errorf("pages overlap: %d distinct runs", len(seen))
	}
	if runs, err = c.Runs(context.Background(), "example/app", domain.StatusQueued, 20); err != nil || len(runs) != 20 {
		t.Errorf("limit 20: got %d, %v", len(runs), err)
	}
}

func TestActionsAndLog(t *testing.T) {
	w, c := world(t)
	ctx := context.Background()
	if _, err := w.AddRunner("example/app", "app-1"); err != nil {
		t.Fatal(err)
	}
	run := w.Push("example/app", "feature/x", "unit", "lint")
	unit := run.Jobs[0].ID
	if err := w.Start(unit, "app-1"); err != nil {
		t.Fatal(err)
	}
	if err := w.Output(unit, "##[group]Run tests", "  ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.JobLog(ctx, "example/app", unit); err == nil {
		t.Error("the log of a running job is not available: want an error")
	}
	// Re-running while the run is live is GitHub's to refuse; its words reach the user.
	err := c.RerunJob(ctx, "example/app", unit)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusForbidden || !strings.Contains(err.Error(), "already running") {
		t.Errorf("rerun of a live job: err = %v", err)
	}

	if err := c.CancelRun(ctx, "example/app", run.ID); err != nil {
		t.Fatal(err)
	}
	for _, j := range w.JobsOf(run.ID) {
		if j.Status != "completed" || j.Conclusion != "cancelled" {
			t.Errorf("after cancel, job %s = %s/%s", j.Name, j.Status, j.Conclusion)
		}
	}
	log, err := c.JobLog(ctx, "example/app", unit)
	if err != nil || !strings.Contains(log, "##[group]Run tests") || !strings.Contains(log, "  ok") {
		t.Errorf("log = %q, %v", log, err)
	}
	if err := c.RerunJob(ctx, "example/app", unit); err != nil {
		t.Fatal(err)
	}
	if err := c.RerunFailed(ctx, "example/app", run.ID); err == nil {
		t.Error("the run is live again after the job re-run: re-run failed must be refused")
	}
	want := []string{
		fmt.Sprintf("/repos/example/app/actions/jobs/%d/rerun", unit),
		fmt.Sprintf("/repos/example/app/actions/runs/%d/cancel", run.ID),
		fmt.Sprintf("/repos/example/app/actions/jobs/%d/rerun", unit),
		fmt.Sprintf("/repos/example/app/actions/runs/%d/rerun-failed-jobs", run.ID),
	}
	if got := w.Posts(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("POSTs = %v, want %v", got, want)
	}
}

func TestErrorsCarryGitHubsWords(t *testing.T) {
	w, c := world(t)
	ctx := context.Background()
	w.FailNext("/actions/runs", http.StatusInternalServerError)
	_, err := c.Runs(ctx, "example/app", domain.StatusQueued, 10)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 500 || !strings.Contains(err.Error(), "simulated outage") {
		t.Errorf("err = %v", err)
	}
	w.Heal()
	if _, err := c.Jobs(ctx, "example/app", domain.Run{ID: 1}); err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("unknown run: err = %v", err)
	}

	bad := New(strings.Replace(c.base, "http://", "http://127.0.0.1:1/", 1), fixed("x"), nil)
	if _, err := bad.Runs(ctx, "example/app", domain.StatusQueued, 10); err == nil {
		t.Error("an unreachable API must be an error")
	}
	noToken := New(c.base, tokenErr{}, nil)
	if _, err := noToken.Runs(ctx, "example/app", domain.StatusQueued, 10); err == nil || !strings.Contains(err.Error(), "no token here") {
		t.Errorf("token error must surface: %v", err)
	}
}

type tokenErr struct{}

func (tokenErr) Token(context.Context, string) (string, error) {
	return "", errors.New("no token here")
}

// forgetful records the repos whose token GitHub refused.
type forgetful struct {
	fixed
	forgot []string
}

func (f *forgetful) Forget(repo string) { f.forgot = append(f.forgot, repo) }

func TestARefusedTokenIsForgotten(t *testing.T) {
	w := fakegh.New(t.TempDir())
	w.Token = "the-right-one"
	srv := httptest.NewServer(w.Handler())
	defer srv.Close()
	tokens := &forgetful{fixed: "stale"}
	c := New(srv.URL, tokens, nil)
	_, err := c.Runs(context.Background(), "example/app", domain.StatusQueued, 10)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if len(tokens.forgot) != 1 || tokens.forgot[0] != "example/app" {
		t.Errorf("forgot = %v; a 401 must drop the remembered token so the next poll looks again", tokens.forgot)
	}
}

func TestTokens(t *testing.T) {
	w := fakegh.New(t.TempDir())
	w.Token = "token-b"
	srv := httptest.NewServer(w.Handler())
	defer srv.Close()
	ctx := context.Background()

	if tok, err := NewTokens("from-env", nil, srv.URL, nil).Token(ctx, "example/app"); err != nil || tok != "from-env" {
		t.Errorf("fixed token: %q, %v", tok, err)
	}
	if _, err := NewTokens("", nil, srv.URL, nil).Token(ctx, "example/app"); err == nil || !strings.Contains(err.Error(), "RUNWATCH_GITHUB_TOKEN") {
		t.Errorf("no gh and no token: err = %v", err)
	}

	var calls []string
	status := "github.com\n  ✓ Logged in to github.com account user-a (keyring)\n  - Active account: true\n  ✓ Logged in to github.com account user-b (keyring)\n"
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case args[1] == "status":
			return []byte(status), nil
		case args[len(args)-1] == "user-a":
			return []byte("token-a\n"), nil
		case args[len(args)-1] == "user-b":
			return []byte("token-b\n"), nil
		}
		return nil, errors.New("unexpected gh call")
	}
	tokens := NewTokens("", gh, srv.URL, nil)
	tok, err := tokens.Token(ctx, "example/app")
	if err != nil || tok != "token-b" {
		t.Fatalf("token = %q, %v; want the second account's, the only one that can read the repo", tok, err)
	}
	n := len(calls)
	if tok, _ = tokens.Token(ctx, "example/app"); tok != "token-b" || len(calls) != n {
		t.Errorf("second ask must come from memory: %q, %d gh calls after %d", tok, len(calls), n)
	}
	tokens.Forget("example/app")
	if _, err = tokens.Token(ctx, "example/app"); err != nil || len(calls) == n {
		t.Errorf("after Forget gh is asked again: %v, calls %d", err, len(calls))
	}

	w.Token = "nobody-has-this"
	tokens.Forget("example/app")
	if _, err = tokens.Token(ctx, "example/app"); err == nil || !strings.Contains(err.Error(), "tried: user-a, user-b") {
		t.Errorf("no account fits: err = %v", err)
	}
	for _, c := range calls {
		if strings.Contains(c, "token-") {
			t.Errorf("a token leaked into a gh argument: %q", c)
		}
	}

	loggedOut := func(context.Context, ...string) ([]byte, error) {
		return []byte("You are not logged into any GitHub hosts."), errors.New("exit status 1")
	}
	if _, err = NewTokens("", loggedOut, srv.URL, nil).Token(ctx, "example/app"); err == nil || !strings.Contains(err.Error(), "gh auth status") {
		t.Errorf("gh logged out: err = %v", err)
	}
	empty := func(context.Context, ...string) ([]byte, error) { return nil, nil }
	if _, err = NewTokens("", empty, srv.URL, nil).Token(ctx, "example/app"); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("gh with no accounts: err = %v", err)
	}
}
