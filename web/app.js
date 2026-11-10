// Runwatch — shared helpers for the dashboard and the tray flyout:
// API client, the state stream, clock, formatting, log lines, banners.
(function () {
  'use strict';
  const RW = window.RW = {};
  const $ = RW.$ = s => document.querySelector(s);

  // ---------- escaping ----------
  const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  const esc = RW.esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ESC[c]);
  // A value used as a CSS class: letters only.
  RW.cls = s => String(s == null ? '' : s).replace(/[^a-z_]/gi, '');

  // ---------- UI errors (kept visible: a small banner) ----------
  const uiErrors = [];
  function noteError(msg) {
    msg = String(msg);
    if (uiErrors.includes(msg) || uiErrors.length >= 6) return;
    uiErrors.push(msg);
    const el = $('#errors');
    if (!el) return;
    el.hidden = false;
    el.innerHTML = '<b>UI error</b> ' + uiErrors.map(esc).join(' · ');
  }
  RW.noteError = noteError;
  addEventListener('error', e => noteError((e.message || 'error') + (e.filename ? ' (' + e.filename.split('/').pop() + ':' + e.lineno + ')' : '')));
  addEventListener('unhandledrejection', e => noteError('unhandled: ' + (e.reason && e.reason.message ? e.reason.message : e.reason)));
  document.addEventListener('securitypolicyviolation', e => noteError('blocked by the page policy: ' + e.violatedDirective + ' ' + e.blockedURI));

  // ---------- time ----------
  let offset = 0; // server clock − this clock, in ms
  RW.now = () => Date.now() + offset;
  // RFC 3339 → ms; 0 when absent or not a real time.
  const t = RW.t = s => {
    if (!s) return 0;
    const ms = Date.parse(String(s).replace(/(\.\d{3})\d+/, '$1'));
    return Number.isFinite(ms) && ms > 946684800000 ? ms : 0;
  };
  const pad = n => String(n).padStart(2, '0');
  RW.clock = ms => { if (!ms) return '—'; const d = new Date(ms); return pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds()); };
  const num = v => (Number.isFinite(+v) ? +v : 0);
  RW.fmtDur = sec => {
    sec = Math.max(0, Math.round(num(sec)));
    if (sec < 60) return sec + 's';
    const m = Math.floor(sec / 60), s = sec % 60;
    if (m < 60) return m + 'm ' + pad(s) + 's';
    return Math.floor(m / 60) + 'h ' + pad(m % 60) + 'm';
  };
  RW.fmtShort = sec => {
    sec = Math.max(0, Math.round(num(sec)));
    if (sec < 60) return sec + 's';
    const m = Math.floor(sec / 60);
    if (m < 60) return m + 'm';
    return Math.floor(m / 60) + 'h' + pad(m % 60);
  };
  RW.fmtBytes = b => {
    b = Math.max(0, num(b));
    if (b < 1024) return b + ' B';
    if (b < 1048576) return (b / 1024).toFixed(0) + ' KB';
    if (b < 1073741824) return (b / 1048576).toFixed(b < 10485760 ? 1 : 0) + ' MB';
    return (b / 1073741824).toFixed(1) + ' GB';
  };

  // ---------- jobs ----------
  // A job started only if it has both a runner and a start time.
  const started = RW.started = j => !!(j && j.runner && t(j.startedAt));
  RW.waited = j => {
    const q = t(j.queuedAt); if (!q) return 0;
    const end = started(j) ? t(j.startedAt) : j.status === 'completed' ? (t(j.finishedAt) || q) : RW.now();
    return Math.max(0, (end - q) / 1000);
  };
  RW.ran = j => {
    if (!started(j)) return 0;
    const end = j.status === 'completed' ? (t(j.finishedAt) || t(j.startedAt)) : RW.now();
    return Math.max(0, (end - t(j.startedAt)) / 1000);
  };
  // Where the job is in its steps: {k, n, name}; k is 0 when unknown.
  RW.stepPos = j => {
    const steps = (j && j.steps) || [], n = steps.length;
    let i = steps.findIndex(s => s.status === 'in_progress');
    if (i < 0) for (let x = n - 1; x >= 0; x--) if (steps[x].status === 'completed' && steps[x].conclusion !== 'skipped') { i = x; break; }
    return { k: i < 0 ? 0 : i + 1, n, name: i < 0 ? '' : steps[i].name || '' };
  };
  RW.result = j => (j.status === 'completed' ? (j.conclusion || 'completed') : j.status);
  RW.jobKey = j => j.repo + ':' + j.id;
  RW.short = repo => { const s = String(repo || ''); return s.slice(s.lastIndexOf('/') + 1); };
  RW.safeUrl = u => (/^https:\/\//.test(String(u || '')) ? String(u) : '');

  const PALETTE = ['#ffcb6b', '#5ad1ff', '#c792ea', '#7bd88f', '#ff9e64', '#f78fb3', '#7aa2f7', '#9ece6a'];
  RW.repos = [];
  RW.color = repo => { const i = RW.repos.indexOf(repo); return i < 0 ? '#8b939b' : PALETTE[i % PALETTE.length]; };
  RW.jobs = new Map();
  RW.findJob = key => RW.jobs.get(key) || null;

  // ---------- API ----------
  async function call(path, init) {
    let res;
    try { res = await fetch(path, init); }
    catch (e) { throw new Error('Runwatch did not answer (' + e.message + ')'); }
    let data = null;
    try { data = await res.json(); } catch (_) { /* not JSON */ }
    if (!res.ok) throw new Error(data && data.error ? data.error : 'Runwatch answered ' + res.status);
    return data || {};
  }
  RW.get = path => call(path, { cache: 'no-store' });
  RW.post = (path, body) => call(path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Runwatch': '1' }, body: JSON.stringify(body) });

  // ---------- state stream ----------
  RW.state = null;
  RW.connected = false;
  const listeners = [];
  RW.onChange = fn => listeners.push(fn);
  function emit() { for (const fn of listeners) { try { fn(); } catch (e) { noteError(e.message || e); } } }

  let failed = false; // a read failed and none has worked since
  function setConnected(on) {
    const was = RW.connected;
    RW.connected = on; failed = !on;
    document.body.classList.toggle('stale', !on);
    renderBanners();
    if (was !== on) emit();
  }
  function accept(s) {
    if (!s || typeof s !== 'object') return;
    for (const k of ['runners', 'active', 'recent', 'problems', 'events']) if (!Array.isArray(s[k])) s[k] = [];
    s.summary = s.summary || {};
    if (!Array.isArray(s.summary.repos)) s.summary.repos = [];
    const at = t(s.at);
    if (at) offset = at - Date.now();
    const repos = new Set();
    s.summary.repos.forEach(r => repos.add(r.repo));
    s.runners.forEach(r => repos.add(r.repo));
    RW.jobs = new Map();
    for (const j of s.recent) { repos.add(j.repo); RW.jobs.set(RW.jobKey(j), j); }
    for (const j of s.active) { repos.add(j.repo); RW.jobs.set(RW.jobKey(j), j); }
    repos.delete(undefined); repos.delete('');
    RW.repos = [...repos].sort();
    RW.state = s;
    if (!RW.connected) setConnected(true); else { renderBanners(); emit(); }
  }
  // The banners exist only while there is something to say.
  function renderBanners() {
    const b = $('#bTop'); if (!b) return;
    const p = RW.state ? RW.state.problems : [];
    setHTML(b, (failed ? '<div class="banner bad" data-testid="disconnected">Not connected to Runwatch — numbers are not current</div>' : '')
      + (p.length ? '<div class="banner bad" data-testid="problems">' + p.map(x => '<div><b>' + esc(x.source) + '</b> ' + esc(x.text)
        + (t(x.since) ? ' <span class="since">since ' + RW.clock(t(x.since)) + '</span>' : '') + '</div>').join('') + '</div>' : ''));
  }

  let es = null;
  function connect() {
    if (es) es.close();
    es = new EventSource('/api/events');
    es.addEventListener('state', e => {
      let s = null;
      try { s = JSON.parse(e.data); } catch (err) { noteError('bad state from Runwatch: ' + err.message); return; }
      accept(s);
    });
    es.onerror = () => {
      setConnected(false);
      // The browser retries by itself unless the stream was refused.
      if (es.readyState === EventSource.CLOSED) setTimeout(connect, 3000);
    };
  }
  RW.start = () => {
    RW.get('/api/state').then(s => { if (!RW.state) accept(s); }).catch(() => { if (!RW.state) setConnected(false); });
    connect();
    setInterval(() => { expireMsgs(); if (RW.connected) emit(); }, 1000);
  };

  // ---------- re-rendering without losing what the user is doing ----------
  RW.mouseDown = false;
  addEventListener('mousedown', () => { RW.mouseDown = true; }, true);
  addEventListener('mouseup', () => setTimeout(() => { RW.mouseDown = false; }, 0), true);
  addEventListener('blur', () => { RW.mouseDown = false; });

  // Replaces el's content unless it is unchanged or holds a text selection.
  // Scroll position and keyboard focus survive.
  function setHTML(el, html) {
    if (!el) return false;
    if (el.__html === html) return false;
    const sel = getSelection();
    if (sel && !sel.isCollapsed && String(sel).length && sel.containsNode(el, true)) return false;
    let fk = null;
    const a = document.activeElement;
    if (a && a !== el && el.contains(a) && a.dataset && a.dataset.a) fk = focusKey(a);
    const top = el.scrollTop;
    el.innerHTML = html;
    el.__html = html;
    if (top) el.scrollTop = top;
    if (fk) for (const b of el.querySelectorAll('[data-a]')) if (focusKey(b) === fk) { b.focus({ preventScroll: true }); break; }
    return true;
  }
  const focusKey = b => [b.dataset.a, b.dataset.repo, b.dataset.id, b.dataset.k].join('|');
  RW.setHTML = setHTML;

  // ---------- actions: pending, done and failed, shown next to the button ----------
  const pending = new Set(), msgs = new Map();
  RW.pending = key => pending.has(key);
  RW.run = async (key, fn, okText) => {
    if (pending.has(key)) return undefined;
    pending.add(key); msgs.delete(key); emit();
    try {
      const r = await fn();
      if (okText) msgs.set(key, { ok: true, text: typeof okText === 'function' ? okText(r) : okText, until: Date.now() + 8000 });
      return r;
    } catch (e) {
      msgs.set(key, { ok: false, text: e.message || String(e) });
      return undefined;
    } finally { pending.delete(key); emit(); }
  };
  function expireMsgs() { for (const [k, m] of msgs) if (m.ok && m.until < Date.now()) msgs.delete(k); }
  // The message for an action, to place next to its button.
  RW.msg = key => {
    const m = msgs.get(key);
    if (pending.has(key)) return '<span class="amsg">sending…</span>';
    if (!m) return '';
    m.inline = true;
    return m.ok ? '<span class="amsg ok">' + esc(m.text) + '</span>'
      : '<span class="amsg err" data-testid="action-error">' + esc(m.text) + ' <button class="x" data-a="dismiss" data-id="' + esc(key) + '" title="dismiss">✕</button></span>';
  };
  // Errors that no button on screen shows go to the bar at the top.
  RW.beginPass = () => { for (const m of msgs.values()) m.inline = false; };
  RW.endPass = () => {
    const b = $('#bAction'); if (!b) return;
    const loose = [...msgs].filter(([, m]) => !m.ok && !m.inline);
    setHTML(b, loose.length ? '<div class="banner act">' + loose.map(([k, m]) => '<div data-testid="action-error">' + esc(m.text)
      + ' <button class="x" data-a="dismiss" data-id="' + esc(k) + '" title="dismiss">✕</button></div>').join('') + '</div>' : '');
  };
  document.addEventListener('click', e => {
    const b = e.target.closest && e.target.closest('[data-a="dismiss"]');
    if (!b) return;
    msgs.delete(b.dataset.id);
    e.stopPropagation();
    emit();
  }, true);

  // ---------- log lines ----------
  const ANSI = /\x1b\[[0-9;?]*[ -\/]*[@-~]/g;
  // One line of job output → {cls, text}, or null for a line that is not shown.
  RW.logLine = raw => {
    let text = String(raw == null ? '' : raw).replace(ANSI, '').replace(/\r$/, '').replace(/^\uFEFF/, '');
    if (text.startsWith('##[endgroup]')) return null;
    const m = /^##\[(group|section|error|warning|notice|debug|command)\]/.exec(text);
    if (m) {
      text = text.slice(m[0].length);
      const cls = { group: 'group', section: 'group', error: 'err', warning: 'warn', notice: '', debug: 'dim', command: 'cmd' }[m[1]];
      return { cls, text };
    }
    if (text.startsWith('[command]')) return { cls: 'cmd', text: text.slice(9) };
    return { cls: '', text };
  };

  // Follows a job's output. cb gets {reset, lines:[{no,at,cls,text}], source, note, done, error}.
  RW.followLog = (repo, jobId, cb) => {
    let from = 0, source = '', stopped = false, timer = null, reset = false, fails = 0;
    async function poll() {
      if (stopped) return;
      let delay = 700, c;
      try {
        c = await RW.get('/api/log?repo=' + encodeURIComponent(repo) + '&job=' + encodeURIComponent(jobId) + '&from=' + from);
      } catch (e) {
        if (stopped) return;
        fails++;
        cb({ lines: [], source, note: '', done: false, error: e.message });
        timer = setTimeout(poll, Math.min(30000, 3000 * fails));
        return;
      }
      if (stopped) return;
      fails = 0;
      const next = num(c.next), lines = Array.isArray(c.lines) ? c.lines : [];
      // The live lines are replaced by the whole log once the job ends.
      if (from > 0 && ((source === 'live' && c.source !== 'live') || next < from)) {
        from = 0; source = c.source || ''; reset = true;
        timer = setTimeout(poll, 0);
        return;
      }
      source = c.source || '';
      const first = next - lines.length;
      const out = [];
      lines.forEach((l, i) => {
        const p = RW.logLine(l && l.text);
        if (p) out.push({ no: first + i + 1, at: t(l && l.at), cls: p.cls, text: p.text });
      });
      from = next;
      cb({ reset, lines: out, source, note: c.note || '', done: !!c.done, error: '' });
      reset = false;
      if (c.done) return;
      if (source === 'none') delay = 2000;
      timer = setTimeout(poll, delay);
    }
    poll();
    return { stop() { stopped = true; clearTimeout(timer); } };
  };

  // ---------- opening the dashboard from the flyout ----------
  RW.openDashboard = link => RW.run('open', async () => {
    try { await RW.post('/api/open', { link }); }
    catch (e) {
      // No tray app to do it (headless): open it from here.
      if (!window.open('/' + link, '_blank')) throw new Error('Could not open the dashboard: ' + e.message);
    }
  });
})();
