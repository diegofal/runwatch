// Package github is the slice of GitHub's Actions REST API Runwatch uses:
// list runs and their jobs, fetch a finished job's log, cancel a run, re-run
// a job or a run's failed jobs. Every GET is conditional (ETag): an
// unchanged answer comes back as 304, which GitHub does not count against
// the rate limit, so polling every few seconds stays cheap.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"runwatch/internal/domain"
)

// DefaultBase is GitHub's public API.
const DefaultBase = "https://api.github.com"

const (
	perPage = 100
	// maxPages bounds one listing: 1,000 runs or jobs is far beyond what
	// one machine's runners serve, and an endless Link chain must not hang a poll.
	maxPages = 10
	// maxLog bounds a job log held in memory.
	maxLog = 32 << 20
)

// TokenSource gives the token to use for a repo.
type TokenSource interface {
	Token(ctx context.Context, repo string) (string, error)
}

// Client talks to one GitHub API host.
type Client struct {
	base   string
	http   *http.Client
	tokens TokenSource

	mu    sync.Mutex
	cache map[string]cached
	rate  Rate
}

type cached struct {
	etag string
	body []byte
}

// Rate is the rate limit GitHub last reported.
type Rate struct {
	Remaining int
	Reset     time.Time
	Known     bool
}

// New returns a client for the API at base ("" = DefaultBase).
func New(base string, tokens TokenSource, hc *http.Client) *Client {
	if base == "" {
		base = DefaultBase
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: strings.TrimRight(base, "/"), http: hc, tokens: tokens, cache: map[string]cached{}}
}

// Rate returns the last rate limit seen.
func (c *Client) Rate() Rate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rate
}

