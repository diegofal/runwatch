// Package shell is the tray app's Wails glue: the icon, the flyout window
// (left click) and the menu (right click). It draws what internal/tray
// renders and holds no logic of its own; it is tested by running the app.
package shell

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"runwatch/internal/core"
	"runwatch/internal/domain"
	"runwatch/internal/tray"
)

// redrawEvery is the least time between two redraws of the icon and menu,
// and how often the menu's elapsed times are refreshed when nothing changes.
const (
	redrawEvery = time.Second
	refresh     = 15 * time.Second
	// menuHold is how long the menu is left alone after a right click:
	// rebuilding it while it is open closes it under the user's cursor.
	menuHold = 20 * time.Second
)

// Options are what the wiring gives the tray.
type Options struct {
	Watcher *core.Watcher
	URL     string // the dashboard
	LogPath string
	Logf    func(format string, args ...any)
	Version string
	Demo    bool
	// SetOpener hands the shell's "open the dashboard at this fragment in
	// the browser" to the web server, for the flyout's links.
	SetOpener func(func(link string) error)
}

type shell struct {
	o     Options
	app   *application.App
	tray  *application.SystemTray
	menu  *application.Menu
	mu    sync.Mutex // one draw at a time
	level domain.Level
	drawn bool
	rows  []tray.Row // the menu as last built
	// menuOpened is when the menu was last opened (Unix nanoseconds).
	menuOpened atomic.Int64
}

// Run starts the tray app and blocks until the user quits or ctx ends.
func Run(ctx context.Context, o Options) error {
	s := &shell{o: o}
	s.app = application.New(application.Options{
		Name:        "Runwatch",
		Description: "CI runners on this PC · " + o.Version,
		Icon:        tray.IconPNG(domain.LevelRunning),
		Mac:         application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
	})
	s.menu = s.app.NewMenu()
	s.tray = s.app.SystemTray.New()
	s.tray.SetIcon(tray.IconPNG(domain.LevelIdle))
	s.tray.SetMenu(s.menu)

	flyout := s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "flyout",
		Title:            "Runwatch",
		URL:              o.URL + "/flyout.html",
		Width:            400,
		Height:           600,
		Frameless:        true,
		AlwaysOnTop:      true,
		Hidden:           true,
		DisableResize:    true,
		HideOnEscape:     true,
		HideOnFocusLost:  true,
		BackgroundColour: application.RGBA{Red: 0x14, Green: 0x17, Blue: 0x1a, Alpha: 0xff},
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
	})
	flyout.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		flyout.Hide()
		e.Cancel()
	})
	if o.SetOpener != nil {
		o.SetOpener(func(link string) error {
			flyout.Hide()
			return s.app.Browser.OpenURL(o.URL + "/" + link)
		})
	}
	// Left click toggles the flyout; right click opens the menu.
	s.tray.AttachWindow(flyout).WindowOffset(6)
	s.tray.OnRightClick(func() {
		s.menuOpened.Store(time.Now().UnixNano())
		s.tray.OpenMenu()
	})

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go s.loop(loopCtx)
	})
	stop := context.AfterFunc(ctx, s.app.Quit)
	defer stop()
	if err := s.app.Run(); err != nil {
		return fmt.Errorf("tray: %w", err)
	}
	return nil
}

// loop redraws after each change of the Snapshot, and now and then so the
// elapsed times in the menu move.
func (s *shell) loop(ctx context.Context) {
	changed, stop := s.o.Watcher.Subscribe()
	defer stop()
	s.draw()
	tick := time.NewTicker(refresh)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-tick.C:
		}
		s.draw()
		select {
		case <-ctx.Done():
			return
		case <-time.After(redrawEvery):
		}
	}
}

func (s *shell) draw() {
	snap := s.o.Watcher.Snapshot()
	ui := tray.UI{Demo: s.o.Demo}
	if on, err := s.app.Autostart.IsEnabled(); err != nil {
		ui.AutostartErr = err.Error()
	} else {
		ui.Autostart = on
	}
	rows := tray.Menu(snap, ui)
	level := domain.TrayLevel(snap)

	s.mu.Lock()
	defer s.mu.Unlock()
	// The Snapshot changes every few seconds on a busy day; the menu is
	// rebuilt only when its rows did, and not while it may be open.
	held := time.Since(time.Unix(0, s.menuOpened.Load())) < menuHold
	if !slices.Equal(rows, s.rows) && !held {
		s.rows = rows
		application.InvokeSync(func() {
			s.menu.Clear()
			for _, r := range rows {
				s.add(r)
			}
			s.menu.Update()
		})
	}
	if !s.drawn || level != s.level {
		s.tray.SetIcon(tray.IconPNG(level))
		s.level, s.drawn = level, true
	}
	s.tray.SetTooltip(domain.Tooltip(snap))
}

func (s *shell) add(r tray.Row) {
	var item *application.MenuItem
	switch r.Kind {
	case tray.Separator:
		s.menu.AddSeparator()
		return
	case tray.Check:
		item = s.menu.AddCheckbox(r.Text, r.Checked)
	default:
		item = s.menu.Add(r.Text)
	}
	item.SetEnabled(r.Kind != tray.Label)
	if r.Kind != tray.Label {
		item.OnClick(func(*application.Context) { go s.click(r) })
	}
}

func (s *shell) click(r tray.Row) {
	var err error
	switch r.Action {
	case tray.ActOpen:
		err = s.app.Browser.OpenURL(s.o.URL + "/" + r.Arg)
	case tray.ActOpenLog:
		err = s.app.Browser.OpenFile(s.o.LogPath)
	case tray.ActAutostart:
		if r.Checked {
			err = s.app.Autostart.Disable()
		} else {
			err = s.app.Autostart.Enable()
		}
		s.menuOpened.Store(0) // the click closed the menu: show the new tick now
		s.draw()
	case tray.ActQuit:
		s.app.Quit()
	}
	if err != nil {
		s.o.Logf("tray: %s: %v", r.Text, err)
	}
}
