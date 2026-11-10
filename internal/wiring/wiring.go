// Package wiring builds the app from its parts: config, the machine reader,
// the GitHub client, the history file, the core and the local web server.
// Both ways of running (tray and --headless) and --demo start here.
package wiring

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"runwatch/internal/adapters/execx"
	"runwatch/internal/adapters/github"
	"runwatch/internal/adapters/history"
	"runwatch/internal/adapters/machine"
	"runwatch/internal/adapters/runnerfs"
	"runwatch/internal/config"
	"runwatch/internal/core"
	"runwatch/internal/fakegh"
	"runwatch/internal/server"
	"runwatch/web"
)

// sizesEvery is how often the runners' folders are measured.
const sizesEvery = 10 * time.Minute

// maxLogFile is the size at which runwatch.log starts over.
const maxLogFile = 5 << 20

// App is the running app without a front end.
type App struct {
	Watcher *core.Watcher
	URL     string // the dashboard, e.g. http://127.0.0.1:7878
	LogPath string
	Logf    func(format string, args ...any)
	Demo    bool

	opener  atomic.Pointer[func(link string) error]
	srv     *http.Server
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	cleanup []func()
}

// Options choose how the app is built.
type Options struct {
	Version  string
	Demo     bool          // run on the built-in fake GitHub and fake runners
	Notifier core.Notifier // nil = none
}

// Start loads the config, starts watching and serving, and returns at once.
func Start(ctx context.Context, o Options) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	a := &App{Demo: o.Demo, LogPath: filepath.Join(cfg.DataDir, "runwatch.log")}
	logger, closeLog, err := openLog(a.LogPath)
	if err != nil {
		return nil, err
	}
	a.cleanup = append(a.cleanup, closeLog)
	a.Logf = logger.Printf
	ok := false
	defer func() {
		if !ok {
			a.Close()
		}
	}()

	ctx, a.cancel = context.WithCancel(ctx)
	historyPath := filepath.Join(cfg.DataDir, "history.jsonl")
	if o.Demo {
		if historyPath, err = a.startDemo(ctx, &cfg); err != nil {
			return nil, err
		}
	}
	store, err := history.Open(historyPath)
	if err != nil {
		return nil, err
	}
	var gh github.GHRunner
	if cfg.Token == "" {
		gh = func(ctx context.Context, args ...string) ([]byte, error) {
			return execx.Run(ctx, "", nil, "gh", args...)
		}
	}
	tokens := github.NewTokens(cfg.Token, gh, cfg.GitHubAPI, nil)
	mach := machine.New(cfg.Roots)
	if o.Demo || !cfg.Services {
		// The demo and the tests never look at or touch real services.
		mach.Services, mach.ControlFn = nil, nil
	}
	if !cfg.Notifications {
		o.Notifier = nil
	}
	a.Watcher = core.New(core.Options{
		GitHub:  github.New(cfg.GitHubAPI, tokens, nil),
		Machine: mach,
		History: store,
		NewTail: func(dir, plan string, since time.Time) core.LogTail { return runnerfs.NewTail(dir, plan, since) },
		Logf:    a.Logf, Managed: cfg.Managed, Notifier: o.Notifier,
		PollActive: cfg.Poll, PollIdle: cfg.Poll,
	})

	if err := a.serve(cfg, o.Version); err != nil {
		return nil, err
	}
	a.wg.Go(func() { a.Watcher.Run(ctx) })
	a.wg.Go(func() { a.measure(ctx) })
	a.Logf("runwatch %s started; dashboard %s; data %s; demo=%v", o.Version, a.URL, cfg.DataDir, o.Demo)
	ok = true
	return a, nil
}

// serve starts the dashboard's web server on cfg.Addr, loopback only.
func (a *App) serve(cfg config.Config, version string) error {
	host, _, err := net.SplitHostPort(cfg.Addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("addr %q must be a loopback address like 127.0.0.1:7878", cfg.Addr)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s (is Runwatch already running?): %w", cfg.Addr, err)
	}
	a.URL = "http://" + ln.Addr().String()
	// url.txt tells scripts and tests where a copy started on port 0 listens.
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "url.txt"), []byte(a.URL+"\n"), 0o600); err != nil {
		ln.Close() //nolint:errcheck // the write error is the one reported
		return fmt.Errorf("write url.txt: %w", err)
	}
	a.srv = &http.Server{Handler: server.New(a.Watcher, version, web.Files, a.open), ReadHeaderTimeout: 10 * time.Second}
	a.wg.Go(func() {
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.Logf("web server: %v", err)
		}
	})
	return nil
}

// startDemo starts the fake GitHub and its runners in a temp folder and
// points cfg at them. It returns the demo's own history file, so a demo
// never mixes into real history.
func (a *App) startDemo(ctx context.Context, cfg *config.Config) (string, error) {
	dir, err := os.MkdirTemp("", "runwatch-demo-")
	if err != nil {
		return "", fmt.Errorf("demo: %w", err)
	}
	a.cleanup = append(a.cleanup, func() { _ = os.RemoveAll(dir) }) // a temp folder: best effort
	world := fakegh.New(filepath.Join(dir, "runners"))
	demo, err := fakegh.NewDemo(world, 42)
	if err != nil {
		return "", fmt.Errorf("demo: %w", err)
	}
	api := httptest.NewServer(world.Handler())
	a.cleanup = append(a.cleanup, api.Close)
	a.wg.Go(func() {
		if err := demo.Drive(ctx); err != nil {
			a.Logf("demo: %v", err)
		}
	})
	cfg.Roots, cfg.GitHubAPI, cfg.Token = []string{filepath.Join(dir, "runners")}, api.URL, "demo-token"
	cfg.Managed = map[string]string{"billing-*": "Managed by scripts/ci-runners.cmd in the billing repo (demo)."}
	return filepath.Join(dir, "history.jsonl"), nil
}

// SetOpener gives the server the tray's way to open the dashboard in the
// user's browser. Until it is set, /api/open answers "not implemented".
func (a *App) SetOpener(fn func(link string) error) { a.opener.Store(&fn) }

func (a *App) open(link string) error {
	fn := a.opener.Load()
	if fn == nil {
		return server.ErrNoTray
	}
	return (*fn)(link)
}

// measure records each runner's folder sizes now and every sizesEvery.
func (a *App) measure(ctx context.Context) {
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, r := range a.Watcher.Snapshot().Runners {
			if ctx.Err() != nil {
				return
			}
			diag, work, err := runnerfs.Sizes(r.Dir)
			if err != nil {
				a.Logf("measure %s: %v", r.Name, err)
			}
			a.Watcher.SetSizes(r.Name, diag, work)
		}
		t.Reset(sizesEvery)
	}
}

// Close stops the server and the watcher and waits for them.
func (a *App) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = a.srv.Shutdown(ctx) // shutting down: open streams are cut by Close below
		cancel()
		_ = a.srv.Close() // already shutting down
	}
	a.wg.Wait()
	for i := len(a.cleanup) - 1; i >= 0; i-- {
		a.cleanup[i]()
	}
}

func openLog(path string) (*log.Logger, func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the data folder: %w", err)
	}
	flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogFile {
		flags = os.O_TRUNC | os.O_CREATE | os.O_WRONLY
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open the log: %w", err)
	}
	return log.New(f, "", log.LstdFlags), func() { _ = f.Close() }, nil // closing the log: nowhere to report
}
