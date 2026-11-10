package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// browser is a real headless Chrome on the running program's pages.
type browser struct {
	t    *testing.T
	ctx  context.Context
	mu   sync.Mutex
	errs []string
}

// openBrowser starts Chrome with a window of the given size. Without a
// Chrome on the machine the test is skipped, unless RUNWATCH_E2E_BROWSER is
// "required" (CI sets it, so a missing browser cannot hide a broken page).
func openBrowser(t *testing.T, width, height int) *browser {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(width, height))
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	ctx, cancelTime := context.WithTimeout(ctx, 2*time.Minute)
	t.Cleanup(func() { cancelTime(); cancelCtx(); cancelAlloc() })
	b := &browser{t: t, ctx: ctx}
	chromedp.ListenTarget(ctx, func(ev any) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			b.errs = append(b.errs, "exception: "+e.ExceptionDetails.Error())
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.APITypeError {
				b.errs = append(b.errs, "console.error: "+string(e.Args[0].Value))
			}
		}
	})
	if err := chromedp.Run(ctx); err != nil {
		if os.Getenv("RUNWATCH_E2E_BROWSER") == "required" {
			t.Fatalf("Chrome is required for this test: %v", err)
		}
		t.Skipf("no Chrome to run the browser tests with: %v", err)
	}
	return b
}

func (b *browser) run(what string, actions ...chromedp.Action) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		var page string
		_ = chromedp.Run(b.ctx, chromedp.Text("body", &page, chromedp.ByQuery))
		b.t.Fatalf("%s: %v\npage text:\n%s", what, err, page)
	}
}

func hook(id string) string { return fmt.Sprintf(`[data-testid="%s"]`, id) }

// text waits until the element's text contains want.
func (b *browser) text(sel, want string) {
	b.t.Helper()
	js := fmt.Sprintf(`(() => { const e = document.querySelector(%q); return !!e && e.innerText.includes(%q); })()`, sel, want)
	b.run(fmt.Sprintf("waiting for %q in %s", want, sel), chromedp.Poll(js, nil, chromedp.WithPollingInterval(100*time.Millisecond)))
}

// gone waits until nothing matches sel.
func (b *browser) gone(sel string) {
	b.t.Helper()
	js := fmt.Sprintf(`document.querySelector(%q) === null`, sel)
	b.run("waiting for "+sel+" to go", chromedp.Poll(js, nil, chromedp.WithPollingInterval(100*time.Millisecond)))
}

func (b *browser) click(sel string) {
	b.t.Helper()
	b.run("click "+sel, chromedp.WaitVisible(sel, chromedp.ByQuery), chromedp.Click(sel, chromedp.ByQuery))
}

// clean fails the test if the page threw, logged an error, or showed its
// own "UI error" bar.
func (b *browser) clean() {
	b.t.Helper()
	var shown bool
	b.run("read the UI error bar", chromedp.Evaluate(`(() => { const e = document.querySelector('#errors'); return !!e && !e.hidden; })()`, &shown))
	b.mu.Lock()
	defer b.mu.Unlock()
	if shown || len(b.errs) > 0 {
		b.t.Errorf("the page reported errors (UI error bar shown: %v): %v", shown, b.errs)
	}
}

