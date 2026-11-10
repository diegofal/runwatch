// Package server is the local web server: the dashboard and the tray's
// flyout (static files) and the small JSON API they call. It listens on
// 127.0.0.1 only and holds no logic: every answer is the core's.
//
// A local server can be reached by any web page the user visits, so:
// requests must name a loopback Host (stops DNS rebinding), no CORS headers
// are sent (other origins cannot read answers), and a POST needs the
// X-Runwatch header (a cross-site form cannot set it) and, when the browser
// sends an Origin, one that is this server.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"runwatch/internal/adapters/github"
	"runwatch/internal/core"
	"runwatch/internal/domain"
)

// Core is what the server needs from the core.
type Core interface {
	Snapshot() domain.Snapshot
	Subscribe() (<-chan struct{}, func())
	JobLog(ctx context.Context, repo string, jobID int64, from int) (core.LogChunk, error)
	CancelRun(ctx context.Context, repo string, runID int64) (int, error)
	RerunJob(ctx context.Context, repo string, jobID int64) error
	RerunFailed(ctx context.Context, repo string, runID int64) error
	ControlService(ctx context.Context, runner string, start bool) error
}

const (
	// pushEvery is the least time between two states pushed to one page.
	pushEvery = time.Second
	// actionTimeout bounds one action; a service start waits for the admin prompt.
	actionTimeout = 2 * time.Minute
)

type server struct {
	core    Core
	version string
	open    func(link string) error
}

// New returns the handler serving static (the web folder) and the API.
// open shows the dashboard at a fragment in the user's browser; nil when
// there is no tray to do it.
func New(c Core, version string, static fs.FS, open func(link string) error) http.Handler {
	s := &server{core: c, version: version, open: open}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/log", s.log)
	mux.HandleFunc("POST /api/cancel", s.cancel)
	mux.HandleFunc("POST /api/rerun-job", s.rerunJob)
	mux.HandleFunc("POST /api/rerun-failed", s.rerunFailed)
	mux.HandleFunc("POST /api/service", s.service)
	mux.HandleFunc("POST /api/open", s.openLink)
	mux.Handle("GET /", http.FileServerFS(static))
	return guard(mux)
}

// guard applies the rules in the package comment to every request.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopback(r.Host) {
			fail(w, http.StatusForbidden, "this server only answers on 127.0.0.1")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Runwatch") != "1" {
				fail(w, http.StatusForbidden, "missing X-Runwatch header")
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					fail(w, http.StatusForbidden, "request from another origin")
					return
				}
			}
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func loopback(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type stateBody struct {
	domain.Snapshot
	Version string `json:"version"`
}

func (s *server) state(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, stateBody{s.core.Snapshot(), s.version})
}

// events streams the state (server-sent events): once at the start, then
// after each change, at most once per pushEvery.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	changed, stop := s.core.Subscribe()
	defer stop()
	send := func() bool {
		raw, err := json.Marshal(stateBody{s.core.Snapshot(), s.version})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(pushEvery):
		}
		select { // changes during the pause are in the state sent now
		case <-changed:
		default:
		}
		if !send() {
			return
		}
	}
}

func (s *server) log(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	job, err := strconv.ParseInt(q.Get("job"), 10, 64)
	if err != nil || q.Get("repo") == "" {
		fail(w, http.StatusBadRequest, "log needs repo and job")
		return
	}
	from, _ := strconv.Atoi(q.Get("from")) // a missing or bad from means the start
	chunk, err := s.core.JobLog(r.Context(), q.Get("repo"), job, from)
	if err != nil {
		failErr(w, err)
		return
	}
	reply(w, http.StatusOK, chunk)
}

type actionBody struct {
	Repo   string `json:"repo"`
	RunID  int64  `json:"runId"`
	JobID  int64  `json:"jobId"`
	Runner string `json:"runner"`
	Action string `json:"action"`
}

// action decodes the body and runs fn with a deadline.
func (s *server) action(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, b actionBody) (any, error)) {
	var b actionBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&b); err != nil {
		fail(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	out, err := fn(ctx, b)
	if err != nil {
		failErr(w, err)
		return
	}
	reply(w, http.StatusOK, out)
}

func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	s.action(w, r, func(ctx context.Context, b actionBody) (any, error) {
		n, err := s.core.CancelRun(ctx, b.Repo, b.RunID)
		return map[string]any{"ok": true, "jobs": n}, err
	})
}

func (s *server) rerunJob(w http.ResponseWriter, r *http.Request) {
	s.action(w, r, func(ctx context.Context, b actionBody) (any, error) {
		return map[string]any{"ok": true}, s.core.RerunJob(ctx, b.Repo, b.JobID)
	})
}

func (s *server) rerunFailed(w http.ResponseWriter, r *http.Request) {
	s.action(w, r, func(ctx context.Context, b actionBody) (any, error) {
		return map[string]any{"ok": true}, s.core.RerunFailed(ctx, b.Repo, b.RunID)
	})
}

func (s *server) service(w http.ResponseWriter, r *http.Request) {
	s.action(w, r, func(ctx context.Context, b actionBody) (any, error) {
		if b.Action != "start" && b.Action != "stop" {
			return nil, fmt.Errorf("%w: action must be start or stop", core.ErrNotAllowed)
		}
		return map[string]any{"ok": true}, s.core.ControlService(ctx, b.Runner, b.Action == "start")
	})
}

// ErrNoTray is what the open function returns when no tray app is there to
// open the browser (--headless). The page then opens the link itself.
var ErrNoTray = errors.New("no tray app to open the browser")

type openBody struct {
	Link string `json:"link"`
}

// openLink opens the dashboard in the browser for the flyout, which is a
// bare webview. Only a fragment of this dashboard can be opened, never a URL.
func (s *server) openLink(w http.ResponseWriter, r *http.Request) {
	var b openBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&b); err != nil {
		fail(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	if b.Link != "" && !strings.HasPrefix(b.Link, "#") {
		fail(w, http.StatusBadRequest, "link must be empty or start with #")
		return
	}
	err := ErrNoTray
	if s.open != nil {
		err = s.open(b.Link)
	}
	switch {
	case errors.Is(err, ErrNoTray):
		fail(w, http.StatusNotImplemented, err.Error())
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, http.StatusOK, map[string]any{"ok": true})
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // the client went away; nothing to tell it
}

func fail(w http.ResponseWriter, status int, text string) {
	reply(w, status, map[string]string{"error": text})
}

// failErr maps a core error to a status: the caller's mistake, GitHub's
// refusal, or a failure here.
func failErr(w http.ResponseWriter, err error) {
	var gh *github.StatusError
	switch {
	case errors.Is(err, core.ErrUnknownJob):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, core.ErrNotAllowed):
		fail(w, http.StatusConflict, err.Error())
	case errors.As(err, &gh):
		fail(w, http.StatusBadGateway, err.Error())
	default:
		fail(w, http.StatusInternalServerError, err.Error())
	}
}
