// Runwatch prototype — simulated CI runners.
// Shapes mirror what the real app reads: Windows services + _diag logs on this PC,
// and the GitHub Actions REST API (runs, jobs, steps). All names are made up.
(function () {
  const SPEED = 8;            // simulated seconds per real second
  const TICK_MS = 250;
  const MIN = 60, H = 3600;

  const STEP_SETS = {
    unit:  [['Set up job', 6], ['Checkout', 14], ['Setup Node', 8], ['Install dependencies', 50], ['Run tests', 230], ['Upload report', 9], ['Post checkout', 3], ['Complete job', 2]],
    e2e:   [['Set up job', 6], ['Checkout', 14], ['Setup Node', 8], ['Install dependencies', 50], ['Install browsers', 40], ['Run e2e', 380], ['Upload traces', 15], ['Complete job', 2]],
    lint:  [['Set up job', 6], ['Checkout', 12], ['Setup Node', 8], ['Install dependencies', 48], ['Lint', 55], ['Typecheck', 80], ['Complete job', 2]],
    build: [['Set up job', 6], ['Checkout', 14], ['Setup Go', 10], ['Build', 140], ['Package', 35], ['Complete job', 2]],
    tiny:  [['Set up job', 5], ['Checkout', 10], ['Detect changes', 20], ['Complete job', 2]],
  };

  const REPOS = {
    'example/app':   { short: 'app',   color: '#5ad1ff', runners: 5, branches: ['feature/ledger-rows', 'fix/lease-floor', 'feature/mentions', 'chore/deps-bump', 'feature/board-city'],
      jobs: [['changes', 'tiny'], ['core', 'unit'], ['core (units)', 'unit'], ['unit (engine)', 'unit'], ['unit (headless)', 'unit'], ['unit (hosts a–h)', 'unit'], ['unit (hosts i–z)', 'unit'], ['e2e (web board)', 'e2e'], ['e2e (scenarios)', 'e2e'], ['lint', 'lint']] },
    'example/hooks': { short: 'hooks', color: '#c792ea', runners: 1, branches: ['main', 'fix/exit-codes'],
      jobs: [['test', 'unit'], ['lint', 'lint']] },
    'acme/billing':  { short: 'billing', color: '#ffcb6b', runners: 3, branches: ['feature/invoice-pdf', 'fix/tax-rounding', 'main'],
      jobs: [['api tests', 'unit'], ['web tests', 'e2e'], ['lint', 'lint'], ['build', 'build']] },
  };

  const ACTORS = ['dev-a', 'dev-b', 'bot-deps', 'dev-c'];
  const rnd = (a, b) => a + Math.random() * (b - a);
  const pick = a => a[Math.floor(Math.random() * a.length)];
  const hex = n => Array.from({ length: n }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('');

  let nextId = 900100, nextRun = 4280;
  const S = {
    SPEED,
    now: Date.now(),
    repos: REPOS,
    runners: [],
    jobs: new Map(),
    runs: new Map(),
    events: [],          // {t, kind, text, jobId, runId}
    listeners: [],
    paused: false,
  };

  // ---------- builders ----------
  function makeRun(repo, branch, at) {
    const run = { id: nextRun++, repo, branch, sha: hex(7), actor: pick(ACTORS), workflow: 'CI',
      event: branch === 'main' ? 'push' : 'pull_request', pr: branch === 'main' ? null : 100 + Math.floor(rnd(0, 900)),
      createdAt: at, attempt: 1, jobIds: [] };
    S.runs.set(run.id, run);
    return run;
  }
  function makeJob(run, name, kind, at, attempt = 1) {
    const base = STEP_SETS[kind];
    const jitter = rnd(0.8, 1.25);
    const steps = base.map(([n, d]) => ({ name: n, dur: Math.round(d * jitter * rnd(0.85, 1.15)), status: 'pending', conclusion: null, startedAt: null, finishedAt: null }));
    const usual = base.reduce((s, [, d]) => s + d, 0);
    const willFail = Math.random() < (kind === 'e2e' ? 0.22 : 0.1);
    const job = { id: nextId++, runId: run.id, repo: run.repo, name, kind, queuedAt: at, startedAt: null, finishedAt: null,
      status: 'queued', conclusion: null, steps, usual, willFail, runner: null, attempt, log: [], logCursor: 0 };
    S.jobs.set(job.id, job);
    run.jobIds.push(job.id);
    return job;
  }
  function pushRun(repo, branch, at) {
    const R = REPOS[repo];
    const run = makeRun(repo, branch, at);
    // A newer push to the same branch cancels the older run (concurrency: cancel-in-progress).
    for (const r of S.runs.values()) {
      if (r !== run && r.repo === repo && r.branch === branch) {
        const live = r.jobIds.map(id => S.jobs.get(id)).filter(j => j.status !== 'completed');
        if (live.length) {
          live.forEach(j => finishJob(j, 'cancelled', 'superseded'));
          emit('superseded', `${R.short} · ${branch}: run #${r.id} cancelled by a newer push (${live.length} jobs)`, null, r.id);
        }
      }
    }
    for (const [n, k] of R.jobs) makeJob(run, n, k, at);
    return run;
  }

  // ---------- logs ----------
  const TESTS = ['lease floor fits a core job', 'ledger rows keep order', 'retry backs off', 'board renders city', 'mentions resolve aliases',
    'hosts pick a free port', 'token refresh survives restart', 'tax rounding half-even', 'invoice pdf has totals', 'queue drains in order',
    'exit code propagates', 'cache key includes lockfile', 'headless boot under 2s', 'worktree cleanup is safe'];
  function stepLines(job, step, i) {
    const k = step.name;
    if (k === 'Set up job') return ['Current runner version: \'2.330.0\'', `Runner name: '${job.runner}'`, 'Operating System', '  Microsoft Windows 11 Pro', 'Prepare workflow directory', 'Download action repository \'actions/checkout@v5\''];
    if (k === 'Checkout') return ['Syncing repository: ' + job.repo, '[command]git version', 'git version 2.51.0.windows.1', `[command]git -c protocol.version=2 fetch --no-tags --prune --depth=1 origin`, `HEAD is now at ${S.runs.get(job.runId).sha} ${pick(['keep the lease floor', 'split the queue', 'fix flaky retry', 'bump deps'])}`];
    if (k.startsWith('Setup')) return ['Found in cache @ C:\\hostedtoolcache\\node\\22.23.2\\x64', 'Environment details', '  node: v22.23.2', '  npm: 10.9.4'];
    if (k.startsWith('Install dep')) return ['[command]npm ci', 'npm warn deprecated inflight@1.0.6', `added ${Math.floor(rnd(400, 1200))} packages in ${Math.floor(rnd(20, 60))}s`];
    if (k === 'Install browsers') return ['Downloading Chromium 141.0 — 172 MiB', '|■■■■■■■■■■■■■■■■■■■■■■■■| 100%', 'Chromium downloaded to C:\\ms-playwright\\chromium-1190'];
    if (k.startsWith('Run') || k === 'Lint' || k === 'Typecheck' || k === 'Build') return null; // streamed
    if (k.startsWith('Upload')) return ['With the provided path, there will be 3 files uploaded', 'Artifact upload finished: report.zip (412 KB)'];
    if (k === 'Detect changes') return ['Changed areas: engine, hosts, web', 'Shards to run: 9'];
    return ['Cleaning up orphan processes'];
  }
  function streamLine(job, step) {
    const k = step.name;
    if (k === 'Lint') return Math.random() < 0.08 ? { text: `  warn  src/${pick(['queue', 'lease', 'board'])}.ts:${Math.floor(rnd(10, 400))}  prefer-const`, cls: 'warn' } : { text: `  ✓ ${pick(['src/', 'lib/', 'web/'])}${pick(['queue', 'lease', 'board', 'hosts', 'ledger'])}/${pick(['index', 'model', 'view'])}.ts`, cls: 'dim' };
    if (k === 'Typecheck') return { text: `tsc: checked ${Math.floor(rnd(100, 900))} files`, cls: 'dim' };
    if (k === 'Build' || k === 'Package') return { text: `  → ${pick(['compiling', 'linking', 'bundling'])} ${pick(['internal/queue', 'cmd/app', 'web/assets', 'internal/ledger'])}`, cls: 'dim' };
    const t = pick(TESTS), ms = Math.floor(rnd(3, 900));
    return { text: `  ✓ ${t} (${ms} ms)`, cls: 'ok' };
  }
  function log(job, text, cls) {
    job.log.push({ t: S.now, text, cls: cls || '' });
    if (job.log.length > 1500) job.log.splice(0, job.log.length - 1500);
  }

  // ---------- lifecycle ----------
  function startJob(job, runner, at) {
    job.status = 'in_progress'; job.startedAt = at; job.runner = runner.name; runner.jobId = job.id;
    log(job, `Waiting for a runner to pick up this job... picked by ${runner.name} after ${fmtDur((at - job.queuedAt) / 1000)}`, 'dim');
    advanceStep(job, at);
  }
  function advanceStep(job, at) {
    const i = job.steps.findIndex(s => s.status !== 'completed');
    if (i < 0) return finishJob(job, 'success');
    const s = job.steps[i];
    s.status = 'in_progress'; s.startedAt = at;
    log(job, s.name, 'group');
    (stepLines(job, s, i) || [`[command]${s.name === 'Run e2e' ? 'npx playwright test' : s.name === 'Run tests' ? 'node scripts/test-shard.cjs run "$SHARD"' : s.name.toLowerCase()}`]).forEach(l => log(job, l, l.startsWith('[command]') ? 'cmd' : ''));
  }
  function finishJob(job, conclusion, why) {
    const at = S.now;
    if (job.status === 'in_progress') {
      job.steps.forEach(s => {
        if (s.status === 'in_progress') { s.status = 'completed'; s.conclusion = conclusion === 'success' ? 'success' : conclusion; s.finishedAt = at; }
        else if (s.status === 'pending') { s.status = 'completed'; s.conclusion = 'skipped'; }
      });
      if (conclusion === 'cancelled') log(job, '##[error]The operation was canceled.', 'err');
      if (conclusion === 'failure') log(job, '##[error]Process completed with exit code 1.', 'err');
      log(job, `Job completed with result: ${conclusion}`, conclusion === 'success' ? 'ok' : 'err');
    }
    job.status = 'completed'; job.conclusion = conclusion; job.finishedAt = at; job.why = why || null;
    if (!job.startedAt) job.startedAt = at; // matches the API: a never-started job reports started_at
    const r = S.runners.find(r => r.jobId === job.id);
    if (r) { r.jobId = null; r.history.push({ jobId: job.id, from: job.startedAt, to: at, conclusion }); }
    if (conclusion === 'failure') emit('failed', `${REPOS[job.repo].short} · ${job.name} failed on ${job.runner} after ${fmtDur((at - job.startedAt) / 1000)}`, job.id, job.runId);
  }

  function tick(dtSim) {
    S.now += dtSim * 1000;
    for (const r of S.runners) {
      if (r.service !== 'running') continue;
      if (r.jobId) {
        const job = S.jobs.get(r.jobId);
        const s = job.steps.find(s => s.status === 'in_progress');
        if (!s) { advanceStep(job, S.now); continue; }
        const isStream = ['Run tests', 'Run e2e', 'Lint', 'Typecheck', 'Build', 'Package'].includes(s.name);
        if (isStream && Math.random() < 0.55) {
          const n = Math.ceil(rnd(0, 3));
          for (let i = 0; i < n; i++) { const l = streamLine(job, s); log(job, l.text, l.cls); }
        }
        const el = (S.now - s.startedAt) / 1000;
        if (job.willFail && (s.name === 'Run tests' || s.name === 'Run e2e') && el > s.dur * 0.7) {
          log(job, `  ✗ ${pick(TESTS)}`, 'err');
          log(job, `    AssertionError: expected ${Math.floor(rnd(1, 9))} to equal ${Math.floor(rnd(10, 20))}`, 'err');
          log(job, `      at ${pick(['test/queue.test.js', 'test/e2e/board.spec.ts', 'test/lease.test.js'])}:${Math.floor(rnd(10, 300))}:${Math.floor(rnd(2, 40))}`, 'dim');
          s.status = 'completed'; s.conclusion = 'failure'; s.finishedAt = S.now;
          finishJob(job, 'failure');
          continue;
        }
        if (el >= s.dur) { s.status = 'completed'; s.conclusion = 'success'; s.finishedAt = S.now; advanceStep(job, S.now); }
      } else {
        const next = queued(r.repo)[0];
        if (next) startJob(next, r, S.now);
      }
    }
    // long waits → one alert per job
    for (const j of S.jobs.values()) {
      if (j.status === 'queued' && !j.waitAlerted && (S.now - j.queuedAt) > 45 * MIN * 1000) {
        j.waitAlerted = true;
      }
    }
    // new pushes now and then
    if (Math.random() < dtSim / 260) {
      const repo = Math.random() < 0.75 ? 'example/app' : pick(Object.keys(REPOS));
      const run = pushRun(repo, pick(REPOS[repo].branches), S.now);
      emit('pushed', `${REPOS[repo].short} · ${run.branch}: new run #${run.id} (${run.jobIds.length} jobs queued)`, null, run.id);
    }
  }

  function emit(kind, text, jobId, runId) {
    S.events.unshift({ t: S.now, kind, text, jobId, runId, id: Math.random() });
    if (S.events.length > 60) S.events.pop();
  }

  // ---------- queries ----------
  function queued(repo) {
    return [...S.jobs.values()].filter(j => j.status === 'queued' && (!repo || j.repo === repo)).sort((a, b) => a.queuedAt - b.queuedAt || a.id - b.id);
  }
  function running() { return [...S.jobs.values()].filter(j => j.status === 'in_progress'); }
  function completed() { return [...S.jobs.values()].filter(j => j.status === 'completed').sort((a, b) => b.finishedAt - a.finishedAt); }
  function stepIndex(job) { const i = job.steps.findIndex(s => s.status === 'in_progress'); return i < 0 ? job.steps.length : i; }
  function usualFor(job) { return job.usual; }
  function summary() {
    const q = queued(), run = running();
    const online = S.runners.filter(r => r.service === 'running');
    const oldest = q.length ? (S.now - q[0].queuedAt) / 1000 : 0;
    const failedHour = completed().filter(j => j.conclusion === 'failure' && S.now - j.finishedAt < H * 1000);
    return { busy: run.length, online: online.length, total: S.runners.length, waiting: q.length, oldest, failedHour: failedHour.length,
      byRepo: Object.keys(REPOS).map(repo => ({ repo, short: REPOS[repo].short, color: REPOS[repo].color,
        runners: S.runners.filter(r => r.repo === repo).length, busy: run.filter(j => j.repo === repo).length,
        waiting: q.filter(j => j.repo === repo).length, oldest: (q.find(j => j.repo === repo) || {}).queuedAt })) };
  }

  // ---------- actions (real app: GitHub REST API) ----------
  const actions = {
    // POST /repos/{o}/{r}/actions/runs/{run_id}/cancel
    cancelRun(runId) {
      const run = S.runs.get(runId); if (!run) return;
      const live = run.jobIds.map(id => S.jobs.get(id)).filter(j => j.status !== 'completed');
      live.forEach(j => finishJob(j, 'cancelled', 'you'));
      emit('cancelled', `${REPOS[run.repo].short} · run #${run.id} cancelled by you (${live.length} jobs)`, null, runId);
    },
    // POST /repos/{o}/{r}/actions/jobs/{job_id}/rerun
    rerunJob(jobId) {
      const j = S.jobs.get(jobId); const run = S.runs.get(j.runId);
      const nj = makeJob(run, j.name, j.kind, S.now, j.attempt + 1);
      emit('rerun', `${REPOS[j.repo].short} · ${j.name} queued again (attempt ${nj.attempt})`, nj.id, run.id);
      return nj.id;
    },
    // POST /repos/{o}/{r}/actions/runs/{run_id}/rerun-failed-jobs
    rerunFailed(runId) {
      const run = S.runs.get(runId);
      const latest = new Map();
      run.jobIds.map(id => S.jobs.get(id)).forEach(j => latest.set(j.name, j));
      const bad = [...latest.values()].filter(j => j.status === 'completed' && j.conclusion !== 'success');
      bad.forEach(j => makeJob(run, j.name, j.kind, S.now, j.attempt + 1));
      emit('rerun', `${REPOS[run.repo].short} · run #${run.id}: ${bad.length} failed/cancelled jobs queued again`, null, runId);
    },
    // simulate a developer pushing (creates a run; cancels the older run on that branch)
    push(repo, branch) {
      const run = pushRun(repo, branch || pick(REPOS[repo].branches), S.now);
      emit('pushed', `${REPOS[repo].short} · ${run.branch}: new run #${run.id} (${run.jobIds.length} jobs queued)`, null, run.id);
      return run.id;
    },
    // Windows service — needs admin (UAC prompt in the real app)
    setService(runnerName, on) {
      const r = S.runners.find(r => r.name === runnerName);
      if (!on && r.jobId) finishJob(S.jobs.get(r.jobId), 'failure', 'runner stopped');
      r.service = on ? 'running' : 'stopped';
      emit('service', `${r.name}: service ${on ? 'started' : 'stopped'} (admin)`, null, null);
    },
  };

  // ---------- seed ----------
  function seed() {
    const t0 = S.now;
    for (const [repo, R] of Object.entries(REPOS)) {
      for (let i = 1; i <= R.runners; i++) {
        S.runners.push({ name: `${R.short}-${i}`, repo, service: 'running', jobId: null, history: [],
          diagMB: Math.round(rnd(180, 380)), workMB: Math.round(rnd(150, 820)), version: '2.330.0' });
      }
    }
    // history for the last ~2h: finished jobs per runner (for the timeline + history table)
    for (const r of S.runners) {
      let t = t0 - 2 * H * 1000;
      const busyness = r.repo === 'example/app' ? 0.92 : r.repo === 'acme/billing' ? 0.25 : 0.5;
      while (t < t0 - 6 * MIN * 1000) {
        if (Math.random() > busyness) { t += rnd(4, 20) * MIN * 1000; continue; }
        const R = REPOS[r.repo];
        const [name, kind] = pick(R.jobs);
        const run = makeRun(r.repo, pick(R.branches), t - rnd(1, r.repo === 'example/app' ? 40 : 3) * MIN * 1000);
        const j = makeJob(run, name, kind, run.createdAt);
        const dur = j.steps.reduce((s, x) => s + x.dur, 0) * rnd(0.6, 1.1);
        const concl = Math.random() < (r.repo === 'example/app' ? 0.45 : 0.08) ? 'cancelled' : (j.willFail ? 'failure' : 'success');
        const end = t + (concl === 'cancelled' ? dur * rnd(0.2, 0.8) : dur) * 1000;
        Object.assign(j, { status: 'completed', conclusion: concl, startedAt: t, finishedAt: end, runner: r.name, why: concl === 'cancelled' ? 'superseded' : null });
        j.steps.forEach(s => { s.status = 'completed'; s.conclusion = concl === 'success' ? 'success' : 'skipped'; });
        if (concl === 'failure') j.steps[j.steps.length - 4].conclusion = 'failure';
        j.log = [{ t: t, text: '(log downloaded from GitHub when you open it)', cls: 'dim' }];
        r.history.push({ jobId: j.id, from: t, to: end, conclusion: concl });
        t = end + rnd(3, 40) * 1000;
      }
    }
    // queued runs on app (the real situation: 5 runners, ~80 jobs waiting, oldest ~1h)
    const branchesApp = REPOS['example/app'].branches;
    const ages = [58, 51, 34, 33, 31, 12, 5];
    ages.forEach((m, i) => {
      const run = makeRun('example/app', branchesApp[i % branchesApp.length] + (i >= branchesApp.length ? '-2' : ''), t0 - m * MIN * 1000);
      for (const [n, k] of REPOS['example/app'].jobs) makeJob(run, n, k, run.createdAt);
    });
    const runH = makeRun('example/hooks', 'fix/exit-codes', t0 - 3 * MIN * 1000);
    REPOS['example/hooks'].jobs.forEach(([n, k]) => makeJob(runH, n, k, runH.createdAt));
    // put app + hooks runners mid-job
    for (const r of S.runners) {
      if (r.repo === 'acme/billing') continue;
      const j = queued(r.repo)[0];
      if (!j) continue;
      const back = rnd(30, 260);
      startJob(j, r, t0 - back * 1000);
      // fast-forward steps
      let el = back;
      for (const s of j.steps) {
        if (s.status !== 'in_progress') break;
        if (el > s.dur) { el -= s.dur; s.status = 'completed'; s.conclusion = 'success'; s.finishedAt = s.startedAt + s.dur * 1000; advanceStep(j, s.finishedAt); }
        else { for (let k = 0; k < el / 2; k++) { const l = streamLine(j, s); if (/Run|Lint|Build|Type/.test(s.name)) log(j, l.text, l.cls); } break; }
      }
    }
    // one failure in the last hour on billing
    const bfail = completed().find(j => j.repo === 'acme/billing' && j.conclusion === 'failure');
    if (!bfail) {
      const r = S.runners.find(r => r.name === 'billing-2');
      const run = makeRun('acme/billing', 'fix/tax-rounding', t0 - 22 * MIN * 1000);
      const j = makeJob(run, 'api tests', 'unit', run.createdAt);
      Object.assign(j, { status: 'completed', conclusion: 'failure', startedAt: run.createdAt + 8000, finishedAt: t0 - 17 * MIN * 1000, runner: r.name });
      j.steps.forEach((s, i) => { s.status = 'completed'; s.conclusion = i < 4 ? 'success' : i === 4 ? 'failure' : 'skipped'; });
      j.log = [{ t: j.finishedAt, text: '(log downloaded from GitHub when you open it)', cls: 'dim' }];
      r.history.push({ jobId: j.id, from: j.startedAt, to: j.finishedAt, conclusion: 'failure' });
    }
    emit('failed', 'billing · api tests failed on billing-2 after 4m 51s', completed().find(j => j.conclusion === 'failure' && j.repo === 'acme/billing').id, null);
  }

  // ---------- formatting ----------
  function fmtDur(sec) {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return sec + 's';
    const m = Math.floor(sec / 60), s = sec % 60;
    if (m < 60) return `${m}m ${String(s).padStart(2, '0')}s`;
    const h = Math.floor(m / 60);
    return `${h}h ${String(m % 60).padStart(2, '0')}m`;
  }
  function fmtShort(sec) { sec = Math.max(0, Math.round(sec)); if (sec < 60) return sec + 's'; const m = Math.floor(sec / 60); if (m < 60) return m + 'm'; return Math.floor(m / 60) + 'h' + String(m % 60).padStart(2, '0'); }
  function clock(ms) { const d = new Date(ms); return d.toTimeString().slice(0, 8); }

  seed();
  let last = performance.now();
  setInterval(() => {
    const t = performance.now(); const dt = (t - last) / 1000; last = t;
    if (!S.paused) tick(dt * SPEED);
    S.listeners.forEach(fn => fn());
  }, TICK_MS);

  Object.assign(S, { queued, running, completed, summary, stepIndex, usualFor, actions, fmtDur, fmtShort, clock,
    on(fn) { S.listeners.push(fn); fn(); } });
  window.Sim = S;
})();
