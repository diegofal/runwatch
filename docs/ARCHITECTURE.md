# How Runwatch works

One Go program. It reads two sources, joins them into one snapshot, and two front ends draw that snapshot.

```
 this PC                                 GitHub
 ───────                                 ──────
 Windows services  actions.runner.*      Actions REST API
 <runner>\.runner                        runs, jobs, steps, logs
 <runner>\_diag\Runner_*.log             cancel / re-run
 <runner>\_diag\pages\*.log
        │                                      │
        ▼                                      ▼
 internal/adapters/machine,            internal/adapters/github
 runnerfs, winsvc                      (ETag cache, token per repo)
        └──────────────┬───────────────────────┘
                       ▼
                 internal/core            the Watcher: one Snapshot, the actions,
                       │                  the live log buffers, the alerts
        ┌──────────────┴──────────────┐
        ▼                             ▼
 internal/server                internal/tray (+ shell)
 127.0.0.1: JSON API,           icon, tooltip, right-click menu,
 dashboard + flyout pages       flyout window showing /flyout.html
```

## Where each fact comes from

| Fact | Source | Why that source |
|---|---|---|
| Which runners exist | Registry keys of the services `actions.runner.*` (their `ImagePath`), plus the folders in `roots` | Readable without admin |
| Which repo a runner serves, its name | `<runner>\.runner` (`gitHubUrl`, `agentName`) | The runner's own registration |
| Service running or stopped | Service manager, query-only access | No admin needed to read |
| Runner busy, with which job, since when | The newest `_diag\Runner_*.log`: the last `Running job: X` line with no `Job X completed with result:` after it | Known within 2 s, before GitHub's API shows it |
| Output of a running job | `_diag\pages\<plan>_<record>_<n>.log`, polled every 300 ms | The only local source; see the limits below |
| A runner's labels | `GET /repos/{repo}/actions/runners`, by runner name, hourly | Needed to tell which waiting jobs are for these runners |
| Queue, steps, branch, who pushed | `GET /repos/{repo}/actions/runs?status=queued` and `in_progress`, then `/runs/{id}/jobs` | Waiting jobs never reach the PC |
| Output of a finished job | `GET /repos/{repo}/actions/jobs/{id}/logs` | Complete, unlike the local blocks |
| "Usual" time | Median run time of the newest 20 successful jobs of the same repo and name in `history.jsonl` | A fact about the past, not an estimate |
| Folder sizes | A walk of `_diag` and `_work`, every 10 minutes | Slow on big checkouts, so done rarely |
| CPU and memory of the PC | `GetSystemTimes` and `GlobalMemoryStatusEx`, every 500 ms; shown as an average over the last 10 s | Exact, no admin |
| CPU and memory of a runner | The process list (`NtQuerySystemInformation`), every 500 ms: the service's process (its PID comes from the service manager) and everything started under it | One call lists every process with its parent and processor time, without admin and without opening a process; see the limits below |

Three details of the GitHub API that shape the code:

- A run's status is `queued` while **any** of its jobs waits, even when others are running. So both the
  `queued` and the `in_progress` lists are read and merged.
- For a waiting job, `started_at` equals `created_at`. A job counts as started only when it has both a
  runner name and a start time (`domain.Job.Started`). The wait is "now − created" while queued.
- There is no endpoint to cancel one job. "Stop" is `POST /runs/{id}/cancel`.

The join between the two sources is the runner's name: a job in progress whose `runner_name` is a runner on
this PC is that runner's job. A job that ran on some other machine's runner for the same repo is not ours
and is left out.

A **waiting** job has no runner yet, so it is matched the way GitHub matches it (`domain.Ours`): it is ours
when one of this PC's runners for that repo carries every label the job asks for, compared without regard to
case. The job's labels are its `runs-on`; a workflow that says `runs-on: my-ci` (often through a variable)
asks for that label only and never mentions `self-hosted`. The runners' labels come from
`GET /repos/{repo}/actions/runners`, matched by runner name and read once an hour, since they change only
when a runner is reconfigured. That call needs an account that administers the repo. When GitHub refuses it,
the rule falls back to the one label every self-hosted runner is known to carry (a waiting job is ours if it
asks for `self-hosted`), and a problem line says so, because jobs asking only for a custom label are then
missed. The read is tried again every ten minutes.

### CPU per runner and its limits

Windows keeps a process's processor time only while the process lives, and does not add a finished child's
time to its parent. So `domain.Meter` adds up what each process used between two readings and credits it to
the runner the process descends from. It remembers each process's runner, so a process stays with its runner
after the shell that started it has ended.

A process that starts and ends between two readings is never seen. Tests start many of those, so the readings
are 500 ms apart (one takes a few hundredths of a second of one processor) and the gap is shown instead of
hidden: `Usage.Unassigned` is the PC's busy time that no process seen in two readings accounts for (it also
holds the system's interrupt handling). The dashboard shows it as "short-lived". A runner's number is
therefore what its processes were seen to use: the true figure is that or more, never less.

Nothing is measured for a runner that is not a running service, in a test, or with `services` off. The demo
has pretend processors (`wiring/demo-usage.go`).

### The live output and its limits

