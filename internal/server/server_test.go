package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"runwatch/internal/adapters/github"
	"runwatch/internal/core"
	"runwatch/internal/domain"
)

// stub is a Core that records what the server asked of it.
type stub struct {
	mu     sync.Mutex
	snap   domain.Snapshot
	calls  []string
	err    error
	subs   []chan struct{}
	opened []string
}

func (s *stub) Snapshot() domain.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

func (s *stub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()
	return ch, func() {}
}

func (s *stub) change(busy int) {
	s.mu.Lock()
	s.snap.Summary.Busy = busy
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}

func (s *stub) call(format string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf(format, args...))
	return s.err
}

func (s *stub) JobLog(_ context.Context, repo string, jobID int64, from int) (core.LogChunk, error) {
	err := s.call("log %s %d from %d", repo, jobID, from)
	return core.LogChunk{Lines: []domain.LogLine{{Text: "hello"}}, Next: from + 1, Source: "live"}, err
}

func (s *stub) CancelRun(_ context.Context, repo string, runID int64) (int, error) {
	return 3, s.call("cancel %s %d", repo, runID)
}

func (s *stub) RerunJob(_ context.Context, repo string, jobID int64) error {
	return s.call("rerun-job %s %d", repo, jobID)
}

func (s *stub) RerunFailed(_ context.Context, repo string, runID int64) error {
	return s.call("rerun-failed %s %d", repo, runID)
}

func (s *stub) ControlService(_ context.Context, runner string, start bool) error {
	return s.call("service %s start=%v", runner, start)
}

func newServer(t *testing.T) (*stub, *httptest.Server) {
	t.Helper()
	s := &stub{snap: domain.Snapshot{Summary: domain.Summary{Total: 9, Busy: 6}}}
	static := fstest.MapFS{"index.html": {Data: []byte("<title>Runwatch</title>")}}
	open := func(link string) error {
		s.opened = append(s.opened, link)
		return nil
	}
	srv := httptest.NewServer(New(s, "v-test", static, open))
	t.Cleanup(srv.Close)
	return s, srv
}

