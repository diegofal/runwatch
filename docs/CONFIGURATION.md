# Configuration

Runwatch works with no configuration when the runners are installed as Windows services and `gh` is logged
in. This page is for everything else.

## Where things live

The data folder is `%APPDATA%\Runwatch` (set `RUNWATCH_DATA_DIR` to use another one).

| File | What it is |
|---|---|
| `config.json` | The settings below. Written with the defaults on first start. Read at start: restart after editing. |
| `history.jsonl` | Finished jobs Runwatch has seen, one JSON object per line, newest 5,000 kept. The History tab and the "usual" times come from it. Safe to delete: the last 20 finished runs per repo are read again from GitHub at the next start. |
| `runwatch.log` | What the app did and every failed read. Starts over at 5 MB. Never contains a token. |
| `url.txt` | The dashboard's address of the running copy (useful when the port is not the default). |

## config.json

```json
{
  "roots": [],
  "addr": "127.0.0.1:7878",
  "managed": {},
  "services": true,
  "notifications": true
}
```

| Key | Default | Meaning |
|---|---|---|
| `roots` | `[]` | Folders that contain runner folders (or are runner folders). Searched in addition to the runners installed as services. Needed only for runners started by hand (`run.cmd`). Example: `["D:\\actions-runner"]`. |
| `addr` | `127.0.0.1:7878` | Where the dashboard is served. Only loopback addresses are accepted; anything else stops the start with an error. |
| `managed` | `{}` | Runners whose service Runwatch must not start or stop. Maps a pattern over the runner's **folder name** to the reason shown in place of the buttons. Example: `{"billing-*": "Use scripts\\ci-runners.cmd in the billing repo."}`. The pattern is a glob (`*`, `?`). These runners are still watched, and their runs can still be stopped and re-run. |
| `services` | `true` | Look for runners installed as Windows services (`actions.runner.*`). With `false`, only `roots` are searched and no service can be started or stopped. |
| `notifications` | `true` | Desktop notifications for a failed job and for a run waiting more than 45 minutes. |
| `githubApi` | (absent) | API base URL for GitHub Enterprise Server, e.g. `https://ghe.example.com/api/v3`. Absent means `https://api.github.com`. With an Enterprise Server also set `RUNWATCH_GITHUB_TOKEN`: the automatic lookup through `gh` asks for github.com accounts only. |

A `config.json` that is not valid JSON stops the start with an error that names the file. It never silently
falls back to the defaults.

## Environment variables

Each one wins over `config.json`.

| Variable | Meaning |
|---|---|
| `RUNWATCH_DATA_DIR` | The data folder. |
| `RUNWATCH_ROOTS` | Runner folders, separated like `PATH` (`;` on Windows). Replaces `roots`. |
| `RUNWATCH_ADDR` | Listen address. `127.0.0.1:0` picks a free port and writes it to `url.txt`. |
| `RUNWATCH_GITHUB_API` | API base URL. Replaces `githubApi`. |
| `RUNWATCH_GITHUB_TOKEN` | One token used for every repo instead of asking `gh`. It needs to read Actions and, to stop and re-run, write them (classic token: `repo`; fine-grained: Actions read and write). It is read from the environment only and never saved. |
| `RUNWATCH_SERVICES` | `on` or `off`. Replaces `services`. The tests set `off` so they never see real runners. |
| `RUNWATCH_POLL_MS` | Ask GitHub this often, in milliseconds (50 or more). For tests; the normal pace is below. |

## How often it asks GitHub

- Every 10 seconds while anything is queued or running, every 60 seconds when idle.
- At once when a runner on this PC picks up or finishes a job (seen in its own log within 2 seconds), and
  after every action you take.
- Every request is conditional (ETag). An unchanged answer comes back as "304 Not Modified", which GitHub
  does not count against the rate limit, so the steady cost is close to zero.

## Command line

```
runwatch              the tray app
runwatch --headless   no tray: only the dashboard server, until Ctrl+C
runwatch --demo       made-up runners and a pretend GitHub (combine with --headless if you like)
runwatch --version
```

Only one copy can serve a given address: a second copy on the same `addr` exits with "is Runwatch already
running?".
