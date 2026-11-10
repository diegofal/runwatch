# Testing

```
task ci      # everything below, plus format check, vet, lint and the build
task test    # unit and scenario tests
task cover   # the same, then the coverage gates
task e2e     # end-to-end tests
```

## The rule: nothing real

No test touches a real repository, a real runner, a real token or the real data folder. Everything runs on
`internal/fakegh`:

- a **pretend GitHub**: an in-memory Actions API with runs, jobs, steps, logs, cancel and re-run, ETags and
  "304 Not Modified", bearer-token checking, and outages on demand (`FailNext`);
- **pretend runner folders**, written the way the real runner writes them: `.runner` with its byte order
  mark, `_diag\Runner_*.log` with the real line formats, `_diag\pages` files that appear while a job runs
  and are deleted when it ends.

The program under test is pointed at them with `RUNWATCH_DATA_DIR`, `RUNWATCH_ROOTS`, `RUNWATCH_GITHUB_API`
and `RUNWATCH_GITHUB_TOKEN`, and `RUNWATCH_SERVICES=off` keeps it from seeing the runner services of the
machine running the tests. All names in the fake are made up (`example/app`, `acme/billing`).

## Layers

| Tests | What they check |
|---|---|
| `internal/domain` | The rules: waited and ran times, which jobs are ours, the summary, the tray level, "usual", alerts, formatting, log-line parsing. 100 % covered. |
| `internal/adapters/runnerfs` | Reading a runner folder, with log lines in the real format; the live tail (partial lines, files deleted mid-job, a stale file from an earlier job of the same run) |
| `internal/adapters/github` | The client against the pretend GitHub: listing, paging, ETag (unchanged answers cost nothing), actions, GitHub's error text, a refused token being forgotten, picking the right `gh` account per repo |
| `internal/adapters/history`, `machine`, `internal/config` | The history file (torn last line, compaction), discovery through services and folders, config and its overrides |
| `internal/core` | Scenarios on the real Watcher with the real client, tail and history: a day's queue, live log then the whole log, an outage (problem shown, old numbers kept, then cleared), stop and re-run and their refusals, service control and "managed elsewhere", alerts sent once, restart without re-announcing old news |
| `internal/server` | Every endpoint, the status of each kind of error, the event stream, and the rules that keep other sites out |
| `internal/tray` | The menu on a quiet and a busy day, the icon per level |
| `internal` (arch test) | The import rules between packages |

Coverage gates (`scripts/covergate.go`): 90 % for `domain`, 85 % for every other gated package. Not gated:
the wiring and `cmd` (run by the end-to-end tests), the fake itself, and the thin OS adapters (`winsvc`,
`notify`, `execx`, the Wails shell), which are checked by running the app.

## End-to-end tests (`e2e/`)

They build `runwatch` from the tree, start it as a separate process with `--headless` on a free port, and
use it the way a user's browser does.

| Test | What it walks through |
|---|---|
| `TestADayOfWork` | Over HTTP: runners found; a push puts jobs in the queue; a runner picks one up and its output is followed live; a request without the `X-Runwatch` header is refused and nothing reaches GitHub; stop run cancels all its jobs; the finished log comes from GitHub; re-run; GitHub's refusal comes through in its own words; a managed runner cannot be stopped; an outage appears as a problem and clears; after a restart history is still there, old jobs are not announced again, and the token is in no log |
| `TestTheStreamPushesChanges` | `/api/events` pushes a change |
| `TestASecondCopyRefusesToStart` | A second copy on the same address exits with a clear message |
| `TestTheDemoRunsOnItsOwn` | `--demo` with no token, no runner and no network shows a busy day that keeps moving, and writes no history into the data folder |
| `TestTheDashboardInABrowser` | A real headless Chrome on the dashboard: the numbers; open a running job and see its terminal grow; queue → stop → "Keep" does nothing → stop → confirm → GitHub got the cancel; history → re-run; GitHub's refusal shown on the page; a deep link to a finished job shows its whole log; an outage shows the red bar and clears; a managed runner has no stop button; when the program dies the page says its numbers are not current. Fails on any JavaScript exception, `console.error` or UI error bar. |
| `TestTheFlyoutInABrowser` | Chrome at 400×600 on the flyout: the figures, the repo row, a running job's step, its mini terminal, re-run a failed job, stop with confirmation, and no sideways overflow |

The browser tests need Chrome or Edge. Without one they are skipped, unless `RUNWATCH_E2E_BROWSER=required`
is set (CI sets it, so a missing browser cannot hide a broken page). The pages carry `data-testid`
attributes; the tests select by those, not by styling.

## What is checked by hand

The parts no automated test reaches, checked on a real PC before a release:

| Check | How |
|---|---|
| Tray icon, flyout window, right-click menu | `task demo`, left and right click the icon |
| Real runners and real GitHub, read-only | Start the app on a PC with runners: the Runners tab lists them, a busy runner shows its job, the terminal shows a running job's output and a finished job's log |
| Notifications | In the demo a job fails every few minutes: a toast appears; clicking it opens the job |
| Service start and stop | Runners tab, on a runner that is safe to stop; Windows asks for admin |
| Start at login | Tick it in the menu, sign out and in |

## Running the app on test data yourself

```
task demo                      # tray app on the demo
bin\runwatch.exe --demo --headless
```

To stop a test copy, stop that exact process (its PID), never "all runwatch processes": a real copy may be
running beside it.