func (a *app) posted(path string) {
	a.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !hasPost(a.world.Posts(), path) {
		if time.Now().After(deadline) {
			a.t.Fatalf("GitHub was never asked %s; it got %v", path, a.world.Posts())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestTheDashboardInABrowser does a user's session on the dashboard with a
// real Chrome: read the numbers, follow a running job's terminal, stop a
// run from the queue, re-run from history, see an outage, open a deep link.
func TestTheDashboardInABrowser(t *testing.T) {
	a := start(t)
	run := a.world.Push("example/app", "feature/ledger-rows", "unit", "lint", "e2e")
	unit := run.Jobs[0].ID
	a.must(a.world.Start(unit, "app-1", "Set up job", "Run tests", "Complete job"))
	a.must(a.world.Output(unit, "##[group]Run tests", "  ok lease floor fits a core job"))
	a.until("the job running", func(s state) bool { return s.Summary.Busy == 1 && s.Summary.Waiting == 2 })

	b := openBrowser(t, 1600, 1000)
	b.run("open the dashboard", chromedp.Navigate(a.url+"/"))
	b.text(hook("fact-running"), "1")
	b.text(hook("fact-waiting"), "2")
	b.text(`[data-testid="lane"][data-runner="app-1"]`, "unit")

	// The running job: steps and a terminal that grows while it runs.
	b.click(`[data-testid="lane"][data-runner="app-1"] ` + hook("lane-current"))
	b.text(hook("job-status"), "in progress")
	b.text(hook("steps"), "Run tests")
	b.text(hook("terminal"), "ok lease floor fits a core job")
	b.text(hook("log-source"), "live")
	a.must(a.world.Output(unit, "  ok printed while the drawer was open"))
	b.text(hook("terminal"), "ok printed while the drawer was open")
	b.click(hook("drawer-close"))
	b.gone(hook("drawer"))

	// Queue: stopping asks first, inside the row, and says how many jobs it stops.
	b.click(hook("nav-queue"))
	row := fmt.Sprintf(`[data-testid="queue-run"][data-run="%d"] `, run.ID)
	b.click(row + hook("stop-run"))
	b.text(row, "3")
	b.click(row + hook("cancel-stop"))
	if posts := a.world.Posts(); len(posts) != 0 {
		t.Fatalf("\"Keep\" must not stop anything, but GitHub got %v", posts)
	}
	b.click(row + hook("stop-run"))
	b.click(row + hook("confirm-stop"))
	a.posted(fmt.Sprintf("/repos/example/app/actions/runs/%d/cancel", run.ID))
	b.text(hook("fact-waiting"), "0")
	b.gone(hook("queue-run"))

	// History: the stopped jobs are there; re-run one.
	b.click(hook("nav-history"))
	hist := fmt.Sprintf(`[data-testid="history-job"][data-job="%d"] `, unit)
	b.text(hist, "cancelled")
	b.click(hist + hook("rerun-job"))
	a.posted(fmt.Sprintf("/repos/example/app/actions/jobs/%d/rerun", unit))
	b.text(hook("fact-waiting"), "1")
	// GitHub refuses a second re-run while the run is live; its words are shown, not swallowed.
	b.click(fmt.Sprintf(`[data-testid="history-job"][data-job="%d"] `, run.Jobs[1].ID) + hook("rerun-job"))
	b.text(hook("action-error"), "already running")

	// A deep link (what a notification opens) shows a finished job's whole log.
	b.run("open a job by link", chromedp.Navigate(fmt.Sprintf("%s/#job=example/app:%d", a.url, unit)))
	b.text(hook("job-status"), "cancelled")
	b.text(hook("terminal"), "ok printed while the drawer was open")
	b.text(hook("log-source"), "GitHub")

	// GitHub goes down: the page says so, in red, and recovers by itself.
	a.world.FailNext("/repos/example/app/", 503)
	b.text(hook("problems"), "github example/app")
	b.text(hook("problems"), "503")
	a.world.Heal()
	b.gone(hook("problems"))

	// Runners: the one managed elsewhere has no start/stop, and says why.
	b.run("open the runners tab", chromedp.Navigate(a.url+"/#runners"))
	b.text(`[data-testid="runner-card"][data-runner="billing-1"] `+hook("managed"), "billing repo's script")
	b.gone(`[data-testid="runner-card"][data-runner="billing-1"] ` + hook("service-stop"))
	b.clean()

	// The program stops: the page must say its numbers are no longer current.
	a.stop()
	b.text(hook("disconnected"), "not current")
}

// TestTheFlyoutInABrowser drives the tray's flyout page at the size of its
// window: numbers, a running job's mini terminal, stop with confirmation,
// and re-running a failed job.
func TestTheFlyoutInABrowser(t *testing.T) {
	a := start(t)
	failed := a.world.Push("example/app", "fix/lease-floor", "lint")
	a.until("the first run", func(s state) bool { return s.Summary.Waiting == 1 })
	a.must(a.world.Start(failed.Jobs[0].ID, "app-2"))
	a.until("lint running", func(s state) bool { return s.Summary.Busy == 1 })
	a.must(a.world.Finish(failed.Jobs[0].ID, "failure"))
	run := a.world.Push("example/app", "feature/ledger-rows", "unit", "e2e")
	unit := run.Jobs[0].ID
	a.must(a.world.Start(unit, "app-1", "Set up job", "Run tests"))
	a.must(a.world.Output(unit, "  ok shown in the tray"))
	a.until("the day", func(s state) bool { return s.Summary.Busy == 1 && s.Summary.Waiting == 1 && s.Summary.FailedHour == 1 })

	b := openBrowser(t, 400, 600)
	b.run("open the flyout", chromedp.Navigate(a.url+"/flyout.html"))
	b.text(hook("fly-running"), "1")
	b.text(hook("fly-waiting"), "1")
	b.text(`[data-testid="fly-repo"][data-repo="example/app"]`, "1 waiting")

	jobRow := fmt.Sprintf(`[data-testid="fly-job"][data-job="%d"]`, unit)
	b.text(jobRow, "unit")
	b.text(jobRow, "1/2")
	b.click(jobRow)
	b.text(jobRow+" "+hook("fly-terminal"), "ok shown in the tray")

	b.click(fmt.Sprintf(`[data-testid="fly-failed"][data-job="%d"] `, failed.Jobs[0].ID) + hook("fly-rerun"))
	a.posted(fmt.Sprintf("/repos/example/app/actions/jobs/%d/rerun", failed.Jobs[0].ID))

	b.run("hover the job to reach stop", chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).scrollIntoView()`, jobRow), nil))
	b.run("click stop", chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).click()`, jobRow+" "+hook("fly-stop")), nil))
	b.text(jobRow, "2") // the run's two unfinished jobs
	b.click(jobRow + " " + hook("fly-confirm-stop"))
	a.posted(fmt.Sprintf("/repos/example/app/actions/runs/%d/cancel", run.ID))
	b.gone(jobRow)

	// The page fits the flyout window: nothing scrolls sideways.
	var overflow bool
	b.run("measure", chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth`, &overflow))
	if overflow {
		t.Error("the flyout is wider than its 400 px window")
	}
	b.clean()
	if posts := strings.Join(a.world.Posts(), " "); strings.Count(posts, "/cancel") != 1 {
		t.Errorf("one stop was confirmed, GitHub got: %s", posts)
	}
}
