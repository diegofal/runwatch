# Runwatch: watch the CI runners on this PC

Started 2026-11-10. The goal is a tray app to watch every CI runner on this PC, plus a dashboard for details.
From the tray you can stop and re-run work, see how long each job waited and has been running, follow its
progress, and watch its terminal.

**Status: built.** See §6 for what was built and §7 for what is open. User docs: [README.md](README.md).

The prototypes that the design was chosen from (made-up data, simulated at 8× speed, open them in a browser):

- `prototype/tray.html`: three tray designs side by side (A native menu, B flyout, C icon + notifications).
- `prototype/dashboard.html`: the dashboard, with the Now / Queue / History / Runners tabs and a job panel
  that has a live terminal.

## 1. Facts checked on this PC (2026-11-10)

| What | Fact |
|---|---|
| Runners | 9 GitHub Actions self-hosted runners, each a Windows service `actions.runner.<owner>-<repo>.<name>`, installed in `D:\actions-runner\<name>\` and running as NETWORK SERVICE |
| Split | repo A ×3 (a work repo), repo B ×1 and repo C ×5 (personal repos). Each runner is registered to **one repo**. |
| Which repo | `<dir>\.runner` (JSON) has `gitHubUrl` and `agentName` |
| Running now | Each runner's `_diag\Runner_*.log` has `Running job: <name>` and `Job <name> completed with result: <X>` lines. While a job runs, a `Runner.Worker` process exists. |
| Live terminal | The running step's output is written to `_diag\pages\<planId>_<recordId>_<n>.log` before it is uploaded. It arrives in 8 KB blocks, so a quiet step can look stuck for a while. |
| Queue | Jobs that are still waiting never reach the PC, so the only place to see them is the GitHub API (`/actions/runs?status=queued`, then `/runs/{id}/jobs`). For a queued job, `started_at` = `created_at`, so the wait is `now − created_at`. |
| Queue today | repo C had **81 jobs waiting** across 21 runs, the oldest for ~1 h. One `changes` job waited 29 min. All 5 repo C runners were busy and the 3 repo A runners were idle. |
| Cancelled | **21 of the last 30** finished repo C runs ended `cancelled` (most likely a newer push to the same branch, via concurrency `cancel-in-progress`) |
| Stop / re-run | Only through the GitHub API: `POST runs/{id}/cancel` (and `force-cancel`), `runs/{id}/rerun`, `runs/{id}/rerun-failed-jobs`, `jobs/{id}/rerun`. **The API can't cancel a single job; it cancels the whole run.** |
| Accounts | `gh` has two accounts in the keyring: your personal account (<you>/*) and the work account (<work-org>/*). Both have the `repo` and `workflow` scopes. |
| Service control | Starting or stopping a runner service needs admin. `D:\actions-runner\README.txt` says the repo A runners are managed **only** through `scripts\ci-runners.cmd` in repo A. |
| Disk | `_diag` takes ~340–360 MB per repo C runner (1,607 files in one of them), and one repo A runner's `_work` is 812 MB. D: has 2.9 TB free. |
| Machine | 32 logical cores, 126 GB RAM. CPU was at 69 % with 6 jobs running. |

## 2. Proposal

**One Go app (Go 1.26 + Wails v3, the stack of two earlier tray apps).** It has a tray icon and a flyout
window, and it serves the dashboard on `127.0.0.1` so the dashboard also opens in a browser.

Tray (simplified):

- **Icon**: blue while jobs run. Amber with a count once a job has waited more than 15 min. A red dot when a
  job failed in the last hour.
- **Left click opens flyout B**: Running / Waiting / Oldest-wait figures, one row per repo with a slot per
  runner, and the running jobs with their step (k/n), elapsed time and usual time. Clicking a job opens its
  terminal inline (the last lines). Stopping asks for confirmation inside the row. Failed jobs from the last
  hour have a re-run button.
- **Right click opens menu A**: the same information as a native menu, plus Open dashboard and Quit.
- **Notifications (from C)**: a job failed (buttons: Re-run, Log), or a job waited more than 45 min (buttons:
  Stop run, Queue). Nothing else by default.

Dashboard:

- **Now**: a timeline for the last hour, one lane per runner, with the running-now column and a waiting-per-repo graph.
- **Queue**: a board of waiting runs, oldest first, with how long they waited, and stop run.
- **History**: finished jobs with waited time, run time and result, plus counts and median wait/run.
  Re-run is available on each row.
- **Runners**: each service's state, how busy it was, `_diag` / `_work` size, and start/stop (admin).
- **Job panel**: steps, queued / waited / ran / usual times, and a terminal (live from this PC while the job
  runs, from the GitHub logs API once it's done) with find, times and follow.

"Progress" is shown only as facts: step k of n, time so far, and "usual" (the median time of the last 20
successful runs of the same repo + job). It never shows an invented percentage.

## 3. How it reads and acts

| Need | Source | Cadence |
|---|---|---|
| Runners, their repo, service state | Windows SCM (`x/sys/windows/svc/mgr`) + `.runner` | 5 s |
| Busy / which job | tail `Runner_*.log` (+ Worker process) | file watch |
| Live terminal | tail newest `_diag\pages\*.log` of the busy runner | file watch |
| Queue, steps, runs | GitHub REST, one token per owner, ETag requests (304 replies don't count against the rate limit) | 10 s while anything is queued or running, 60 s when idle |
| Finished logs | `GET jobs/{id}/logs` | on open |
| History / usual times | local SQLite (jobs seen) | — |
| Stop / re-run | GitHub REST | on click |
| Service start/stop | elevated helper (UAC). For repo A, run its `ci-runners.cmd` instead. | on click |

Safety: the server listens on 127.0.0.1 only. Actions need a per-launch token, and the server rejects foreign
`Origin` headers (otherwise any web page could POST "cancel"). Tokens are read from `gh`'s keyring and are
never written to disk or logs.

## 4. Decisions (recommended option taken; say so to change one)

1. **Standalone app**, not a module of an existing tray app: it has a different job (live operations).
2. **"Stop" = cancel the run**, because GitHub has no per-job cancel. The confirmation says how many jobs it stops.
3. **Repo A runners**: watch, cancel and re-run like the others, using the work account. Start/stop the
   service only through repo A's script, as its README asks.
4. **Notifications**: failed jobs and waits over 45 min only.
5. **No webhooks** (they would need a public endpoint). Polling with ETags is enough at this scale.

## 5. Things the prototypes surface (facts, no fix chosen)

- The repo C queue is the bottleneck: 5 runners and ~80 jobs waiting, while the repo A runners sit idle. A
  runner can only take its own repo's jobs.
- Much of repo C's runner time goes to runs that are later cancelled. The dashboard shows it ("Cancelled runs ·
  2 h").
- `_diag` grows without limit (~350 MB per runner and rising). A possible later action: clean diag logs older than N days.

## 6. Work packages and status (built 2026-11-10)

| WP | What | Status |
|---|---|---|
| 1 | Discovery: services + `.runner` + diag tail → runner state and current job (no network) | Done (`adapters/winsvc`, `runnerfs`, `machine`) |
| 2 | GitHub client: token per repo through `gh`, runs/jobs with ETag, actions (cancel, re-run job, re-run failed) | Done (`adapters/github`) |
| 3 | Store: finished jobs, usual = median of the last 20 successful runs | Done as a JSON-lines file (`adapters/history`), not SQLite: the standard library is enough at 5,000 jobs |
| 4 | Live terminal: pages tail kept in memory; finished → logs API | Done (`runnerfs.Tail`, `core/logs.go`). Served by polling `/api/log`, not SSE: simpler, and the source itself arrives in blocks |
| 5 | Local server + dashboard + flyout, loopback rules | Done (`internal/server`, `web/`). Host + header + Origin checks instead of a per-launch token, so the dashboard URL is stable |
| 6 | Tray: icon states, flyout B, menu A, notifications | Done (`internal/tray`, `tray/shell`, `adapters/notify`). The menu's job rows open the dashboard; stop and re-run live in the flyout and dashboard, where they can ask for confirmation. A notification opens the job or the queue when clicked; it has no Re-run button, because a click on a toast cannot ask for confirmation |
| 7 | Service start/stop through the admin prompt; `managed` runners are left to their own script | Done; Runwatch shows the reason instead of running another repo's script |
| 8 | Start at login | Done (tray menu). Packaging and update-from-tray: not built |
| 9 | Demo mode + pretend GitHub (`internal/fakegh`) | Done; added during the build: it is what the tests, the end-to-end tests and `--demo` run on |
| 10 | CPU and memory: the PC's, and each runner's own processes | Done 2026-11-12 (`adapters/sysusage`, `domain.Meter`). Read every 500 ms without admin; time of processes too short-lived to be seen is shown on its own, not guessed onto a runner |

Verified on 2026-11-10: `task ci` green (lint clean, coverage gates, 6 end-to-end tests including two real
Chrome sessions). On this PC, read-only against the real runners: all 9 found without admin, the right
GitHub account chosen per repo, busy runners matched to their GitHub jobs, a running job's output followed,
a finished job's log downloaded. Tray icon, flyout and right-click menu checked on the desktop in demo mode.

## 7. Open

| # | What | Note |
|---|---|---|
| O1 | Stop, re-run and service start/stop have not been used against real GitHub and real services | Tested end to end against the pretend GitHub only; the first real use should be on a run that is safe to cancel |
| O2 | Notifications not seen on the desktop | The toast library is one already used in another app here; the click-to-open URL is new |
| O3 | Organization-level runners | Listed with a problem message; needs the org's repo list to watch |
| O4 | Backoff when the GitHub rate limit runs low | Polling is ETag-conditional so the steady cost is close to nothing; no backoff yet |
| O5 | Clean-up of old `_diag` logs | Sizes are shown; deleting is not offered |
| O6 | Packaging (installer, release workflow), update from the tray | Not built |
| O7 | macOS and Linux | The core builds anywhere; services, notifications and the tray are Windows-only today |
