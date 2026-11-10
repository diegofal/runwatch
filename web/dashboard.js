// Runwatch — dashboard: Now / Queue / History / Runners and the job drawer.
(function () {
  'use strict';
  const { $, esc, cls, t, clock, short, color } = RW;
  const F = RW.fmtDur, FS = RW.fmtShort;
  const WIN = 3600e3, H2 = 2 * 3600e3;
  const VIEWS = ['now', 'queue', 'history', 'runners'];
  const n = v => (Number.isFinite(+v) ? +v : 0);

  let view = 'now', histFilter = 'all', confirm = null, jobOpen = null;
  const repoOff = new Set();
  const vis = x => !repoOff.has(x.repo);
  const S = () => RW.state;
  const jobAttr = j => 'data-open="' + esc(RW.jobKey(j)) + '"';
  const runAttr = (repo, runId) => 'data-repo="' + esc(repo) + '" data-id="' + esc(runId) + '"';
  const unfinished = (repo, runId) => S().active.filter(a => a.repo === repo && String(a.runId) === String(runId)).length;
  const plural = (k, w) => k + ' ' + w + (k === 1 ? '' : 's');
  const drift = () => Math.max(0, (RW.now() - t(S().at)) / 1000);

  // ---------- top ----------
  function renderTop() {
    const st = S(), s = st.summary, now = RW.now();
    const total = n(s.total), busy = n(s.busy), online = n(s.online), waiting = n(s.waiting);
    const off = Math.max(0, total - online);
    const firstQ = st.active.find(j => j.status === 'queued');
    const done = st.recent.filter(j => t(j.finishedAt) && now - t(j.finishedAt) < H2);
    const all = done.reduce((a, j) => a + RW.ran(j), 0);
    const wasted = done.filter(j => j.conclusion === 'cancelled').reduce((a, j) => a + RW.ran(j), 0);
    RW.setHTML($('#facts'), `
      <div class="fact"><div class="k">Running</div><div class="v" data-testid="fact-running">${busy}<small> / ${total}</small></div><div class="s">${Math.max(0, online - busy)} idle${off ? ' · ' + off + ' not running' : ''}</div></div>
      <div class="fact"><div class="k">Waiting</div><div class="v ${waiting ? 'wait' : ''}" data-testid="fact-waiting">${waiting}</div><div class="s">jobs not started</div></div>
      <div class="fact"><div class="k">Oldest wait</div><div class="v" data-testid="fact-oldest">${waiting ? F(n(s.oldestWaitSec) + drift()) : '—'}</div><div class="s">${waiting && firstQ ? esc(short(firstQ.repo)) : '&nbsp;'}</div></div>
      <div class="fact"><div class="k">Failed · 1 h</div><div class="v ${n(s.failedHour) ? 'bad' : ''}" data-testid="fact-failed">${n(s.failedHour)}</div><div class="s">jobs</div></div>
      <div class="fact"><div class="k">Cancelled runs · 2 h</div><div class="v">${FS(wasted)}</div><div class="s">${all > 0 ? Math.round(wasted / all * 100) + '% of runner time' : 'no runner time yet'}</div></div>`);
    $('#nNow').textContent = busy; $('#nQueue').textContent = waiting; $('#nRun').textContent = total;
    $('#nHist').textContent = st.recent.length;
    $('#version').textContent = st.version ? 'version ' + st.version : '';
    $('#conn').textContent = (RW.connected ? 'live · ' : 'not connected · last ') + clock(t(st.at));
    RW.setHTML($('#repoF'), RW.repos.map(r => `<button class="repoBtn ${repoOff.has(r) ? 'off' : ''}" data-a="repo" data-id="${esc(r)}" title="show or hide ${esc(r)}"><i style="background:${color(r)}"></i>${esc(r)}</button>`).join(''));
  }

  // ---------- NOW ----------
  function liveJob(r) {
    const j = S().active.find(a => a.status === 'in_progress' && a.runner === r.name);
    if (j) return j;
    // GitHub has not reported it yet; the runner's own files say it is busy.
    if (r.busy && r.jobName) return { partial: true, id: r.jobId, repo: r.repo, name: r.jobName, startedAt: r.jobStarted, runner: r.name, status: 'in_progress', usualSec: 0, steps: [] };
    return null;
  }
  function slots(repo) {
    return S().runners.filter(r => r.repo === repo).map(r => `<span class="slot ${r.busy ? 'busy' : r.state === 'stopped' ? 'off' : r.state === 'unknown' ? 'unk' : ''}" title="${esc(r.name)} · ${r.busy ? 'busy' : r.state === 'running' ? 'idle' : esc(r.state)}"></span>`).join('');
  }
  function renderNow() {
    const st = S(), now = RW.now(), t0 = now - WIN;
    const x = ms => Math.min(100, Math.max(0, (ms - t0) / WIN * 100));
    const waitingFor = {};
    st.summary.repos.forEach(r => { waitingFor[r.repo] = n(r.waiting); });
    const lanes = st.runners.filter(vis).map(r => {
      const segs = st.recent.filter(j => j.runner === r.name && j.repo === r.repo && RW.started(j) && t(j.finishedAt) > t0).map(j => {
        const l = x(t(j.startedAt)), w = Math.max(0, x(t(j.finishedAt)) - l), res = RW.result(j);
        return `<div class="seg ${cls(res)} ${w > 7 ? '' : 'thin'}" style="left:${l.toFixed(3)}%;width:${w.toFixed(3)}%" ${jobAttr(j)} title="${esc(j.name)} · ${esc(res)} · ${F(RW.ran(j))}">${w > 7 ? esc(j.name) : ''}</div>`;
      }).join('');
      const j = liveJob(r);
      let live = '', cur;
      if (j) {
        const el = RW.ran(j), usual = n(j.usualSec), over = usual > 0 && el > usual, p = RW.stepPos(j);
        const l = x(t(j.startedAt)), open = j.id ? jobAttr(j) : '';
        live = `<div class="seg live ${100 - l > 7 ? '' : 'thin'}" style="left:${l.toFixed(3)}%;width:${(100 - l).toFixed(3)}%" ${open}>${100 - l > 7 ? esc(j.name) : ''}</div>`;
        const bits = [];
        if (p.n) bits.push(`${p.k}/${p.n} ${esc(p.name)}`);
        bits.push(usual > 0 ? 'usual ' + FS(usual) : 'usual —');
        if (j.branch) bits.push(esc(j.branch));
        if (j.partial) bits.push('not on GitHub yet');
        cur = `<div class="cur" data-testid="lane-current" ${open}><div class="a"><b>${esc(j.name)}</b><span class="${over ? 'over' : ''}">${F(el)}</span></div>
          <div class="b">${bits.join(' · ')}</div>
          <div class="pbar ${over ? 'over' : ''}"><i style="width:${usual > 0 ? Math.min(100, el / usual * 100).toFixed(1) : 0}%"></i></div></div>`;
      } else {
        const txt = r.state === 'stopped' ? 'service stopped' : r.state === 'unknown' ? 'idle · service state unknown'
          : waitingFor[r.repo] ? 'idle' : 'idle — nothing waiting for ' + esc(short(r.repo));
        cur = `<div class="cur" data-testid="lane-current"><div class="b ${r.state === 'stopped' ? 'bad' : ''}">${txt}</div>${r.problem ? `<div class="b bad" title="${esc(r.problem)}">${esc(r.problem)}</div>` : ''}</div>`;
      }
      const dot = r.state === 'stopped' ? 'var(--bad)' : j ? 'var(--run)' : r.state === 'running' ? 'var(--ok)' : 'var(--faint)';
      return `<div class="lane" data-testid="lane" data-runner="${esc(r.name)}"><div class="who"><span class="n"><i style="background:${dot}"></i>${esc(r.name)}</span><span class="r" title="${esc(r.repo)}">${esc(r.repo)}</span></div>
        <div class="track">${segs}${live}<div class="nowline"></div></div>${cur}</div>`;
    }).join('');
    const ticks = [60, 50, 40, 30, 20, 10, 0].map((m, i) => `<span style="left:${(i / 6 * 100).toFixed(2)}%">${m ? '−' + m + 'm' : 'now'}</span>`).join('');
    const press = st.summary.repos.filter(vis).map(r => `<div class="pr"><div class="l"><span class="nm" style="color:${color(r.repo)}" title="${esc(r.repo)}">${esc(r.repo)}</span>
      <span class="q">${n(r.waiting) ? `<b>${n(r.waiting)} waiting</b> · oldest ${F(n(r.oldestWaitSec) + drift())} · ` : 'nothing waiting · '}${n(r.busy)}/${n(r.runners)} busy</span></div>
      <div class="slots">${slots(r.repo)}</div></div>`).join('');
    const ICON = { failed: '✕', cancelled: '■', rerun: '↻', queued: '＋', finished: '✓', service: '⏻' };
    const evs = st.events.filter(e => !e.repo || vis(e)).slice(0, 14).map(e => {
      const open = e.repo && e.jobId ? `data-open="${esc(e.repo + ':' + e.jobId)}"` : '';
      return `<div class="ev" ${open}><span class="t">${clock(t(e.at))}</span><span class="${cls(e.kind)}">${ICON[e.kind] || '·'}</span><span>${esc(e.text)}</span></div>`;
    }).join('');
    RW.setHTML($('#v-now'), `<h2>Now</h2><p class="sub">Each row is a runner. The bars are the jobs it ran in the last hour, and the right column is what it's running now. Click any bar to open the job.</p>
      <div class="yard"><div class="yhead"><span>runner</span><div class="ticks">${ticks}</div><span>running now</span></div>${lanes || '<div class="empty">No runners to show.</div>'}</div>
      <div class="legend"><span><i class="run"></i>running</span><span><i class="ok"></i>passed</span><span><i class="bad"></i>failed</span><span><i class="can"></i>cancelled</span><span class="r">progress bar = time so far ÷ usual time (median of past runs of the job)</span></div>
      <div class="grid2"><div class="card"><h3>Waiting per repo</h3><div class="press">${press || '<div class="empty">No repos to show.</div>'}</div></div><div class="card"><h3>What happened</h3>${evs || '<div class="empty">Nothing yet.</div>'}</div></div>`);
  }

  // ---------- QUEUE ----------
  function stopCell(repo, runId) {
    const key = 'run:' + repo + '#' + runId;
    if (confirm === key) {
      return `<span class="confirm">Stop run #${esc(runId)}? It cancels its ${plural(unfinished(repo, runId), 'unfinished job')}.</span><button class="btn stop" data-a="yes" data-testid="confirm-stop" ${runAttr(repo, runId)}>Stop run</button><button class="btn" data-a="no" data-testid="cancel-stop">Keep</button>`;
    }
    return `${RW.msg(key)}<button class="btn stop" data-a="stop" data-testid="stop-run" ${runAttr(repo, runId)} ${RW.pending(key) ? 'disabled' : ''}>■ stop run</button>`;
  }
  function renderQueue() {
    const st = S(), now = RW.now();
    const groups = new Map();
    for (const j of st.active.filter(vis)) {
      const k = j.repo + '#' + j.runId;
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(j);
    }
    const runs = [...groups.values()].map(jobs => {
      const wq = jobs.filter(j => j.status === 'queued');
      return { jobs, wq, wr: jobs.filter(j => j.status === 'in_progress'), since: Math.min(...wq.map(j => t(j.queuedAt) || now)) };
    }).filter(r => r.wq.length).sort((a, b) => a.since - b.since);
    const rows = runs.map((run, i) => {
      const j0 = run.jobs[0], w = Math.max(0, (now - run.since) / 1000);
      const heat = w > 45 * 60 ? 'hot' : w > 15 * 60 ? '' : 'cool';
      return `<tr class="runrow" data-testid="queue-run" data-run="${esc(j0.runId)}"><td class="pos">${String(i + 1).padStart(2, '0')}</td><td class="waited ${heat}">${F(w)}</td>
        <td><span class="rp" style="color:${color(j0.repo)}" title="${esc(j0.repo)}">${esc(short(j0.repo))}</span></td>
        <td><span class="br" title="${esc(j0.title || j0.branch || '')}">${esc(j0.branch || '—')}</span><div class="meta">#${esc(j0.runId)}${j0.event ? ' · ' + esc(j0.event) : ''}${j0.actor ? ' · ' + esc(j0.actor) : ''}${j0.workflow ? ' · ' + esc(j0.workflow) : ''}</div></td>
        <td class="jobs">${run.wr.length ? `<span class="pill r">${run.wr.length} running</span>` : ''}<span class="pill q">${run.wq.length} waiting</span><br>${run.wq.slice(0, 6).map(j => `<span ${jobAttr(j)}>${esc(j.name)}</span>`).join(' · ')}${run.wq.length > 6 ? ` · +${run.wq.length - 6}` : ''}</td>
        <td><div class="inl">${stopCell(j0.repo, j0.runId)}</div></td></tr>`;
    }).join('');
    const tiles = st.summary.repos.filter(vis).map(r => `<div class="tile"><div class="k" style="color:${color(r.repo)}">${esc(r.repo)}</div><div class="v">${n(r.waiting)}<small> waiting · ${n(r.busy)}/${n(r.runners)} busy</small></div><div class="s">${n(r.waiting) ? 'oldest ' + F(n(r.oldestWaitSec) + drift()) : 'nothing waiting'}</div></div>`).join('');
    RW.setHTML($('#v-queue'), `<h2>Queue</h2><p class="sub">Runs with jobs that haven't started, oldest first. Waits turn amber after 15 min and red after 45 min. A runner only takes jobs from its own repo, so the queue is shared only within a repo. Stopping cancels the whole run: GitHub cannot cancel one job.</p>
      <div class="qtiles">${tiles}</div>
      <div class="board"><table><thead><tr><th>#</th><th>Waited</th><th>Repo</th><th>Branch · run</th><th>Jobs</th><th></th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="empty">Nothing waiting.</td></tr>'}</tbody></table></div>`);
  }

  // ---------- HISTORY ----------
  function median(a) { if (!a.length) return null; const s = [...a].sort((x, y) => x - y); return s[Math.floor(s.length / 2)]; }
  const FILTERS = [['all', 'all'], ['failure', 'failed'], ['cancelled', 'cancelled'], ['success', 'passed']];
  function renderHistory() {
    const all = S().recent.filter(vis);
    const list = all.filter(j => histFilter === 'all' || j.conclusion === histFilter);
    const waits = all.map(RW.waited), runs = all.filter(RW.started).map(RW.ran);
    const c = k => all.filter(j => j.conclusion === k).length;
    const mxW = Math.max(...waits, 1), mxR = Math.max(...runs, 1);
    const mW = median(waits), mR = median(runs);
    const never = all.filter(j => j.conclusion === 'cancelled' && !RW.started(j)).length;
    const rows = list.map(j => {
      const w = RW.waited(j), ran = RW.started(j), r = RW.ran(j), res = RW.result(j), key = 'rerun:' + RW.jobKey(j);
      return `<tr ${jobAttr(j)} data-testid="history-job" data-job="${esc(j.id)}"><td class="num l">${clock(t(j.finishedAt)).slice(0, 5)}</td><td class="m" style="color:${color(j.repo)}" title="${esc(j.repo)}">${esc(short(j.repo))}</td>
        <td>${esc(j.name)}${n(j.attempt) > 1 ? ` <span class="pill">attempt ${n(j.attempt)}</span>` : ''}</td><td class="m dim">${esc(j.branch || '')}</td>
        <td class="m">${esc(j.runner || '—')}</td>
        <td class="num"><span class="wbar" style="width:${(w / mxW * 60).toFixed(1)}px"></span> ${FS(w)}</td><td class="num">${ran ? `<span class="rbar" style="width:${(r / mxR * 60).toFixed(1)}px"></span> ${FS(r)}` : '—'}</td>
        <td class="res ${cls(res)}">${esc(res)}${!ran ? ' <small>(never started)</small>' : ''}</td>
        <td>${j.conclusion !== 'success' ? `<div class="inl">${RW.msg(key)}<button class="btn go" data-a="rerun" data-testid="rerun-job" data-repo="${esc(j.repo)}" data-id="${esc(j.id)}" title="re-run this job" ${RW.pending(key) ? 'disabled' : ''}>↻</button></div>` : ''}</td></tr>`;
    }).join('');
    RW.setHTML($('#v-history'), `<h2>History</h2><p class="sub">Finished jobs. "Waited" is the time from queued to picked up by a runner. "Ran" is the time on the runner.</p>
      <div class="tiles">
        <div class="tile"><div class="k">Finished</div><div class="v">${all.length}</div><div class="s">jobs</div></div>
        <div class="tile"><div class="k">Passed</div><div class="v ok">${c('success')}</div></div>
        <div class="tile"><div class="k">Failed</div><div class="v bad">${c('failure')}</div><div class="s">${all.length ? Math.round(c('failure') / all.length * 100) + '% of finished' : ''}</div></div>
        <div class="tile"><div class="k">Cancelled</div><div class="v">${c('cancelled')}</div><div class="s">${never} never started</div></div>
        <div class="tile"><div class="k">Median wait / run</div><div class="v">${mW == null ? '—' : FS(mW)} <small>/ ${mR == null ? '—' : FS(mR)}</small></div></div>
      </div>
      <div class="filters">${FILTERS.map(([f, label]) => `<button class="btn ${histFilter === f ? 'on' : ''}" data-a="filter" data-id="${f}" data-testid="filter-${label}">${label}</button>`).join('')}</div>
      <table class="hist"><thead><tr><th>Finished</th><th>Repo</th><th>Job</th><th>Branch</th><th>Runner</th><th class="num">Waited</th><th class="num">Ran</th><th>Result</th><th></th></tr></thead><tbody>
      ${rows || '<tr><td colspan="9" class="empty">No finished jobs to show.</td></tr>'}
      </tbody></table>`);
  }

  // ---------- RUNNERS ----------
  function serviceActs(r, j) {
    if (r.managed) return `<span class="managed" data-testid="managed">${esc(r.managed)}</span>`;
    if (!r.service) return ''; // the Service row says it
    const key = 'svc:' + r.name, dis = RW.pending(key) ? 'disabled' : '', id = `data-id="${esc(r.name)}"`;
    if (confirm === key + ':stop') {
      return `<span class="confirm">Stop ${esc(r.name)}?${r.busy ? ' It is running ' + esc(j ? j.name : r.jobName || 'a job') + ': stopping the runner fails that job.' : ''} Windows will ask for admin.</span><button class="btn stop adm" data-a="svc" data-k="stop" data-testid="confirm-service" ${id}>Stop service</button><button class="btn" data-a="no">Keep</button>`;
    }
    if (confirm === key + ':start') {
      return `<span class="confirm ask">Start ${esc(r.name)}? Windows will ask for admin.</span><button class="btn go adm" data-a="svc" data-k="start" data-testid="confirm-service" ${id}>Start service</button><button class="btn" data-a="no">Not now</button>`;
    }
    const stop = `<button class="btn adm" data-a="svcAsk" data-k="stop" data-testid="service-stop" ${id} ${dis}>stop service</button>`;
    const start = `<button class="btn adm go" data-a="svcAsk" data-k="start" data-testid="service-start" ${id} ${dis}>start service</button>`;
    return (r.state === 'running' ? stop : r.state === 'stopped' ? start : start + stop) + RW.msg(key);
  }
  function renderRunners() {
    const st = S(), now = RW.now(), from = now - H2;
    const cards = st.runners.filter(vis).map(r => {
      const j = liveJob(r);
      const mine = st.recent.filter(x => x.runner === r.name && x.repo === r.repo && RW.started(x) && t(x.finishedAt) > from);
      let busyMs = mine.reduce((a, x) => a + Math.max(0, Math.min(t(x.finishedAt), now) - Math.max(t(x.startedAt), from)), 0);
      if (j && t(j.startedAt)) busyMs += Math.max(0, now - Math.max(t(j.startedAt), from));
      const state = r.state === 'stopped' ? ['off', 'stopped'] : j || r.busy ? ['busy', 'busy'] : r.state === 'running' ? ['on', 'idle'] : ['unk', 'unknown'];
      const measuring = !n(r.diagBytes) && !n(r.workBytes);
      return `<div class="rc" data-testid="runner-card" data-runner="${esc(r.name)}"><div class="h"><b>${esc(r.name)}</b><span class="rp" style="color:${color(r.repo)}" title="${esc(r.repo)}">${esc(r.repo)}</span><span class="st ${state[0]}">${state[1]}</span></div>
        <div class="kv"><span>Now</span><span>${j ? esc(j.name) + ' · ' + FS(RW.ran(j)) : '—'}</span>
        <span>Busy · last 2 h</span><span>${Math.min(100, Math.round(busyMs / H2 * 100))}% · ${plural(mine.length + (j ? 1 : 0), 'job')}</span>
        <span>Logs (_diag)</span><span>${measuring ? 'measuring…' : RW.fmtBytes(r.diagBytes)}</span><span>Checkouts (_work)</span><span>${measuring ? 'measuring…' : RW.fmtBytes(r.workBytes)}</span>
        <span>Service</span><span>${r.service ? esc(r.service) : 'not installed as a service'}</span>
        <span>Folder</span><span>${esc(r.dir || '—')}</span></div>
        ${r.problem ? `<div class="prob">${esc(r.problem)}</div>` : ''}
        <div class="acts">${serviceActs(r, j)}</div></div>`;
    }).join('');
    RW.setHTML($('#v-runners'), `<h2>Runners</h2><p class="sub">The GitHub Actions runners on this PC. Logs and checkouts grow over time, so their size is shown here.</p>
      <div class="rgrid">${cards || '<div class="empty">No runners to show.</div>'}</div>`);
  }

  // ---------- DRAWER ----------
  const MAXDOM = 6000;
  let term = null, lastJob = null;
  const splitKey = key => { const i = key.lastIndexOf(':'); return { repo: key.slice(0, i), id: key.slice(i + 1) }; };

  function openJob(key) {
    if (!key || key.lastIndexOf(':') < 1) return;
    stopStream();
    jobOpen = key; confirm = null; lastJob = null;
    term = { stream: null, lines: [], drawn: 0, ph: null, search: '', follow: true, ts: term ? term.ts : true, source: '', note: '', error: '', done: false, status: null };
    if (location.hash !== '#job=' + key) history.replaceState(null, '', '#job=' + key);
    $('#drawerHost').innerHTML = `<div class="scrim" data-a="close"></div><div class="drawer" role="dialog" aria-label="Job" data-testid="drawer">
      <div class="dh" id="dhead"></div><div class="times" id="dtimes"></div>
      <div class="dbody"><div class="steps" id="dsteps" data-testid="steps"></div>
      <div class="term"><div class="tbar"><input type="search" id="tsearch" placeholder="find in log" aria-label="find in log"><label><input type="checkbox" id="tts" ${term.ts ? 'checked' : ''}> times</label><label><input type="checkbox" id="tfol" checked> follow</label><span class="src" id="tsrc" data-testid="log-source"></span></div>
      <div class="tout ${term.ts ? '' : 'nots'}" id="tout" data-testid="terminal"></div></div></div></div>`;
    $('#tsearch').addEventListener('input', e => { term.search = e.target.value; redrawTerm(); });
    $('#tts').addEventListener('change', e => { term.ts = e.target.checked; $('#tout').classList.toggle('nots', !term.ts); });
    $('#tfol').addEventListener('change', e => { term.follow = e.target.checked; if (term.follow) { const o = $('#tout'); o.scrollTop = o.scrollHeight; } });
    $('#tout').addEventListener('scroll', e => {
      const o = e.target, atEnd = o.scrollHeight - o.scrollTop - o.clientHeight < 30;
      if (!atEnd && term.follow) { term.follow = false; $('#tfol').checked = false; }
    });
    startStream();
    render(true);
  }
  function closeJob() {
    stopStream();
    jobOpen = null; term = null; confirm = null;
    $('#drawerHost').innerHTML = '';
    if (location.hash.startsWith('#job=')) history.replaceState(null, '', '#' + view);
  }
  function stopStream() { if (term && term.stream) { term.stream.stop(); term.stream = null; } }
  function startStream() {
    const { repo, id } = splitKey(jobOpen), mine = term;
    mine.done = false;
    mine.stream = RW.followLog(repo, id, c => {
      if (term !== mine) return;
      mine.source = c.source; mine.note = c.note; mine.error = c.error; mine.done = c.done;
      if (c.reset) { mine.lines = []; redrawTerm(); }
      if (c.lines.length) { mine.lines.push(...c.lines); appendTerm(c.lines); }
      renderTermBar();
    });
  }
  function hl(text, q) {
    if (!q) return esc(text);
    const lo = text.toLowerCase(), ql = q.toLowerCase();
    let out = '', i = 0, k;
    while ((k = lo.indexOf(ql, i)) >= 0) { out += esc(text.slice(i, k)) + '<mark>' + esc(text.slice(k, k + q.length)) + '</mark>'; i = k + q.length; }
    return out + esc(text.slice(i));
  }
  const lineHTML = l => `<div class="ln ${l.cls}" data-no="${l.no}"><span class="no">${l.no}</span><span class="ts">${l.at ? clock(l.at) : ''}</span><span class="tx">${hl(l.text, term.search)}</span></div>`;
  const matches = l => !term.search || l.cls === 'group' || l.text.toLowerCase().includes(term.search.toLowerCase());
  const noteLine = text => `<div class="ln dim"><span class="no"></span><span class="ts"></span><span class="tx">${esc(text)}</span></div>`;
  function redrawTerm() {
    const out = $('#tout'); if (!out) return;
    const list = term.lines.filter(matches), skip = Math.max(0, list.length - MAXDOM);
    out.innerHTML = (skip ? noteLine(skip + ' earlier lines are not drawn here; "copy log" has all of them.') : '') + list.slice(skip).map(lineHTML).join('');
    term.drawn = list.length; term.ph = null;
    if (!term.drawn) renderPlaceholder(); else if (term.follow) out.scrollTop = out.scrollHeight;
  }
  function appendTerm(lines) {
    const out = $('#tout'); if (!out) return;
    const add = lines.filter(matches);
    if (!add.length) return;
    if (!term.drawn) { out.innerHTML = ''; term.ph = null; }
    out.insertAdjacentHTML('beforeend', add.map(lineHTML).join(''));
    term.drawn += add.length;
    if (term.follow) out.scrollTop = out.scrollHeight;
  }
  // What the terminal says while it has no line to show.
  function renderPlaceholder() {
    const out = $('#tout'); if (!out || !term || term.drawn) return;
    const j = lastJob;
    let text;
    if (term.lines.length) text = 'No line matches "' + term.search + '".';
    else if (j && j.status === 'queued') {
      const q = S().active.filter(a => a.status === 'queued' && a.repo === j.repo);
      const pos = q.findIndex(a => a.id === j.id) + 1, rs = S().runners.filter(r => r.repo === j.repo).length;
      text = 'Waiting for a runner.' + (pos ? ` ${pos} in line for ${short(j.repo)} (${plural(rs, 'runner')}).` : '');
    } else if (term.error) text = 'Could not read the output: ' + term.error;
    else if (term.note) text = term.note;
    else if (term.done) text = 'This job has no output.';
    else text = term.source ? 'No output yet.' : 'Reading the output…';
    const h = noteLine(text);
    if (term.ph !== h) { out.innerHTML = h; term.ph = h; }
  }
  function renderTermBar() {
    if (!term) return;
    const what = { live: "live · this PC's runner files", github: 'from GitHub · the whole log', none: 'no output' }[term.source] || '';
    const bits = [what, term.lines.length ? plural(term.lines.length, 'line') : ''].filter(Boolean).join(' · ');
    RW.setHTML($('#tsrc'), esc(bits) + (term.note ? ` <span class="note">${esc(term.note)}</span>` : '') + (term.error ? ` <span class="err">${esc(term.error)}</span>` : ''));
    renderPlaceholder();
  }
  function copyLog() {
    const mine = term;
    RW.run('copy', async () => {
      if (!mine.lines.length) throw new Error('There is no output to copy.');
      if (!navigator.clipboard) throw new Error('This browser does not allow copying from here.');
      await navigator.clipboard.writeText(mine.lines.map(l => l.text).join('\n'));
      return mine.lines.length;
    }, k => 'copied ' + plural(k, 'line'));
  }
  function gotoStep(i) {
    const s = lastJob && lastJob.steps && lastJob.steps[i], from = s && t(s.startedAt);
    if (!from || !term) return;
    const line = term.lines.find(l => l.at >= from && matches(l));
    const el = line && $('#tout').querySelector('[data-no="' + line.no + '"]');
    if (!el) return;
    term.follow = false; $('#tfol').checked = false;
    el.scrollIntoView({ block: 'start' });
    el.classList.add('flash');
    setTimeout(() => el.classList.remove('flash'), 1200);
  }

  function renderDrawer() {
    if (!jobOpen || !term) return;
    const { repo, id } = splitKey(jobOpen);
    const j = RW.findJob(jobOpen) || lastJob; // keeps showing a job that left the list
    lastJob = j;
    const closeBtn = '<span class="sp"></span><button class="btn" data-a="close" data-testid="drawer-close" title="close (Esc)">✕</button>';
    if (!j) {
      RW.setHTML($('#dhead'), `<div><h3>Job ${esc(id)} <span class="stat" data-testid="job-status">not in the list</span></h3><div class="meta">${esc(repo)} · Runwatch has no active or recent job with this number.</div></div><div class="dacts">${closeBtn}</div>`);
      RW.setHTML($('#dtimes'), ''); RW.setHTML($('#dsteps'), '<div class="none">No steps.</div>');
      renderTermBar();
      return;
    }
    if (term.status && term.status !== j.status && term.done) { stopStream(); term.lines = []; redrawTerm(); startStream(); }
    term.status = j.status;
    const res = RW.result(j), live = j.status !== 'completed';
    const left = unfinished(j.repo, j.runId), runLive = left > 0;
    const anyBad = !live && (j.conclusion !== 'success' || S().recent.some(x => x.repo === j.repo && x.runId === j.runId && x.conclusion !== 'success'));
    const kRun = 'run:' + j.repo + '#' + j.runId, kJob = 'rerun:' + RW.jobKey(j), kFail = 'rerunf:' + j.repo + '#' + j.runId;
    const url = RW.safeUrl(j.url);
    const dis = k => (RW.pending(k) ? 'disabled' : '');
    const meta = [esc(j.repo), esc(j.branch || '—') + (j.sha ? ' @ ' + esc(String(j.sha).slice(0, 7)) : ''), 'run #' + esc(j.runId) + (n(j.attempt) > 1 ? ' · attempt ' + n(j.attempt) : ''),
      esc(j.event || ''), esc(j.actor || ''), esc(j.workflow || ''), j.runner ? 'on ' + esc(j.runner) : ''].filter(Boolean).join(' · ');
    RW.setHTML($('#dhead'), `<div><h3>${esc(j.name)} <span class="stat ${cls(res)}" data-testid="job-status">${esc(String(res).replace('_', ' '))}</span></h3>
        <div class="meta">${meta}</div>${j.title ? `<div class="meta">${esc(j.title)}</div>` : ''}</div>
      <div class="dacts">
        ${runLive ? (confirm === kRun
          ? `<span class="confirm">Stop run #${esc(j.runId)}? It cancels its ${plural(left, 'unfinished job')}.</span><button class="btn stop" data-a="yes" data-testid="drawer-confirm-stop" ${runAttr(j.repo, j.runId)}>Stop run</button><button class="btn" data-a="no">Keep</button>`
          : `<button class="btn stop" data-a="stop" data-testid="drawer-stop" ${runAttr(j.repo, j.runId)} ${dis(kRun)}>■ stop run</button>`) : ''}${RW.msg(kRun)}
        ${!live ? `<button class="btn go" data-a="rerun" data-testid="drawer-rerun" data-repo="${esc(j.repo)}" data-id="${esc(j.id)}" ${dis(kJob)}>↻ re-run this job</button>${RW.msg(kJob)}` : ''}
        ${!runLive && anyBad ? `<button class="btn go" data-a="rerunFailed" data-testid="drawer-rerun-failed" ${runAttr(j.repo, j.runId)} ${dis(kFail)}>↻ re-run failed jobs</button>${RW.msg(kFail)}` : ''}
        <button class="btn" data-a="copy">copy log</button>${RW.msg('copy')}
        ${url ? `<a class="btn" href="${esc(url)}" target="_blank" rel="noopener">open on GitHub ↗</a>` : ''}${closeBtn}</div>`);
    const waited = RW.waited(j), ran = RW.started(j), p = RW.stepPos(j), usual = n(j.usualSec);
    RW.setHTML($('#dtimes'), `<div><div class="k">Queued</div><div class="v">${clock(t(j.queuedAt))}</div></div>
      <div><div class="k">Waited</div><div class="v ${waited > 900 ? 'wait' : ''}">${F(waited)}</div></div>
      <div><div class="k">${live ? 'Running for' : 'Ran'}</div><div class="v">${ran ? F(RW.ran(j)) : '—'}</div></div>
      <div><div class="k">Usual</div><div class="v">${usual > 0 ? F(usual) : '—'}</div></div>
      <div><div class="k">Step</div><div class="v">${p.k ? p.k + ' / ' + p.n : '—'}</div></div>`);
    const now = RW.now();
    RW.setHTML($('#dsteps'), (j.steps || []).map((s, i) => {
      const ic = s.status === 'in_progress' ? '<span class="spin"></span>' : s.status === 'pending' ? '○'
        : s.conclusion === 'success' ? '<span class="ic ok">✓</span>' : s.conclusion === 'failure' ? '<span class="ic bad">✕</span>'
        : s.conclusion === 'cancelled' ? '<span class="ic bad">■</span>' : '<span class="ic skip">⊘</span>';
      const a = t(s.startedAt), b = t(s.finishedAt);
      const d = s.status === 'in_progress' && a ? F((now - a) / 1000) : a && b ? F((b - a) / 1000) : '';
      return `<div class="step ${cls(s.status)}" data-step="${i}" title="${esc(s.name)}${s.conclusion ? ' · ' + esc(s.conclusion) : ''}"><span class="ic">${ic}</span><span>${esc(s.name)}</span><span class="d">${d}</span></div>`;
    }).join('') || `<div class="none">${j.status === 'queued' ? 'No steps yet.' : 'No steps reported.'}</div>`);
    renderTermBar();
  }

  // ---------- wiring ----------
  const RENDER = { now: renderNow, queue: renderQueue, history: renderHistory, runners: renderRunners };
  function render(force) {
    if (!S()) return;
    if (RW.mouseDown && !force) return;
    RW.beginPass();
    renderTop();
    RENDER[view]();
    renderDrawer();
    RW.endPass();
  }
  function setView(v) {
    view = v;
    document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('on', a.dataset.v === v));
    VIEWS.forEach(k => {
      const el = $('#v-' + k);
      el.classList.toggle('hide', k !== v);
      if (k !== v) { el.innerHTML = ''; el.__html = null; } // nothing stale stays behind a hidden tab
    });
    render(true);
  }
  function route() {
    const h = location.hash.slice(1);
    if (h.startsWith('job=')) {
      let key = h.slice(4);
      try { key = decodeURIComponent(key); } catch (_) { /* keep it as typed */ }
      setView('now');
      if (key !== jobOpen) openJob(key);
      return;
    }
    if (jobOpen) closeJob();
    setView(VIEWS.includes(h) ? h : 'now');
  }
  addEventListener('hashchange', route);

  document.addEventListener('click', e => {
    const b = e.target.closest('[data-a]');
    if (b) {
      const d = b.dataset, runKey = 'run:' + d.repo + '#' + d.id;
      switch (d.a) {
        case 'dismiss': return;
        case 'close': closeJob(); return;
        case 'stop': confirm = runKey; break;
        case 'no': confirm = null; break;
        case 'yes': confirm = null; RW.run(runKey, () => RW.post('/api/cancel', { repo: d.repo, runId: +d.id }), r => 'stop sent · ' + plural(n(r.jobs), 'job')); break;
        case 'rerun': RW.run('rerun:' + d.repo + ':' + d.id, () => RW.post('/api/rerun-job', { repo: d.repo, jobId: +d.id }), 're-run requested'); break;
        case 'rerunFailed': RW.run('rerunf:' + d.repo + '#' + d.id, () => RW.post('/api/rerun-failed', { repo: d.repo, runId: +d.id }), 're-run requested'); break;
        case 'svcAsk': confirm = 'svc:' + d.id + ':' + d.k; break;
        case 'svc': confirm = null; RW.run('svc:' + d.id, () => RW.post('/api/service', { runner: d.id, action: d.k }), d.k + ' requested'); break;
        case 'repo': if (repoOff.has(d.id)) repoOff.delete(d.id); else repoOff.add(d.id); break;
        case 'filter': histFilter = d.id; break;
        case 'copy': copyLog(); break;
        default: return;
      }
      render(true);
      return;
    }
    const s = e.target.closest('.step[data-step]');
    if (s) { gotoStep(+s.dataset.step); return; }
    const sel = getSelection();
    if (sel && !sel.isCollapsed && String(sel).length) return; // a drag to select text is not a click
    const j = e.target.closest('[data-open]');
    if (j) openJob(j.dataset.open);
  });
  addEventListener('keydown', e => {
    if (e.key !== 'Escape') return;
    if (confirm) { confirm = null; render(true); } else if (jobOpen) closeJob();
  });

  let routed = false;
  RW.onChange(() => {
    if (!routed && S()) { routed = true; route(); return; }
    render(false);
  });
  RW.start();
})();