The runner writes each step's output to a file in `_diag\pages`, in blocks of a few KB, uploads the file and
deletes it seconds later. `runnerfs.Tail` keeps an offset per file and the core keeps every line it has seen
(up to 50,000 per job). Consequences, all shown to the user as a note in the terminal:

- Nothing appears until the first block is written, so a quiet step looks empty for a while.
- Output from before Runwatch started is gone from the PC until the job ends.
- A file created and deleted between two polls (300 ms) would be missed.

When the job ends the terminal switches to the whole log from GitHub.

## The Snapshot

`core.Watcher` owns one `domain.Snapshot`: runners, active jobs (queued and running, oldest queued first),
recent finished jobs (newest 300), the summary numbers, the problems (reads failing right now) and the
events ("what happened"). It is rebuilt after each local scan (every 2 s), each GitHub poll and each action.
Subscribers (the server's event stream, the tray) are told after each rebuild.

**Problems are data, not log lines.** A failed read becomes a `domain.Problem` in the Snapshot with its
source, text and the time it started. The data it could not refresh stays as it was, next to the problem.
`domain.TrayLevel` puts a failing read above every other state.

## The local server

`internal/server` serves the static pages in `web/` (built into the binary) and this API:

| Request | Answer |
|---|---|
| `GET /api/state` | The Snapshot as JSON, plus `version` |
| `GET /api/events` | Server-sent events: the same JSON at connect and after each change, at most once a second |
| `GET /api/log?repo=&job=&from=` | Lines of a job's output from index `from`: `{lines, next, source, done, note}` with `source` = `live`, `github` or `none` |
| `POST /api/cancel` `{repo, runId}` | Cancels the run. `{ok, jobs}` |
| `POST /api/rerun-job` `{repo, jobId}` | Re-runs a finished job |
| `POST /api/rerun-failed` `{repo, runId}` | Re-runs a finished run's failed and cancelled jobs |
| `POST /api/service` `{runner, action}` | `start` or `stop` a runner's service (Windows asks for admin) |
| `POST /api/open` `{link}` | Asks the tray to open the dashboard at a `#fragment` in the browser (the flyout is a bare webview). 501 without a tray. |

Errors are `{ "error": "text" }` with 400 (bad request), 403 (refused by the rules below), 404 (unknown
job), 409 (not allowed: not something Runwatch watches, or a runner managed elsewhere), 502 (GitHub refused;
the text is GitHub's) or 500.

A server on localhost can be reached by any web page open in the user's browser. The rules, enforced in
`guard` and tested in `server_test.go`:

1. The `Host` header must be a loopback name. This stops DNS rebinding (a hostile name pointed at 127.0.0.1).
2. No CORS headers are sent, so a page from another origin cannot read any answer.
3. Every POST needs the header `X-Runwatch: 1`. A cross-site form cannot set a custom header, and a
   cross-site script that tries triggers a preflight this server does not answer.
4. If a POST carries an `Origin`, it must be this server.
5. The core only acts on what is in the Snapshot: a run with unfinished jobs here, a finished job it has
   seen, a runner on this PC. The API cannot be used to cancel or re-run arbitrary things with the user's token.
6. Pages get a Content-Security-Policy of `default-src 'self'`: no inline scripts, no external requests.

## The pages

`web/` is plain HTML, CSS and JavaScript, no build step. `app.js` holds the API client, the event stream and
the formatting; `dashboard.js` and `flyout.js` draw. They hold no rules: every number is in the Snapshot or
computed from its timestamps (durations tick in the browser, corrected by the server's clock). All text from
GitHub (job names, branch names, log lines) is escaped before it reaches the page.

## The tray

`internal/tray` turns a Snapshot into menu rows, a tooltip and an icon (pure functions, tested).
`internal/tray/shell` is the Wails v3 glue: a tray icon, a hidden frameless window that loads
`/flyout.html` and is toggled by a left click, and the native menu on a right click. The menu is rebuilt
only when its rows change and never within 20 s of being opened, because rebuilding an open menu closes it.

## Code layout

```
cmd/runwatch            flags, then wiring + the tray shell (or --headless)
internal/domain         types and pure rules (no I/O, no imports from this module)
internal/core           the Watcher. Imports only domain; its collaborators are interfaces
internal/adapters/
  runnerfs              a runner's folder: .runner, Runner log, pages tail, sizes
  winsvc                runner services: list, state and process (no admin), start/stop (UAC)
  sysusage              the PC's processor time, memory and process list
  machine               core.Machine = winsvc + runnerfs
  github                the Actions API client and the token lookup through gh
  history               history.jsonl
  notify                Windows toasts
  execx                 the one place that starts programs (hidden console window)
internal/config         config.json + RUNWATCH_* (the only package that knows the data folder)
internal/server         the local web server
internal/tray           menu, tooltip, icon; tray/shell is the Wails glue
internal/wiring         builds the app from the parts; also starts the demo
internal/fakegh         a pretend GitHub + pretend runner folders (tests, e2e, --demo)
web/                    the dashboard and flyout pages
e2e/                    end-to-end tests
```

`internal/arch_test.go` enforces the import rules (for example: core imports only domain; adapters never
import core; the tray never imports an adapter).
