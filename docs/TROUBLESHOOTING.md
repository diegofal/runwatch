# Troubleshooting

Start with the red bar on the dashboard or flyout: it names the read that is failing. The same text, with
more detail, is in `%APPDATA%\Runwatch\runwatch.log` (tray menu, **Open log**).

## I cannot find the tray icon

Windows puts new icons in the hidden-icons overflow: click the `^` arrow at the left of the tray and drag
the three-bar icon onto the taskbar. If it is not there either, the app is not running: start
`runwatch.exe` from a terminal to see the error, or read `runwatch.log`.

## Messages

| Message | Meaning | What to do |
|---|---|---|
| `no GitHub token: ... gh auth status failed` | The GitHub CLI is missing or logged out | Install `gh` and run `gh auth login`. Or set `RUNWATCH_GITHUB_TOKEN`. |
| `no gh account can read <repo> (tried: a, b)` | None of the accounts `gh` is logged in to can see that repo | `gh auth login` with an account that can, or check the account's access to the repo |
| `github <repo>: ... HTTP 401` | The token expired or was revoked | Runwatch asks `gh` again on the next poll. If it stays, run `gh auth status` and log in again. |
| `github <repo>: ... HTTP 403` on stop or re-run | The token can read Actions but not write them | Use an account with write access to the repo; a classic token needs the `repo` scope |
| `github <repo>: ... HTTP 403` or `429` mentioning a rate limit | Too many requests on that account | It recovers when the limit resets. Other tools sharing the token are the usual cause: Runwatch's own polling is nearly free (see Configuration). |
| `github <repo> runner labels: Could not read the runners' labels ...` | GitHub only lists a repo's runners (with their labels) for an account that administers the repo | Log `gh` in with an account that is an admin of the repo; Runwatch tries again every ten minutes. Until then waiting jobs are counted only if they ask for the `self-hosted` label, so a workflow with `runs-on: <custom label>` shows no queue. |
| `github <repo>: ... dial tcp` / `timeout` | No network path to GitHub | The last known numbers stay on screen with this warning; it clears by itself |
| `runner <name>: .runner has no repository URL ... organization runners are not supported` | The runner is registered to an organization | A known limit: only repository runners are watched |
| `runner <name>: parse .runner` / `read .runner` | The runner's folder is damaged or unreadable by your user | Check the folder; re-register the runner if `.runner` is broken |
| `this PC: runner folder <path>: ...` | A folder in `roots` does not exist or cannot be listed | Fix `roots` in `config.json` |
| `this PC: open the service manager` / `service <name>: ...` | Windows refused to list or query a service | Rare; the runner is still watched through its files. Check the log for the Windows error. |
| `listen on 127.0.0.1:7878 (is Runwatch already running?)` | Another copy, or another program, has the port | Use the running copy, or change `addr` |
| `addr "..." must be a loopback address` | `addr` is not on 127.0.0.1 | Runwatch refuses to listen on the network; set a loopback address |
| `config.json is not valid JSON` | A typo in the file | Fix it, or delete it to get the defaults back |
| `... is managed elsewhere: <reason>` | The runner matches a `managed` pattern | Use the tool the reason names |
| `... (needs admin)` after start or stop | The Windows admin prompt was declined, or `sc.exe` failed | Try again and accept the prompt |
| Page banner: `Not connected to Runwatch — numbers are not current` | The page lost the app (it was quit or crashed) | Start Runwatch; the page reconnects by itself |

## The terminal of a running job is empty

Expected in two cases, and the terminal says which:

- "No output yet": the runner writes output to disk a few KB at a time. A step that prints little shows
  nothing for a while.
- "This job started before Runwatch did": the runner had already uploaded and deleted its earlier output.

The whole log appears when the job ends. For a live view of a job with gaps, use **open on GitHub**.

## A job is running but its row shows no step

The runner's own log shows a job starting within two seconds; GitHub's API can take a few more to list its
steps. The row fills in on the next poll.

## Re-run says "already running"

GitHub only re-runs a job when its whole run has finished. Wait for the run's other jobs, or stop the run
first.

## The numbers differ from GitHub's page

Runwatch counts only jobs for this PC's runners: jobs that ran on them, and waiting jobs whose `runs-on`
labels are all carried by one of this PC's runners for that repo. Jobs on GitHub-hosted machines and on
other PCs' runners are left out. Waiting jobs that another PC's runner with the same labels could also take
are counted: until a runner picks a job up, GitHub does not say which one will.

If a repo's queue shows as empty while GitHub shows waiting jobs, look for the "runner labels" message above.

## Resetting

Quit Runwatch and delete `%APPDATA%\Runwatch`. Nothing outside that folder is kept, except the "start at
login" entry (untick it in the menu first) and the app's registration for notifications.
