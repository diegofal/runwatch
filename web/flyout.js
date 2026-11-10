// Runwatch — tray flyout: three figures, a row per repo, the running jobs
// with a mini terminal, and the failures of the last hour.
(function () {
  'use strict';
  const { $, esc, t, clock, short, color } = RW;
  const F = RW.fmtDur, FS = RW.fmtShort;
  const n = v => (Number.isFinite(+v) ? +v : 0);
  const S = () => RW.state;
  const plural = (k, w) => k + ' ' + w + (k === 1 ? '' : 's');
  const MINI = 8;

  let confirm = null;  // key of the job whose run was asked to stop
  let mini = null;     // {key, stream, lines, source, note, error}

  function closeMini() { if (mini && mini.stream) mini.stream.stop(); mini = null; }
  function toggleMini(key) {
    if (mini && mini.key === key) { closeMini(); return; }
    closeMini();
    const i = key.lastIndexOf(':'), mine = mini = { key, stream: null, lines: [], source: '', note: '', error: '' };
    mine.stream = RW.followLog(key.slice(0, i), key.slice(i + 1), c => {
      if (mini !== mine) return;
      if (c.reset) mine.lines = [];
      mine.lines = mine.lines.concat(c.lines).slice(-MINI);
      mine.source = c.source; mine.note = c.note; mine.error = c.error;
      render(false);
    });
  }

  function running() {
    const st = S();
    const jobs = st.active.filter(j => j.status === 'in_progress');
    // Busy by the runner's own files, but GitHub has not reported the job yet.
    for (const r of st.runners) {
      if (r.busy && r.jobName && !jobs.some(j => j.runner === r.name)) {
        jobs.push({ partial: true, id: r.jobId, repo: r.repo, name: r.jobName, startedAt: r.jobStarted, runner: r.name, status: 'in_progress', usualSec: 0, steps: [] });
      }
    }
    return jobs.sort((a, b) => String(a.runner || '').localeCompare(String(b.runner || ''), undefined, { numeric: true }));
  }

  function jobRow(j) {
    const st = S(), key = RW.jobKey(j), runKey = j.repo + '#' + j.runId;
    const el = RW.ran(j), usual = n(j.usualSec), over = usual > 0 && el > usual, p = RW.stepPos(j);
    const open = mini && mini.key === key;
    let extra = '';
    if (!j.partial) {
      const kRun = 'run:' + runKey;
      if (confirm === key) {
        const left = st.active.filter(a => a.repo === j.repo && a.runId === j.runId).length;
        extra += `<div class="confirm"><span>Stop run #${esc(j.runId)}? It cancels its ${plural(left, 'unfinished job')}.</span><button class="btn stop" data-a="yes" data-testid="fly-confirm-stop" data-repo="${esc(j.repo)}" data-id="${esc(j.runId)}">Stop</button><button class="btn" data-a="no" data-testid="fly-cancel-stop">Keep</button></div>`;
      }
      const m = RW.msg(kRun);
      if (m) extra += `<div class="rowmsg">${m}</div>`;
      if (open) {
        const lines = mini.lines.map(l => `<div class="${l.cls}">${l.cls === 'group' ? '▸ ' : ''}${esc(l.text)}</div>`).join('');
        const empty = mini.error ? 'Could not read the output: ' + mini.error : mini.note || (mini.source ? 'No output yet.' : 'Reading the output…');
        const src = { live: "live · this PC's runner files", github: 'from GitHub', none: 'no output' }[mini.source] || '';
        extra += `<div class="mini" data-testid="fly-terminal">${lines || `<div class="dim">${esc(empty)}</div>`}</div>
          <div class="minifoot"><span>${esc(src)}${lines && mini.error ? ' · ' + esc(mini.error) : ''}</span><a href="/#job=${esc(key)}" data-a="open" data-link="#job=${esc(key)}">full terminal ↗</a></div>`;
      }
    }
    return `<div class="job" ${j.partial ? '' : `data-testid="fly-job" data-job="${esc(j.id)}" data-key="${esc(key)}"`} title="${esc(j.repo)}">
      <div class="l1"><span class="rn">${esc(j.runner)}</span><span class="nm">${esc(j.name)}</span><span class="el ${over ? 'over' : ''}">${F(el)}</span></div>
      <div class="l2"><span class="br">${esc(j.branch || (j.partial ? 'not on GitHub yet' : ''))}</span><span class="stp">${p.n ? `· ${p.k}/${p.n} ${esc(p.name)}` : ''}</span><span class="us">${usual > 0 ? 'usual ' + FS(usual) : 'usual —'}</span></div>
      <div class="bar ${over ? 'over' : ''}"><i style="width:${usual > 0 ? Math.min(100, el / usual * 100).toFixed(1) : 0}%"></i></div>
      ${j.partial ? '' : `<div class="acts"><button class="btn" data-a="term" data-id="${esc(key)}">${open ? '▾' : '▸'} terminal</button><button class="btn stop" data-a="stop" data-testid="fly-stop" data-repo="${esc(j.repo)}" data-id="${esc(j.runId)}" ${RW.pending('run:' + runKey) ? 'disabled' : ''}>■ stop</button></div>`}
      ${extra}</div>`;
  }

  function render(force) {
    const st = S();
    if (!st) return;
    if (RW.mouseDown && !force) return;
    RW.beginPass();
    const s = st.summary, now = RW.now(), drift = Math.max(0, (now - t(st.at)) / 1000);
    const waiting = n(s.waiting);
    RW.setHTML($('#figs'), `<div class="fig"><div class="k">Running</div><div class="v" data-testid="fly-running">${n(s.busy)}<small>/${n(s.total)}</small></div></div>
      <div class="fig"><div class="k">Waiting</div><div class="v ${waiting ? 'wait' : ''}" data-testid="fly-waiting">${waiting}</div></div>
      <div class="fig"><div class="k">Oldest wait</div><div class="v" data-testid="fly-oldest">${waiting ? FS(n(s.oldestWaitSec) + drift) : '—'}</div></div>`);
    RW.setHTML($('#repos'), s.repos.map(r => {
      const slots = st.runners.filter(x => x.repo === r.repo).map(x => `<span class="slot ${x.busy ? 'busy' : x.state === 'stopped' ? 'off' : x.state === 'unknown' ? 'unk' : ''}" title="${esc(x.name)} · ${x.busy ? 'busy' : x.state === 'running' ? 'idle' : esc(x.state)}"></span>`).join('');
      return `<div class="repo" data-testid="fly-repo" data-repo="${esc(r.repo)}" title="${esc(r.repo)}"><span class="nm" style="color:${color(r.repo)}">${esc(short(r.repo))}</span><span class="slots">${slots}</span>
        <span class="q">${n(r.waiting) ? `<b>${n(r.waiting)} waiting</b> · oldest ${FS(n(r.oldestWaitSec) + drift)}` : n(r.busy) ? 'nothing waiting' : 'idle'}</span></div>`;
    }).join(''));

    const run = running();
    if (mini && !run.some(j => !j.partial && RW.jobKey(j) === mini.key)) closeMini();
    if (confirm && !run.some(j => !j.partial && RW.jobKey(j) === confirm)) confirm = null;
    const failed = st.recent.filter(j => j.conclusion === 'failure' && t(j.finishedAt) && now - t(j.finishedAt) < 3600e3);
    const fails = failed.slice(0, 4).map(j => {
      const key = RW.jobKey(j), kJob = 'rerun:' + key, m = RW.msg(kJob);
      return `<div class="fail" data-testid="fly-failed" data-job="${esc(j.id)}" data-key="${esc(key)}" title="${esc(j.repo)} · open in the dashboard"><span class="x0">✕</span><span class="t">${esc(short(j.repo))} · ${esc(j.name)}${j.runner ? ` <span>on ${esc(j.runner)}</span>` : ''}</span><span class="ago">${FS((now - t(j.finishedAt)) / 1000)} ago</span><button class="btn go" data-a="rerun" data-testid="fly-rerun" data-repo="${esc(j.repo)}" data-id="${esc(j.id)}" ${RW.pending(kJob) ? 'disabled' : ''}>↻ re-run</button></div>${m ? `<div class="failmsg">${m}</div>` : ''}`;
    }).join('');
    RW.setHTML($('#list'), `<div class="sect"><span>Running now</span><span>step · time / usual</span></div>
      ${run.map(jobRow).join('') || '<div class="none">Nothing is running.</div>'}
      ${failed.length ? `<div class="sect"><span>Failed · last hour</span><span>${failed.length > 4 ? '4 of ' + failed.length : ''}</span></div>${fails}` : ''}`);
    RW.setHTML($('#updated'), RW.msg('open') || esc((RW.connected ? 'Updated ' : 'Not current · last ') + clock(t(st.at)) + ' · from GitHub + this PC'));
    RW.endPass();
  }

  document.addEventListener('click', e => {
    const b = e.target.closest('[data-a]');
    if (b) {
      const d = b.dataset;
      switch (d.a) {
        case 'dismiss': return;
        case 'open': e.preventDefault(); RW.openDashboard(d.link || ''); break;
        case 'term': toggleMini(d.id); break;
        case 'stop': confirm = b.closest('.job').dataset.key; break;
        case 'no': confirm = null; break;
        case 'yes': confirm = null; RW.run('run:' + d.repo + '#' + d.id, () => RW.post('/api/cancel', { repo: d.repo, runId: +d.id }), r => 'stop sent · ' + plural(n(r.jobs), 'job')); break;
        case 'rerun': RW.run('rerun:' + d.repo + ':' + d.id, () => RW.post('/api/rerun-job', { repo: d.repo, jobId: +d.id }), 're-run requested'); break;
        default: return;
      }
      render(true);
      return;
    }
    if (e.target.closest('.mini,.minifoot,.confirm,.rowmsg,.failmsg')) return;
    const f = e.target.closest('.fail[data-key]');
    if (f) { RW.openDashboard('#job=' + f.dataset.key); return; }
    const row = e.target.closest('.job[data-key]');
    if (row) { toggleMini(row.dataset.key); render(true); }
  });
  addEventListener('keydown', e => { if (e.key === 'Escape' && confirm) { confirm = null; render(true); } });

  RW.onChange(() => render(false));
  RW.start();
})();
