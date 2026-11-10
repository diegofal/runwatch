# Runwatch

Windows tray app + local dashboard (Go 1.26, Wails v3.0.0, pure Go, no cgo on Windows) that watches the
GitHub Actions self-hosted runners installed on the PC. Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
before changing anything: it says where every fact comes from and why. [PLAN.md](PLAN.md) has the facts the
design rests on, the decisions, and what is still open.

## Dev setup

Go 1.26 must be first on PATH before any `go` or `task` command. `task ci` = format check, vet,
golangci-lint, tests with coverage gates (domain ≥ 90 %, the rest ≥ 85 %), the end-to-end tests (they need
Chrome or Edge) and the build. Run it before every commit.

## Hard rules

1. **Tests never touch anything real.** No real repository, runner, token or data folder. Everything runs
   on `internal/fakegh`. A program started by a test gets `RUNWATCH_DATA_DIR`, `RUNWATCH_ROOTS`,
   `RUNWATCH_GITHUB_API`, `RUNWATCH_GITHUB_TOKEN` and `RUNWATCH_SERVICES=off` (without the last one it finds
   the real runner services of the machine running the tests).
2. **Tokens never reach disk or a log.** They come from `gh` or the environment and stay in memory. Never
   print one, never put a real one in a fixture.
3. **A failed read is shown, never hidden.** It becomes a `domain.Problem` in the Snapshot; the data it
   could not refresh stays, beside the problem. No stale number shown as current, no empty catch, no
   log-and-continue.
4. **Facts, not estimates.** Show step k of n, time so far, and "usual" (a median of past runs). Never an
   invented percentage or a guessed finish time.
5. **Runner folders are read-only.** The only change Runwatch makes on the PC is starting or stopping a
   runner service when the user asks, through the Windows admin prompt.
6. **The core only acts on what is in the Snapshot** (a run with unfinished jobs here, a finished job it has
   seen, a runner on this PC). Do not add an API that passes ids straight through to GitHub.
7. **The server stays on loopback** with the rules in `internal/server` (Host check, no CORS, `X-Runwatch`
   header, Origin check, CSP). The pages make no external request and have no inline script.
8. **No real information in the repo.** No real name, email, company, repo or machine name in code, tests,
   docs, commit messages or screenshots. Use the demo's names (`example/app`, `acme/billing`, `app-1`).
   Screenshots come from `--demo`.
9. Front ends (tray, pages) hold no rules: they draw the Snapshot and call the API.
10. Never use a browser dialog (`alert`, `confirm`): confirmations are inline, and they say what will happen
    (stopping a run says how many jobs).

## Code rules

- Reuse before writing; standard library first; a new module needs a reason in the commit message.
- No comments that restate the code; comment only the *why*. No placeholders, no TODO without an issue.
- Errors propagate with context (`fmt.Errorf("…: %w", err)`).
- Interfaces live where they are used (`core.GitHub`, `core.Machine`) and exist because the tests need the
  fake side.
- Delete dead code in the same change. Kebab-case files, Go names in code, user-facing text in English.
- Child processes go through `internal/adapters/execx` (hidden console window: the tray has no console).
- `internal/arch_test.go` enforces the import rules; a new package must be added there.
- Stop a test copy by its exact PID (`powershell -NoProfile -Command "Stop-Process -Id <pid> -Force"`),
  never by name or path: a real copy may be running.

## Pitfalls already known

- A run is `queued` while any of its jobs waits, even with others running. Read both lists.
- A waiting job has `started_at` = `created_at`; a job cancelled in the queue has a `started_at` and no
  runner. Use `Job.Started()`, never the timestamp alone.
- `.runner` starts with a byte order mark, and so does each `_diag\pages` file.
- Page files live for seconds: the runner deletes them after upload. The tail must keep what it has read.
- Rebuilding the tray menu while it is open closes it. The shell rebuilds only when the rows change and
  never within 20 s of a right click.
- A new tray icon starts in Windows' hidden-icons overflow.
- Writing Go source through a shell heredoc mangles backslash escapes (`"\xef"`, `"\n"`). Write files with
  an editor tool, and let a test catch the rest.
- Defender sometimes quarantines tiny stripped Go test binaries: build test helpers unstripped.
