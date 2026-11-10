package tray

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
	"time"

	"runwatch/internal/domain"
)

var t0 = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

func texts(rows []Row) string {
	var out []string
	for _, r := range rows {
		if r.Kind == Separator {
			out = append(out, "---")
		} else {
			out = append(out, r.Text)
		}
	}
	return strings.Join(out, "\n")
}

func TestMenuOnAQuietDay(t *testing.T) {
	rows := Menu(domain.Snapshot{At: t0, Summary: domain.Summary{Total: 9}}, UI{})
	want := "0 of 9 runners busy\nNothing waiting\n---\nOpen dashboard\nStart at login\nOpen log\nQuit Runwatch"
	if got := texts(rows); got != want {
		t.Errorf("menu =\n%s\nwant\n%s", got, want)
	}
	if rows[0].Kind != Label || rows[1].Kind != Label {
		t.Error("the numbers are labels, not clickable rows")
	}
}

func TestMenuOnABusyDay(t *testing.T) {
	snap := domain.Snapshot{
		At:      t0,
		Summary: domain.Summary{Total: 9, Busy: 2, Waiting: 41, OldestWait: 58 * 60, FailedHour: 2},
		Problems: []domain.Problem{{Source: "github example/app",
			Text: "list queued runs: github: HTTP 502: " + strings.Repeat("upstream connect error ", 10)}},
		Active: []domain.Job{
			{ID: 12, Repo: "example/app", Name: "unit (engine)", Status: domain.StatusInProgress, Runner: "app-2", StartedAt: t0.Add(-4 * time.Minute)},
			{ID: 11, Repo: "example/app", Name: "core", Status: domain.StatusInProgress, Runner: "app-1", StartedAt: t0.Add(-70 * time.Second)},
			{ID: 13, Repo: "example/app", Name: "lint", Status: domain.StatusQueued},
		},
	}
	rows := Menu(snap, UI{Demo: true, Autostart: true})
	got := texts(rows)
	for _, want := range []string{
		"DEMO: made-up runners, nothing real\n2 of 9 runners busy\n41 jobs waiting · oldest 58m\n2 failed in the last hour\n⚠ Not current · github example/app: list queued runs",
		"---\napp-1 · core · 1m\napp-2 · unit (engine) · 4m\n---\nOpen dashboard",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("menu lacks\n%s\n\ngot\n%s", want, got)
		}
	}
	if strings.Contains(got, "lint") {
		t.Error("waiting jobs are not listed one by one")
	}
	byText := map[string]Row{}
	for _, r := range rows {
		byText[r.Text] = r
		if len([]rune(r.Text)) > maxText {
			t.Errorf("row too long for a menu (%d): %q", len([]rune(r.Text)), r.Text)
		}
	}
	checks := []struct {
		text   string
		action Action
		arg    string
	}{
		{"41 jobs waiting · oldest 58m", ActOpen, "#queue"},
		{"2 failed in the last hour", ActOpen, "#history"},
		{"app-1 · core · 1m", ActOpen, "#job=example/app:11"},
		{"Open dashboard", ActOpen, ""},
		{"Open log", ActOpenLog, ""},
		{"Quit Runwatch", ActQuit, ""},
	}
	for _, c := range checks {
		if r := byText[c.text]; r.Kind != Item || r.Action != c.action || r.Arg != c.arg {
			t.Errorf("%q = %+v", c.text, r)
		}
	}
	if r := byText["Start at login"]; r.Kind != Check || !r.Checked || r.Action != ActAutostart {
		t.Errorf("autostart row = %+v", r)
	}
}

func TestMenuSaysWhyAutostartIsUnavailable(t *testing.T) {
	got := texts(Menu(domain.Snapshot{}, UI{AutostartErr: "access denied"}))
	if !strings.Contains(got, "Start at login (unavailable: access denied)") {
		t.Errorf("menu = %s", got)
	}
}

func TestIconsDifferPerLevel(t *testing.T) {
	seen := map[string]domain.Level{}
	for _, level := range []domain.Level{domain.LevelIdle, domain.LevelRunning, domain.LevelWaiting, domain.LevelFailed, domain.LevelProblem} {
		raw := IconPNG(level)
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("level %d: not a PNG: %v", level, err)
		}
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Errorf("level %d: %v", level, b)
		}
		if other, dup := seen[string(raw)]; dup {
			t.Errorf("levels %d and %d draw the same icon", other, level)
		}
		seen[string(raw)] = level
	}
}