// post sends a JSON body the way the pages do.
func post(t *testing.T, url, body string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Runwatch", "1")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test reading a response
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test reading a response
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func TestStateStaticAndHeaders(t *testing.T) {
	_, srv := newServer(t)
	resp, body := get(t, srv.URL+"/api/state")
	var st struct {
		Version string `json:"version"`
		Summary struct {
			Total int `json:"total"`
			Busy  int `json:"busy"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil || st.Version != "v-test" || st.Summary.Total != 9 || st.Summary.Busy != 6 {
		t.Fatalf("state = %s (%v)", body, err)
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'", "X-Frame-Options": "DENY",
		"X-Content-Type-Options": "nosniff", "Cache-Control": "no-store",
	} {
		if got := resp.Header.Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want it to contain %q", header, got, want)
		}
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("no other origin may read the API, but Access-Control-Allow-Origin = %q", got)
	}
	if _, body = get(t, srv.URL+"/"); !strings.Contains(body, "<title>Runwatch</title>") {
		t.Errorf("index = %q", body)
	}
	if resp, _ = get(t, srv.URL+"/nope.html"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing file: %d", resp.StatusCode)
	}
}

func TestOnlyThisMachinesPagesMayAct(t *testing.T) {
	s, srv := newServer(t)
	body := `{"repo":"example/app","runId":7}`
	cases := []struct {
		name    string
		headers map[string]string
		host    string
		want    int
	}{
		{"the dashboard itself", map[string]string{"Origin": srv.URL}, "", http.StatusOK},
		{"no Origin (the flyout webview, curl)", nil, "", http.StatusOK},
		{"a form on another site cannot set the header", map[string]string{"X-Runwatch": ""}, "", http.StatusForbidden},
		{"a script on another site", map[string]string{"Origin": "https://evil.example"}, "", http.StatusForbidden},
		{"an unparsable Origin", map[string]string{"Origin": "null"}, "", http.StatusForbidden},
		{"another name pointed at 127.0.0.1 (DNS rebinding)", nil, "evil.example", http.StatusForbidden},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/cancel", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Runwatch", "1")
		for k, v := range c.headers {
			if v == "" {
				req.Header.Del(k)
			} else {
				req.Header.Set(k, v)
			}
		}
		if c.host != "" {
			req.Host = c.host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if len(s.calls) != 2 {
		t.Errorf("only the two allowed requests may reach the core: %v", s.calls)
	}
	// Reading is refused under a foreign Host too.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/state", http.NoBody)
	req.Host = "evil.example:7878"
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET with a foreign Host: %d", resp.StatusCode)
	}
	for host, want := range map[string]bool{"127.0.0.1:7878": true, "localhost:7878": true, "[::1]:7878": true, "localhost": true,
		"192.168.1.5:7878": false, "evil.example": false, "": false} {
		if loopback(host) != want {
			t.Errorf("loopback(%q) = %v", host, !want)
		}
	}
}

func TestActionsReachTheCore(t *testing.T) {
	s, srv := newServer(t)
	steps := []struct{ path, body, want string }{
		{"/api/cancel", `{"repo":"example/app","runId":7}`, "cancel example/app 7"},
		{"/api/rerun-job", `{"repo":"example/app","jobId":8}`, "rerun-job example/app 8"},
		{"/api/rerun-failed", `{"repo":"example/app","runId":9}`, "rerun-failed example/app 9"},
		{"/api/service", `{"runner":"app-1","action":"stop"}`, "service app-1 start=false"},
		{"/api/service", `{"runner":"app-1","action":"start"}`, "service app-1 start=true"},
	}
	for _, st := range steps {
		status, out := post(t, srv.URL+st.path, st.body, nil)
		if status != http.StatusOK || out["ok"] != true {
			t.Errorf("%s: %d %v", st.path, status, out)
		}
		if got := s.calls[len(s.calls)-1]; got != st.want {
			t.Errorf("%s: core got %q, want %q", st.path, got, st.want)
		}
	}
	if _, out := post(t, srv.URL+"/api/cancel", `{"repo":"example/app","runId":7}`, nil); out["jobs"] != float64(3) {
		t.Errorf("cancel must say how many jobs it stopped: %v", out)
	}
	n := len(s.calls)
	if status, out := post(t, srv.URL+"/api/service", `{"runner":"app-1","action":"restart"}`, nil); status != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "start or stop") {
		t.Errorf("unknown service action: %d %v", status, out)
	}
	if status, _ := post(t, srv.URL+"/api/cancel", `{not json`, nil); status != http.StatusBadRequest {
		t.Errorf("bad body: %d", status)
	}
	if len(s.calls) != n {
		t.Errorf("bad requests reached the core: %v", s.calls[n:])
	}

	_, body := get(t, srv.URL+"/api/log?repo=example/app&job=8&from=4")
	if !strings.Contains(body, `"text":"hello"`) || !strings.Contains(body, `"next":5`) || s.calls[len(s.calls)-1] != "log example/app 8 from 4" {
		t.Errorf("log = %s; core got %q", body, s.calls[len(s.calls)-1])
	}
	if resp, _ := get(t, srv.URL+"/api/log?job=8"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("log without a repo: %d", resp.StatusCode)
	}
}

func TestErrorsKeepTheirWordsAndGetAStatus(t *testing.T) {
	s, srv := newServer(t)
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: run 7 has no unfinished jobs here", core.ErrNotAllowed), http.StatusConflict},
		{fmt.Errorf("cancel run 7: %w", &github.StatusError{Status: 403, Message: "Resource not accessible"}), http.StatusBadGateway},
		{errors.New("disk full"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		s.err = c.err
		status, out := post(t, srv.URL+"/api/cancel", `{"repo":"example/app","runId":7}`, nil)
		if status != c.want || out["error"] != c.err.Error() {
			t.Errorf("%v: got %d %v, want %d with the same words", c.err, status, out, c.want)
		}
	}
	s.err = core.ErrUnknownJob
	if resp, _ := get(t, srv.URL+"/api/log?repo=example/app&job=1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown job: %d", resp.StatusCode)
	}
}

func TestOpenOnlyOpensThisDashboard(t *testing.T) {
	s, srv := newServer(t)
	for link, want := range map[string]int{"": 200, "#queue": 200, "#job=example/app:8": 200, "https://evil.example": 400, "//evil.example": 400, "/x": 400} {
		body, _ := json.Marshal(map[string]string{"link": link})
		if status, _ := post(t, srv.URL+"/api/open", string(body), nil); status != want {
			t.Errorf("open %q: %d, want %d", link, status, want)
		}
	}
	if len(s.opened) != 3 {
		t.Errorf("opened = %v", s.opened)
	}
	headless := httptest.NewServer(New(s, "v", fstest.MapFS{}, nil))
	defer headless.Close()
	if status, _ := post(t, headless.URL+"/api/open", `{"link":"#queue"}`, nil); status != http.StatusNotImplemented {
		t.Errorf("no tray: %d, want 501 so the page falls back to a normal link", status)
	}
	// The wiring always passes a function; before the tray exists it answers ErrNoTray.
	notYet := httptest.NewServer(New(s, "v", fstest.MapFS{}, func(string) error { return ErrNoTray }))
	defer notYet.Close()
	if status, _ := post(t, notYet.URL+"/api/open", `{"link":""}`, nil); status != http.StatusNotImplemented {
		t.Errorf("tray not there yet: %d, want 501", status)
	}
	broken := httptest.NewServer(New(s, "v", fstest.MapFS{}, func(string) error { return errors.New("no browser") }))
	defer broken.Close()
	if status, out := post(t, broken.URL+"/api/open", `{"link":""}`, nil); status != http.StatusInternalServerError || out["error"] != "no browser" {
		t.Errorf("a browser that will not open: %d %v", status, out)
	}
}

func TestEventsStreamTheStateOnChange(t *testing.T) {
	s, srv := newServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test reading a response
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	next := func() int {
		t.Helper()
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var st struct {
					Summary struct {
						Busy int `json:"busy"`
					} `json:"summary"`
				}
				if err := json.Unmarshal([]byte(data), &st); err != nil {
					t.Fatalf("event data %q: %v", data, err)
				}
				return st.Summary.Busy
			}
		}
		t.Fatalf("stream ended: %v", sc.Err())
		return 0
	}
	if got := next(); got != 6 {
		t.Fatalf("first event busy = %d, want the current state at once", got)
	}
	s.change(7)
	s.change(8) // two quick changes: one push, carrying the latest
	if got := next(); got != 8 {
		t.Fatalf("after changes busy = %d, want 8", got)
	}
}