type apiRun struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	Event        string    `json:"event"`
	DisplayTitle string    `json:"display_title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Actor        struct {
		Login string `json:"login"`
	} `json:"actor"`
}

// Runs lists a repo's runs with the given status ("queued", "in_progress",
// "completed"), newest first, at most limit of them.
func (c *Client) Runs(ctx context.Context, repo, status string, limit int) ([]domain.Run, error) {
	var out []domain.Run
	for page := 1; page <= maxPages && len(out) < limit; page++ {
		var body struct {
			Runs []apiRun `json:"workflow_runs"`
		}
		n := min(perPage, limit)
		path := fmt.Sprintf("/repos/%s/actions/runs?status=%s&per_page=%d&page=%d", repo, status, n, page)
		if err := c.get(ctx, repo, path, &body); err != nil {
			return nil, err
		}
		for _, r := range body.Runs {
			out = append(out, domain.Run{
				ID: r.ID, Status: r.Status, Conclusion: r.Conclusion, Branch: r.HeadBranch, SHA: r.HeadSHA,
				Event: r.Event, Actor: r.Actor.Login, Workflow: r.Name, Title: r.DisplayTitle,
				CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			})
		}
		if len(body.Runs) < n {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type apiJob struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	RunnerName  string     `json:"runner_name"`
	Labels      []string   `json:"labels"`
	RunAttempt  int        `json:"run_attempt"`
	HTMLURL     string     `json:"html_url"`
	Steps       []struct {
		Number      int        `json:"number"`
		Name        string     `json:"name"`
		Status      string     `json:"status"`
		Conclusion  string     `json:"conclusion"`
		StartedAt   *time.Time `json:"started_at"`
		CompletedAt *time.Time `json:"completed_at"`
	} `json:"steps"`
}

// Jobs lists the jobs of a run's latest attempt, with the run's facts
// copied in.
func (c *Client) Jobs(ctx context.Context, repo string, run domain.Run) ([]domain.Job, error) {
	var out []domain.Job
	for page := 1; page <= maxPages; page++ {
		var body struct {
			Jobs []apiJob `json:"jobs"`
		}
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?filter=latest&per_page=%d&page=%d", repo, run.ID, perPage, page)
		if err := c.get(ctx, repo, path, &body); err != nil {
			return nil, err
		}
		for _, j := range body.Jobs {
			out = append(out, toJob(repo, run, j))
		}
		if len(body.Jobs) < perPage {
			break
		}
	}
	return out, nil
}

func toJob(repo string, run domain.Run, j apiJob) domain.Job {
	job := domain.Job{
		ID: j.ID, RunID: run.ID, Repo: repo, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion,
		QueuedAt: j.CreatedAt, StartedAt: at(j.StartedAt), FinishedAt: at(j.CompletedAt),
		Runner: j.RunnerName, Labels: j.Labels, Attempt: j.RunAttempt, URL: j.HTMLURL,
		Branch: run.Branch, SHA: run.SHA, Event: run.Event, Actor: run.Actor, Workflow: run.Workflow, Title: run.Title,
	}
	for _, s := range j.Steps {
		job.Steps = append(job.Steps, domain.Step{
			Number: s.Number, Name: s.Name, Status: s.Status, Conclusion: s.Conclusion,
			StartedAt: at(s.StartedAt), FinishedAt: at(s.CompletedAt),
		})
	}
	return job
}

func at(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// JobLog downloads a finished job's whole log.
func (c *Client) JobLog(ctx context.Context, repo string, jobID int64) (string, error) {
	// GitHub answers with a redirect to a short-lived download URL; the Go
	// client follows it and drops the Authorization header on the new host.
	resp, err := c.do(ctx, repo, http.MethodGet, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, jobID), "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // read below
	if resp.StatusCode != http.StatusOK {
		return "", apiError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLog))
	if err != nil {
		return "", fmt.Errorf("github: read job log: %w", err)
	}
	return string(raw), nil
}

// CancelRun asks GitHub to cancel a run: every job of it that has not
// finished. GitHub has no way to cancel a single job.
func (c *Client) CancelRun(ctx context.Context, repo string, runID int64) error {
	return c.post(ctx, repo, fmt.Sprintf("/repos/%s/actions/runs/%d/cancel", repo, runID))
}

// RerunJob queues one finished job again.
func (c *Client) RerunJob(ctx context.Context, repo string, jobID int64) error {
	return c.post(ctx, repo, fmt.Sprintf("/repos/%s/actions/jobs/%d/rerun", repo, jobID))
}

// RerunFailed queues a finished run's failed and cancelled jobs again.
func (c *Client) RerunFailed(ctx context.Context, repo string, runID int64) error {
	return c.post(ctx, repo, fmt.Sprintf("/repos/%s/actions/runs/%d/rerun-failed-jobs", repo, runID))
}

func (c *Client) post(ctx context.Context, repo, path string) error {
	resp, err := c.do(ctx, repo, http.MethodPost, path, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // drained by apiError or unused
	if resp.StatusCode/100 != 2 {
		return apiError(resp)
	}
	return nil
}

// get fetches path as JSON into out, using and refreshing the ETag cache.
func (c *Client) get(ctx context.Context, repo, path string, out any) error {
	c.mu.Lock()
	hit, ok := c.cache[path]
	c.mu.Unlock()
	etag := ""
	if ok {
		etag = hit.etag
	}
	resp, err := c.do(ctx, repo, http.MethodGet, path, etag)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read below
	body := hit.body
	switch {
	case resp.StatusCode == http.StatusNotModified && ok:
	case resp.StatusCode == http.StatusOK:
		if body, err = io.ReadAll(resp.Body); err != nil {
			return fmt.Errorf("github: read %s: %w", path, err)
		}
		if tag := resp.Header.Get("ETag"); tag != "" {
			c.mu.Lock()
			c.cache[path] = cached{etag: tag, body: body}
			c.mu.Unlock()
		}
	default:
		return apiError(resp)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("github: parse %s: %w", path, err)
	}
	return nil
}

// Forget drops cached answers for a run, once it is finished and stored.
func (c *Client) Forget(repo string, runID int64) {
	prefix := fmt.Sprintf("/repos/%s/actions/runs/%d/", repo, runID)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if strings.HasPrefix(k, prefix) {
			delete(c.cache, k)
		}
	}
}

func (c *Client) do(ctx context.Context, repo, method, path, etag string) (*http.Response, error) {
	token, err := c.tokens.Token(ctx, repo)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "runwatch")
	req.Header.Set("Authorization", "Bearer "+token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	c.noteRate(resp)
	if resp.StatusCode == http.StatusUnauthorized {
		// The token expired or was revoked: look for one again next time.
		if f, ok := c.tokens.(interface{ Forget(repo string) }); ok {
			f.Forget(repo)
		}
	}
	return resp, nil
}

func (c *Client) noteRate(resp *http.Response) {
	rem, err1 := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	reset, err2 := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err1 != nil || err2 != nil {
		return
	}
	c.mu.Lock()
	c.rate = Rate{Remaining: rem, Reset: time.Unix(reset, 0), Known: true}
	c.mu.Unlock()
}

// StatusError is an answer GitHub gave that is not a success.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message)
}

func apiError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10)) // best effort: the status is the error
	var body struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &body) == nil && body.Message != "" {
		msg = body.Message
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &StatusError{Status: resp.StatusCode, Message: msg}
}
