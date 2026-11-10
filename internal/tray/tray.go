// Package tray renders the Snapshot into what the tray shows: the icon, the
// tooltip and the right-click menu's rows. Pure functions; the Wails glue
// that draws them is internal/tray/shell.
package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sort"
	"time"

	"runwatch/internal/domain"
)

// Kind is what a menu row is.
type Kind int

const (
	Label Kind = iota // text only, not clickable
	Item
	Check
	Separator
)

// Action is what clicking a row does.
type Action int

const (
	ActNone Action = iota
	ActOpen        // open the dashboard at Arg (a fragment like "#queue")
	ActAutostart
	ActOpenLog
	ActQuit
)

// Row is one line of the menu.
type Row struct {
	Kind    Kind
	Text    string
	Action  Action
	Arg     string
	Checked bool
}

// UI is the state the menu shows that is not in the Snapshot.
type UI struct {
	Autostart    bool
	AutostartErr string
	Demo         bool
}

// maxText keeps a menu row on one line.
const maxText = 90

func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxText {
		return s
	}
	return string(r[:maxText-1]) + "…"
}

// Menu builds the right-click menu: the numbers, every failing read, one
// row per running job (opens it in the dashboard), then the app's own rows.
func Menu(s domain.Snapshot, ui UI) []Row {
	sum := s.Summary
	var rows []Row
	if ui.Demo {
		rows = append(rows, Row{Kind: Label, Text: "DEMO: made-up runners, nothing real"})
	}
	rows = append(rows, Row{Kind: Label, Text: fmt.Sprintf("%d of %d runners busy", sum.Busy, sum.Total)})
	if sum.Waiting > 0 {
		rows = append(rows, Row{Kind: Item, Action: ActOpen, Arg: "#queue",
			Text: fmt.Sprintf("%d jobs waiting · oldest %s", sum.Waiting, domain.ShortDur(time.Duration(sum.OldestWait)*time.Second))})
	} else {
		rows = append(rows, Row{Kind: Label, Text: "Nothing waiting"})
	}
	if sum.FailedHour > 0 {
		rows = append(rows, Row{Kind: Item, Action: ActOpen, Arg: "#history", Text: fmt.Sprintf("%d failed in the last hour", sum.FailedHour)})
	}
	for _, p := range s.Problems {
		rows = append(rows, Row{Kind: Label, Text: clip("⚠ Not current · " + p.Source + ": " + p.Text)})
	}

	var running []domain.Job
	for _, j := range s.Active {
		if j.Status == domain.StatusInProgress {
			running = append(running, j)
		}
	}
	sort.SliceStable(running, func(a, b int) bool { return running[a].Runner < running[b].Runner })
	if len(running) > 0 {
		rows = append(rows, Row{Kind: Separator})
	}
	for _, j := range running {
		rows = append(rows, Row{Kind: Item, Action: ActOpen, Arg: fmt.Sprintf("#job=%s:%d", j.Repo, j.ID),
			Text: clip(fmt.Sprintf("%s · %s · %s", j.Runner, j.Name, domain.ShortDur(j.Ran(s.At))))})
	}

	auto := "Start at login"
	if ui.AutostartErr != "" {
		auto = clip("Start at login (unavailable: " + ui.AutostartErr + ")")
	}
	return append(rows,
		Row{Kind: Separator},
		Row{Kind: Item, Text: "Open dashboard", Action: ActOpen},
		Row{Kind: Check, Text: auto, Action: ActAutostart, Checked: ui.Autostart},
		Row{Kind: Item, Text: "Open log", Action: ActOpenLog},
		Row{Kind: Item, Text: "Quit Runwatch", Action: ActQuit},
	)
}

var levelColor = map[domain.Level]color.RGBA{
	domain.LevelIdle:    {0x8b, 0x93, 0x9b, 0xff},
	domain.LevelRunning: {0x5a, 0xd1, 0xff, 0xff},
	domain.LevelWaiting: {0xff, 0xb3, 0x47, 0xff},
	domain.LevelFailed:  {0x5a, 0xd1, 0xff, 0xff},
	domain.LevelProblem: {0x8b, 0x93, 0x9b, 0xff},
}

var red = color.RGBA{0xff, 0x5a, 0x4e, 0xff}

// size is the icon's side in pixels; Windows scales it down.
const size = 64

// IconPNG draws the tray icon: three bars in the level's colour, with a red
// dot when a job failed and a red bar underneath when a read is failing.
func IconPNG(level domain.Level) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	c := levelColor[level]
	bar := func(x, top int) {
		for y := top; y < 54; y++ {
			for dx := range 12 {
				img.SetRGBA(x+dx, y, c)
			}
		}
	}
	bar(6, 10)
	bar(26, 22)
	bar(46, 34)
	switch level {
	case domain.LevelFailed:
		disc(img, 50, 14, 12, red)
	case domain.LevelProblem:
		for y := 56; y < 64; y++ {
			for x := 4; x < 60; x++ {
				img.SetRGBA(x, y, red)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("tray: encode icon: " + err.Error()) // an in-memory RGBA image always encodes
	}
	return buf.Bytes()
}

func disc(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r && image.Pt(x, y).In(img.Rect) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}
