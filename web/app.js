'use strict';

/*
 * Companion status web.
 *
 * Pure front end, no backend. It reads only the companion's on-disk state:
 *   companion.json                       (plansDir, companionPreferences)
 *   <plansDir>/status.json               tasks, pending asks, findings
 *   <plansDir>/runs/<task-id>.json       per-step worker sessionIDs + findings
 *   <plansDir>/reports/<task-id>.json    un-ingested agent reports
 *   <plansDir>/reports/<task-id>-<step>.json
 *   <plansDir>/<plan-id>.md              best-effort parse of the step DAG
 *
 * The shape of those files is the contract. If you change it, update this file
 * and web/sample/ together. See AGENTS.md.
 */

const SEV_RANK = { severe: 0, minor: 1 };
// Highest state-file version this viewer understands. See AGENTS.md.
const SUPPORTED_STATE_VERSION = 1;

let STATE = null;
let autoTimer = null;
const AUTO_MS = 10000;
const EXPANDED = new Set(); // task ids the user has opened (survives re-renders)
const TABS = ['tasks', 'asks', 'findings'];

// which tab is showing (persisted across reloads and auto-refreshes)
let activeTab = (() => {
  try { return localStorage.getItem('companion.tab') || 'tasks'; } catch { return 'tasks'; }
})();
if (!TABS.includes(activeTab)) activeTab = 'tasks';

/* ------------------------------------------------------------------ utils */

const $ = (sel, root = document) => root.querySelector(sel);

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function relTime(iso) {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return esc(iso);
  const s = Math.round((Date.now() - t) / 1000);
  if (s < 0) return 'just now';
  if (s < 60) return s + 's ago';
  const m = Math.round(s / 60);
  if (m < 60) return m + 'm ago';
  const h = Math.round(m / 60);
  if (h < 24) return h + 'h ago';
  return Math.round(h / 24) + 'd ago';
}

function shortId(id) {
  if (!id) return '—';
  const s = String(id);
  return s.length > 16 ? s.slice(0, 12) + '…' : s;
}

function truncate(s, n) {
  s = String(s ?? '');
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}

async function fetchText(url) {
  try {
    const r = await fetch(url, { cache: 'no-store' });
    if (!r.ok) return null;
    return await r.text();
  } catch { return null; }
}

async function fetchJson(url) {
  const t = await fetchText(url);
  if (t == null || !t.trim()) return null;
  try { return JSON.parse(t); } catch { return null; }
}

function chipKey(k, v, extra = '') {
  return `<span class="chip ${extra}"><span class="k">${esc(k)}</span>${esc(v)}</span>`;
}

/* ------------------------------------------------------------- plan parse */

/*
 * Best-effort parse of a plan .md for step ids, titles, deps, agent and
 * checkpoint. The plan format is prose, so this only recognises common
 * patterns; when it finds nothing the DAG falls back to status.json's steps.
 */
function parsePlanSteps(md) {
  const steps = {};
  if (!md) return steps;

  const stepHeadRe = /^\s{0,3}#{1,6}\s+\**\s*(s\d+)\b\s*[:.\u2014\u2013-]?\s*(.*)$/i;
  const headingRe = /^\s{0,3}(#{1,6})\s+(.*)$/;
  const bulletRe = /^\s*(?:[-*]|\d+\.)\s+(.*)$/;

  // fields may sit anywhere on a bullet, e.g. "**Repo:** x · **Agent:** `worker-high` · **Depends on:** s1"
  const depRe = /(?:depends on|deps|dependencies|blocked by|after)\s*[:\-]?\s*([a-z0-9,\s]*)/i;
  const agentRe = /(?:agent|worker|tier)\s*[:\-]?\s*(worker-(?:low|medium|high))/i;
  const ckRe = /checkpoint\s*[:\-]?\s*(true|yes|required)/i;
  const writesRe = /writes?\s*[:\-]?\s*([^·|]+)/i;

  const clean = (s) => String(s).replace(/[*`_]/g, '').replace(/\s+/g, ' ').trim();
  const depIds = (s) => String(s)
    .split(/[,\s]+/)
    .map((x) => x.replace(/[^a-z0-9]/gi, '').toLowerCase())
    .filter((x) => /^s\d+$/.test(x));

  let cur = null;
  let inSteps = false;
  const start = (id, title) => {
    cur = id.toLowerCase();
    if (!steps[cur]) steps[cur] = { id: cur, title: '', agent: '', deps: [], checkpoint: false, writes: [] };
    if (title && !steps[cur].title) steps[cur].title = clean(title);
  };

  for (const raw of md.split(/\r?\n/)) {
    const line = raw.replace(/\s+$/, '');

    const h = headingRe.exec(line);
    if (h) {
      const level = h[1].length;
      const text = clean(h[2]);
      const sh = stepHeadRe.exec(line);
      if (sh) { start(sh[1], sh[2]); continue; }
      if (/^steps\b/i.test(text)) { inSteps = true; cur = null; continue; }
      if (level <= 2) inSteps = false;   // left the Steps section
      continue;
    }

    if (!inSteps || !cur) continue;
    const b = bulletRe.exec(line);
    if (!b) continue;
    const txt = b[1].replace(/\*\*/g, '').replace(/`/g, '').trim();

    const m = txt;
    if (!steps[cur].agent && (agentRe.exec(m))) steps[cur].agent = agentRe.exec(m)[1].toLowerCase();
    const dep = depRe.exec(m);
    if (dep) steps[cur].deps.push(...depIds(dep[1]));
    if (ckRe.test(m)) steps[cur].checkpoint = true;
    const wr = writesRe.exec(m);
    if (wr && !steps[cur].writes.length) {
      steps[cur].writes = wr[1].split(/[,;]\s*|\s{2,}/).map((w) => w.replace(/[*`_]/g, '').trim()).filter(Boolean);
    }
  }
  for (const s of Object.values(steps)) s.deps = [...new Set(s.deps)];
  return steps;
}

/* Best-effort read of a plan's Summary (a "## Summary" section or a **Summary:** line). */
function parsePlanSummary(md) {
  if (!md) return '';
  const clean = (x) => String(x).replace(/[*`#_]/g, '').replace(/\s+/g, ' ').trim();
  const inline = /^\s*(?:\*\*)?summary(?:\*\*:?|:\*\*)\s*(.+)$/im.exec(md);
  if (inline) return clean(inline[1]);
  const lines = md.split(/\r?\n/);
  const i = lines.findIndex((l) => /^#{1,6}\s+summary\b/i.test(l));
  if (i >= 0) {
    const buf = [];
    for (let j = i + 1; j < lines.length; j++) {
      if (/^#{1,6}\s/.test(lines[j])) break;
      if (lines[j].trim()) buf.push(lines[j].trim());
    }
    if (buf.length) return clean(buf.join(' '));
  }
  return '';
}

/* ------------------------------------------------------------------ load */

async function loadState() {
  const demo = new URLSearchParams(location.search).has('demo');
  const root = demo ? 'sample' : '..';

  const companion = (await fetchJson(`${root}/companion.json`)) || {};
  const plansDir = String(companion.plansDir || 'plans/');
  const base = `${root}/${plansDir.replace(/\/+$/, '')}/`;

  const status = (await fetchJson(`${base}status.json`)) || { tasks: {}, pending: [], findings: [] };
  status.tasks = status.tasks || {};
  status.pending = status.pending || [];
  status.findings = status.findings || [];

  // missing version => v0 (pre-versioning format)
  const rawVer = Number(status.version);
  const stateVersion = Number.isInteger(rawVer) && rawVer >= 0 ? rawVer : 0;

  const ids = Object.keys(status.tasks);
  const runs = {};
  const reports = {};
  const plans = {};
  const stepReports = {};

  await Promise.all(ids.map(async (id) => {
    const t = status.tasks[id] || {};
    const planName = planRelName(plansDir, t.plan);
    const [run, report, planMd] = await Promise.all([
      fetchJson(`${base}runs/${id}.json`),
      fetchJson(`${base}reports/${id}.json`),
      planName ? fetchText(`${base}${planName}`) : Promise.resolve(null),
    ]);
    runs[id] = run;
    reports[id] = report;
    plans[id] = planName ? { path: planName, steps: parsePlanSteps(planMd), summary: parsePlanSummary(planMd) } : null;
  }));

  // step-level reports, discoverable from the task id + step id (no dir listing)
  await Promise.all(ids.flatMap((id) => {
    const t = status.tasks[id] || {};
    const steps = Object.keys(t.steps || {});
    return steps.map(async (sid) => {
      const r = await fetchJson(`${base}reports/${id}-${sid}.json`);
      if (r) stepReports[`${id}-${sid}`] = r;
    });
  }));

  return {
    demo, root, base, companion, plansDir, stateVersion,
    status, runs, reports, plans, stepReports, loadedAt: new Date().toISOString(),
  };
}

/* --------------------------------------------------------------- helpers */

function stepsOf(taskId) {
  const t = STATE.status.tasks[taskId] || {};
  const statusSteps = t.steps || {};
  const planSteps = (STATE.plans[taskId] && STATE.plans[taskId].steps) || {};
  const ids = Object.keys(statusSteps).length ? Object.keys(statusSteps) : Object.keys(planSteps);
  return ids.map((id) => {
    const p = planSteps[id] || {};
    const raw = statusSteps[id];
    const obj = (raw && typeof raw === 'object') ? raw : {};
    const run = STATE.runs[taskId] || {};
    return {
      id,
      status: stepStatus(raw),
      title: p.title || obj.summary || '',
      detail: obj.note || obj.detail || '',
      agent: p.agent || '',
      deps: (p.deps || []).filter((d) => ids.includes(d)),
      checkpoint: !!p.checkpoint,
      writes: p.writes || [],
      commit: obj.commit || (Array.isArray(obj.commits) ? obj.commits.join(', ') : ''),
      prUrl: obj.prUrl || '',
      session: obj.workerSession || obj.session || (run.steps || {})[id] || '',
      report: STATE.stepReports[`${taskId}-${id}`] || null,
    };
  });
}

function findingsForTask(id) {
  return STATE.status.findings.filter((f) =>
    f.task === id || (!f.task && deriveProvenance(f).tasks.includes(id)));
}

/* ------------------------------------------------------- effective state */
/*
 * `status` alone does not tell "busy" from "waiting": a task with an open PR,
 * an unanswered ask, or a stale `updated` is recorded `running` but is not
 * actually progressing. Derive an activity state from what the files already
 * carry, so waiting tasks are visible instead of hiding behind `running`.
 */
const FRESH_MS = 10 * 60 * 1000; // updated within 10 min => still active
function effectiveState(id, t) {
  const raw = t.status || 'pending';
  const b = statusBucket(raw);
  const steps = stepsOf(id);
  const runningSteps = steps.filter((s) => statusBucket(s.status) === 'running').length;
  const blockedSteps = steps.filter((s) => statusBucket(s.status) === 'blocked').length;
  const asks = openAsksFor(id).length;
  const pr = prSummary(t);
  const ageMs = t.updated ? Date.now() - Date.parse(t.updated) : null;
  const fresh = ageMs != null && ageMs < FRESH_MS;

  const notes = [];
  if (runningSteps) notes.push(`${runningSteps} step${runningSteps > 1 ? 's' : ''} in progress`);
  if (blockedSteps) notes.push(`${blockedSteps} step${blockedSteps > 1 ? 's' : ''} blocked`);
  if (asks) notes.push(`${asks} open ask${asks > 1 ? 's' : ''}`);

  // key, label, css color, sort rank (lower = first), extra note
  const mk = (key, label, color, rank, note = '') => ({ key, label, color, rank, note });

  if (b === 'failed') return mk('failed', 'failed', 'var(--s-failed)', 1);
  if (b === 'cancelled') return mk('cancelled', 'cancelled', 'var(--s-cancelled)', 10);
  if (b === 'done') return mk('done', 'done', 'var(--s-done)', 9);
  if (b === 'blocked') return mk('blocked', 'blocked', 'var(--s-blocked)', 0);
  if (b === 'awaiting_approval') return mk('awaiting_approval', 'awaiting approval', 'var(--s-awaiting_approval)', 2);
  if (asks) return mk('waiting', 'waiting · you', 'var(--e-waiting)', 3, notes.join(' · '));
  if (b === 'running') {
    if (fresh) return mk('active', 'active', 'var(--e-active)', 4, notes.join(' · '));
    if (pr) return mk('review', 'PR / review', 'var(--e-review)', 5, String(pr));
    if (blockedSteps && runningSteps === 0) return mk('blocked', 'blocked', 'var(--s-blocked)', 0, notes.join(' · '));
    return mk('idle', `idle · ${relTime(t.updated)}`, 'var(--e-idle)', 6, notes.join(' · '));
  }
  if (b === 'planning') return mk('planning', 'planning', 'var(--s-planning)', 7);
  return mk('pending', 'pending', 'var(--s-pending)', 8);
}

function findingById(id) {
  return STATE.status.findings.find((f) => f.id === id) || null;
}

/* --------------------------------------------------- schema normalization */
/*
 * The state schema has more than one shape in the wild: steps may be a plain
 * status string or an object with status/commit/summary; findings carry
 * `detail` or `evidence` and may lack severity/task; asks carry `digest` or
 * `question`; tasks reference PRs via `pr` or `prUrls`. Normalize both so the
 * view never shows [object Object] and tolerates missing fields.
 */
function stepStatus(v) {
  if (v == null) return 'pending';
  if (typeof v === 'string') return v;
  return v.status || 'pending';
}

function toList(v) {
  if (v == null || v === '') return [];
  if (Array.isArray(v)) return v.map(String).filter(Boolean);
  return String(v).split(/\s*;\s*|\s+\/\s+/).map((s) => s.trim()).filter(Boolean);
}

function isOpenFinding(f) {
  return !/ignor|noted|resolved|closed|done|informational|no action/i.test(String(f.status || ''));
}

function normalizeFinding(f) {
  return {
    id: f.id || '', title: f.title || '',
    detail: f.detail || f.evidence || '',
    severity: f.severity === 'severe' ? 'severe' : 'minor',
    status: f.status || '', task: f.task || '', step: f.step || '',
    agent: f.agent || '', session: f.session || '', source: f.source || '',
    options: toList(f.options), open: isOpenFinding(f),
  };
}

function normalizeAsk(a) {
  return {
    id: a.id || '', kind: a.kind || 'ask', task: a.task || '',
    text: a.digest || a.question || '',
    options: toList(a.options), recommended: a.recommended,
    findingRef: a.finding || '', findingIds: toList(a.findings),
    status: a.status || '', answer: a.answer || '',
  };
}

// An ask is open only while it needs the user. It is closed when it carries an
// `answer`, or a resolved `status` (answered/resolved/closed/moot/...). The
// documented schema has no status: absence means open.
const RESOLVED_ASK_RE = /answer|resolv|clos|done|moot|declin|ignor|cancel|supersed|complete|dropped/i;
function isOpenAsk(a) {
  if (!a) return false;
  if (a.answer) return false;
  const st = String(a.status || '').trim();
  return !st || !RESOLVED_ASK_RE.test(st);
}
function taskRefMatches(ref, id) {
  if (!ref) return false;
  return String(ref).split(/[\s,/]+/).includes(id);
}
function openAsksFor(id) {
  return STATE.status.pending.filter((a) => isOpenAsk(a) && taskRefMatches(a.task, id));
}

// t.plan is inconsistent: sometimes "plans/x.md", sometimes "x.md (rev 2)".
function planRelName(plansDir, p) {
  if (!p) return null;
  let s = String(p).replace(/\s*\([^)]*\)\s*$/, '').trim();
  const dir = String(plansDir).replace(/\/+$/, '');
  if (dir && s.startsWith(dir + '/')) s = s.slice(dir.length + 1);
  return s.replace(/^\/+/, '');
}

function prSummary(t) {
  if (t.pr) return String(t.pr);
  const urls = t.prUrls;
  if (!urls) return '';
  const vals = Array.isArray(urls) ? urls : Object.values(urls);
  return vals.length ? `${vals.length} PR${vals.length > 1 ? 's' : ''}` : '';
}

/* ---------------------------------------------------------- github links */
function firstUrl(x) {
  const m = /https?:\/\/[^\s)]+/.exec(String(x || ''));
  return m ? m[0] : null;
}
function ghRepo(name) {
  const m = /(?:https?:\/\/github\.com\/)?([\w.-]+\/[\w.-]+?)(?:\.git)?$/.exec(String(name).trim());
  return m ? `https://github.com/${m[1]}` : null;
}
function ghIssue(x) {
  if (!x) return null;
  const t = String(x).trim();
  if (/^https?:\/\//.test(t)) return t;
  let m = /^([\w.-]+\/[\w.-]+)#(\d+)$/.exec(t);
  if (m) return `https://github.com/${m[1]}/issues/${m[2]}`;
  m = /^([\w.-]+\/[\w.-]+)\/(?:issues\/)?(\d+)$/.exec(t);
  if (m) return `https://github.com/${m[1]}/issues/${m[2]}`;
  return null;
}
function linkChip(k, label, url) {
  const inner = `<span class="k">${esc(k)}</span>${esc(label)}`;
  return url
    ? `<a class="chip link" href="${esc(url)}" target="_blank" rel="noopener">${inner}</a>`
    : `<span class="chip">${inner}</span>`;
}
function repoLinks(repo) {
  if (!repo) return '';
  return String(repo).split(/\s*\+\s*|\s*,\s*/).map((x) => x.trim()).filter(Boolean)
    .map((name) => linkChip('repo', name, ghRepo(name))).join('');
}
function issueLinks(issue) {
  if (!issue) return '';
  return String(issue).split(/\s*[,+]\s*/).map((x) => x.trim()).filter(Boolean)
    .map((name) => linkChip('issue', name, ghIssue(name))).join('');
}
function prLinkChips(t) {
  const out = [];
  if (t.pr) out.push(linkChip('pr', firstUrl(t.pr) ? 'PR' : String(t.pr), firstUrl(t.pr)));
  const urls = t.prUrls;
  if (urls && typeof urls === 'object' && !Array.isArray(urls)) {
    for (const [repo, v] of Object.entries(urls)) out.push(linkChip('pr', repo, firstUrl(v)));
  } else if (Array.isArray(urls)) {
    urls.forEach((v, i) => out.push(linkChip('pr', 'PR' + (i + 1), firstUrl(v))));
  }
  return out.join('');
}

/* ------------------------------------------------------------ traceability */
/*
 * Findings do not carry first-class provenance (task/step/agent/session) in
 * every schema, so the web recovers what it can: identifiers mentioned in the
 * finding text, plus the source files (reports/runs) it can reach. This feeds
 * the "Ask" button, which assembles a self-contained context block to paste
 * back to the companion when the recorded info is not enough.
 */
function deriveProvenance(raw) {
  const text = [raw.title, raw.detail, raw.evidence, Array.isArray(raw.options) ? raw.options.join(' ') : raw.options]
    .filter(Boolean).join('\n');
  const uniq = (a) => [...new Set(a)];
  const tasks = uniq([raw.task, ...(text.match(/\bT-\d+\b/g) || [])].filter(Boolean));
  const steps = uniq([raw.step, ...[
    ...(text.match(/\bs\d+\b(?=\s*[:.—-])/gi) || []),
    ...(text.match(/\bstep\s+s\d+\b/gi) || []),
  ].map((s) => s.toLowerCase().replace(/^step\s+/, ''))].filter(Boolean));
  const sessions = uniq([raw.session, ...(text.match(/ses_[A-Za-z0-9]+/g) || [])].filter(Boolean));
  const roles = uniq([raw.agent, ...(text.match(/\b(?:worker-(?:low|medium|high)|planner|orchestrator|companion)\b/gi) || [])]
    .filter(Boolean).map((s) => s.toLowerCase()));
  const files = uniq(text.match(/\b[\w./-]+\.(?:md|json|ya?ml|go|ts|tsx|sh)\b/g) || [])
    .filter((p) => p.includes('/') || /\.(md|json)$/.test(p)).slice(0, 4);
  return { tasks, steps, sessions, roles, files, source: raw.source || '' };
}

function findingSources(prov) {
  if (prov.source) return [prov.source];
  const out = [];
  for (const t of prov.tasks) {
    if (STATE.reports[t]) out.push(`reports/${t}.json`);
    if (STATE.runs[t]) out.push(`runs/${t}.json`);
    for (const s of prov.steps) {
      if (STATE.stepReports[`${t}-${s}`]) out.push(`reports/${t}-${s}.json`);
    }
  }
  return [...new Set(out)];
}

function findingContext(id) {
  const raw = STATE.status.findings.find((f) => f.id === id);
  if (!raw) return '';
  const f = normalizeFinding(raw);
  const prov = deriveProvenance(raw);
  const src = findingSources(prov);
  const L = [];
  L.push(`Companion finding ${f.id} — ${f.title}`);
  L.push(`severity: ${f.severity}   status: ${f.status}`);
  if (f.detail) L.push(`evidence: ${f.detail}`);
  if (f.options.length) L.push(`options: ${f.options.join(' | ')}`);
  const where = [];
  if (prov.tasks.length) where.push(`task(s) ${prov.tasks.join(', ')}`);
  if (prov.steps.length) where.push(`step(s) ${prov.steps.join(', ')}`);
  if (prov.roles.length) where.push(`agent ${prov.roles.join(', ')}`);
  if (prov.sessions.length) where.push(`session(s) ${prov.sessions.join(', ')}`);
  if (where.length) L.push(`provenance: ${where.join(' · ')}`);
  if (src.length) L.push(`source file(s): ${src.map((s) => STATE.plansDir + s).join(', ')}`);
  for (const t of prov.tasks) {
    const task = STATE.status.tasks[t];
    if (task) L.push(`context ${t}: ${task.title || ''} [${task.status || ''}] ${task.repo || ''} ${task.branch || ''}`.trim());
  }
  L.push('question: ');
  return L.join('\n');
}


/* ---------------------------------------------------------------- render */

function render() {
  renderTabs();
  renderStats();
  renderBanner();
  renderTasks();
  renderAsks();
  renderFindings();
  renderRaw();
  renderFooterInfo();
  applyTab();
  drawAllEdges();
}

/* --------------------------------------------------------------- tabs */
/*
 * The whole page is one tab per concern: tasks, open questions (asks) and
 * findings. Tab panels are all rendered (so counts and deep links stay
 * correct) and then shown/hidden by `applyTab`.
 */
function renderTabs() {
  const tasks = Object.keys(STATE.status.tasks).length;
  const open = STATE.status.pending.filter(isOpenAsk).length;
  const findings = STATE.status.findings.map(normalizeFinding);
  const openFindings = findings.filter((f) => f.open);
  const severe = openFindings.filter((f) => f.severity === 'severe').length;

  const spec = [
    ['tasks', 'Tasks', tasks, ''],
    ['asks', 'Open questions', open, open ? 'waiting' : ''],
    ['findings', 'Findings', openFindings.length, severe ? 'severe' : ''],
  ];
  const buttons = spec.map(([key, label, n, extra]) =>
    `<button class="tab ${key === activeTab ? 'active' : ''}" data-tab="${key}"
        role="tab" aria-selected="${key === activeTab}">
      ${esc(label)}<span class="count-pill ${extra}">${n}</span>
    </button>`).join('');

  $('#tabs').innerHTML = buttons +
    `<span class="spacer"></span><span class="read-at">read ${esc(relTime(STATE.loadedAt))}</span>`;
}

function applyTab() {
  for (const key of TABS) {
    const el = document.getElementById('view-' + key);
    if (el) el.hidden = key !== activeTab;
  }
  const nav = $('#tabs');
  if (nav) for (const b of nav.querySelectorAll('.tab')) {
    b.classList.toggle('active', b.dataset.tab === activeTab);
  }
}

function showTab(key) {
  if (!TABS.includes(key)) key = 'tasks';
  activeTab = key;
  try { localStorage.setItem('companion.tab', key); } catch { /* private mode */ }
  applyTab();
  requestAnimationFrame(drawAllEdges); // the tasks DAG may have just become visible
}

function renderFooterInfo() {
  const el = $('#foot-sub');
  if (!el) return;
  const ver = `v${STATE.stateVersion}${STATE.stateVersion === 0 ? ' (legacy)' : ''}`;
  el.innerHTML = (STATE.demo
    ? 'demo · <b>web/sample</b>'
    : `<b>${esc(STATE.plansDir)}</b>status.json · ${Object.keys(STATE.status.tasks).length} tasks`) + ` · ${ver}`;
}


function renderStats() {
  const entries = Object.entries(STATE.status.tasks);
  const keys = entries.map(([id, t]) => effectiveState(id, t).key);
  const n = (k) => keys.filter((x) => x === k).length;
  const severe = STATE.status.findings.map(normalizeFinding).filter((f) => f.severity === 'severe' && f.open).length;
  const tiles = [
    ['', entries.length, 'Tasks'],
    ['active', n('active'), 'Active'],
    ['waiting', n('waiting') + n('awaiting_approval'), 'Waiting on you'],
    ['review', n('review'), 'PR / review'],
    ['blocked', n('blocked'), 'Blocked'],
    ['idle', n('idle'), 'Idle'],
    ['done', n('done'), 'Done'],
    ['severe', severe, 'Severe findings'],
  ];
  $('#stats').innerHTML = tiles.map(([c, count, l]) =>
    `<div class="stat ${c}"><div class="n">${count}</div><div class="l">${l}</div></div>`).join('');
}

function renderBanner() {
  const el = $('#banner');
  const severe = STATE.status.findings.map(normalizeFinding).filter((f) => f.severity === 'severe' && f.open);
  const blocked = Object.entries(STATE.status.tasks).filter(([id, t]) => effectiveState(id, t).key === 'blocked');
  const newer = STATE.stateVersion > SUPPORTED_STATE_VERSION;
  if (!severe.length && !blocked.length && !newer) { el.hidden = true; return; }
  const parts = [];
  if (newer) parts.push(`<b>state v${STATE.stateVersion}</b> is newer than this viewer (supports up to v${SUPPORTED_STATE_VERSION}) — some fields may not be shown`);
  if (severe.length) parts.push(`<b>${severe.length} severe finding${severe.length > 1 ? 's' : ''}</b> needing attention`);
  if (blocked.length) parts.push(`<b>${blocked.length} blocked task${blocked.length > 1 ? 's' : ''}</b> (${blocked.map(([id]) => esc(id)).join(', ')})`);
  el.innerHTML = `<span>⚠</span><span>${parts.join(' · ')}</span>`;
  el.hidden = false;
}

function renderTasks() {
  const entries = Object.entries(STATE.status.tasks);
  if (!entries.length) {
    $('#tasks-col').innerHTML =
      `<section class="panel"><div class="panel-head"><h2>Tasks</h2></div>
       <div class="empty">No <code>status.json</code> state yet. Run <code>/companion-start</code>, or open
       <code>?demo=1</code> to see a sample.</div></section>`;
    return;
  }
  entries.sort((a, b) => {
    const ra = effectiveState(a[0], a[1]).rank;
    const rb = effectiveState(b[0], b[1]).rank;
    if (ra !== rb) return ra - rb;
    return String(b[0]).localeCompare(String(a[0]), undefined, { numeric: true });
  });

  const html = entries.map(([id, t]) => renderTask(id, t)).join('');
  $('#tasks-col').innerHTML =
    `<section class="panel"><div class="panel-head"><h2>Tasks</h2>
     <span class="count-pill">${entries.length}</span></div>${html}</section>`;
}

function renderTask(id, t) {
  const st = t.status || 'pending';
  const eff = effectiveState(id, t);
  const steps = stepsOf(id);
  const findings = findingsForTask(id).map(normalizeFinding);
  const asks = openAsksFor(id).map(normalizeAsk);
  const blockedSteps = steps.filter((s) => statusBucket(s.status) === 'blocked');
  const severe = findings.filter((f) => f.severity === 'severe');
  const report = STATE.reports[id];
  const summary = (t.summary || (STATE.plans[id] && STATE.plans[id].summary) || '').trim();

  const needsAttention = ['waiting', 'blocked', 'awaiting_approval', 'failed', 'review'].includes(eff.key);
  const attention = needsAttention ? renderAttention(eff, asks, blockedSteps, severe, t) : '';

  const sessions = { ...(t.sessions || {}) };
  for (const [k, v] of [['planner', t.plannerSession], ['orchestrator', t.orchestratorSession], ['worker', t.workerSession]]) {
    if (v && !sessions[k]) sessions[k] = v;
  }

  // top: repos + issues (links), status (badge above), summary
  const links = [repoLinks(t.repo), issueLinks(t.issue)].filter(Boolean).join('');
  const prs = prLinkChips(t);

  // everything else lives in the collapsible body
  const meta = [chipKey('status', st)];
  if (t.kind) meta.push(chipKey('kind', t.kind));
  if (t.branch) meta.push(chipKey('branch', t.branch));
  if (t.plan) meta.push(chipKey('plan', planRelName(STATE.plansDir, t.plan)));
  if (t.worktree) meta.push(chipKey('worktree', t.worktree));
  if (t.workArea) meta.push(chipKey('area', t.workArea));
  if (prs) meta.push(prs);
  meta.push(chipKey('updated', relTime(t.updated)));
  if (report) meta.push(`<span class="chip" style="border-color:var(--sev-minor);color:var(--sev-minor)">un-ingested report</span>`);

  const sessionChips = Object.entries(sessions).map(([role, sid]) => chipKey(role, shortId(sid))).join('');
  const writes = (t.writes || []).map((w) => `<span class="chip write">${esc(w)}</span>`).join('');

  const flags = [`<span class="badge" style="--st:${eff.color}">${esc(eff.label)}</span>`];

  const progress = renderProgress(steps);
  const stepsBlock = steps.length
    ? `<div class="steps-wrap">${progress}${renderStepsDag(id, steps)}</div>`
    : '';
  const findingsBlock = findings.length
    ? `<details class="finds"><summary>Findings <span class="count-pill">${findings.length}</span></summary>
       <div class="finds-body">${findings.map(findingCard).join('')}</div></details>`
    : '';

  const expanded = EXPANDED.has(id);
  return `
  <article class="card task ${expanded ? 'expanded' : ''}" id="task-${esc(id)}" data-task="${esc(id)}" style="--st:${eff.color}">
    <div class="task-head" role="button" tabindex="0" aria-expanded="${expanded}">
      <span class="chev"></span>
      <div class="task-line1">
        <span class="dot ${eff.key === 'active' ? 'pulse' : ''}"></span>
        <span class="task-id">${esc(id)}</span>
        <span class="task-title">${esc(t.title || '')}</span>
        <span class="task-flag">${flags.join('')}</span>
      </div>
    </div>
    <div class="task-body">
      ${(links || summary) ? `<div class="task-top">
        ${links ? `<div class="task-bar">${links}</div>` : ''}
        ${summary ? `<div class="task-summary">${esc(summary)}</div>` : ''}
      </div>` : ''}
      ${attention}
      <div class="meta">${meta.join('')}</div>
      ${sessionChips ? `<div class="meta">${sessionChips}</div>` : ''}
      ${writes ? `<div class="meta">${writes}</div>` : ''}
      ${findingsBlock}
      ${stepsBlock}
    </div>
  </article>`;
}

// "why this task needs you": the open asks, blocked steps, severe findings
function renderAttention(eff, asks, blockedSteps, severe, t) {
  const rows = [];
  for (const a of asks) {
    const rec = recommendIndex(a);
    const opts = a.options.map((o, i) => `<span class="opt ${i === rec ? 'rec' : ''}">${esc(o)}</span>`).join('');
    rows.push(`<div class="att-row">
      <span class="badge ghost">${esc(a.id)}</span>
      <span class="badge ghost">${esc(a.kind)}</span>
      <div class="att-text">${esc(a.text)}</div>
      ${opts ? `<div class="a-opts">${opts}</div>` : ''}
    </div>`);
  }
  for (const s of blockedSteps) {
    rows.push(`<div class="att-row"><span class="badge" style="--st:var(--s-blocked)">step ${esc(s.id)}</span>
      <div class="att-text">${esc(s.status)}</div></div>`);
  }
  for (const f of severe) {
    rows.push(`<div class="att-row"><span class="badge" style="--st:var(--sev-severe)">${esc(f.id)} · severe</span>
      <div class="att-text">${esc(f.title)}</div></div>`);
  }
  if (eff.key === 'review') {
    const pr = prLinkChips(t);
    if (pr) rows.push(`<div class="att-row"><div class="att-text">Waiting on PR review —</div>${pr}</div>`);
  }
  if (!rows.length) rows.push(`<div class="att-row"><div class="att-text">${esc(eff.note || 'waiting')}</div></div>`);
  return `<div class="task-attention" style="--st:${eff.color}">
    <div class="att-head">⚠ Needs attention · ${esc(eff.label)}</div>
    ${rows.join('')}
  </div>`;
}

function renderProgress(steps) {
  if (!steps.length) return '';
  const counts = {};
  for (const s of steps) {
    const b = statusBucket(s.status);
    counts[b] = (counts[b] || 0) + 1;
  }
  const total = steps.length;
  const order = ['done', 'running', 'failed', 'blocked', 'pending'];
  const segs = order.filter((k) => counts[k]).map((k) =>
    `<span class="seg-${k}" style="width:${(counts[k] / total) * 100}%"></span>`).join('');
  const done = counts.done || 0;
  return `<div style="display:flex;justify-content:space-between;margin-top:12px;font-size:11.5px;color:var(--text-faint)">
    <span>Steps ${done}/${total} done</span>
    <span>${steps.map((s) => `${s.id}:${truncate(s.status, 22)}`).join(' · ')}</span></div>
    <div class="progress">${segs}</div>`;
}

function renderStepsDag(taskId, steps) {
  const hasDeps = steps.some((s) => s.deps.length);
  const nodes = (list) => list.map((s) => stepNode(taskId, s)).join('');

  if (!hasDeps) {
    return `<div class="steps-list" data-dag>${steps.map((s) => stepNode(taskId, s, true)).join('')}</div>`;
  }

  // longest-path layering
  const byId = Object.fromEntries(steps.map((s) => [s.id, s]));
  const level = {};
  const depth = (id, stack) => {
    if (level[id] != null) return level[id];
    if (stack.has(id)) return 0;
    stack.add(id);
    const ds = (byId[id].deps || []).filter((d) => byId[d]);
    const v = ds.length ? Math.max(...ds.map((d) => depth(d, stack))) + 1 : 0;
    stack.delete(id);
    level[id] = v;
    return v;
  };
  steps.forEach((s) => depth(s.id, new Set()));

  const cols = [];
  for (const s of steps) {
    const l = level[s.id] || 0;
    (cols[l] = cols[l] || []).push(s);
  }
  const colsHtml = cols.map((list) => `<div class="dag-col">${nodes(list)}</div>`).join('');

  return `<div class="dag" data-dag>
    <div class="dag-cols">${colsHtml}</div>
    <svg class="dag-edges"><defs>
      <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
        <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--border)"></path>
      </marker>
    </defs></svg>
  </div>`;
}

function stepNode(taskId, s, row = false) {
  const meta = [];
  if (s.deps.length) meta.push(`needs ${s.deps.map(esc).join(', ')}`);
  if (s.session) meta.push(shortId(s.session));
  if (s.checkpoint) meta.push('<span class="s-ck">checkpoint</span>');
  if (s.report) meta.push('<span class="s-find">report</span>');
  if (s.commit) meta.push(esc(String(s.commit).split(/[ (]/)[0]));
  if (s.prUrl) meta.push(`<span title="${esc(s.prUrl)}">PR</span>`);
  if (s.writes && s.writes.length) meta.push(`writes ${s.writes.map(esc).join(', ')}`);

  return `<div class="step ${row ? 'row' : ''}" data-step="${esc(s.id)}"
      data-deps="${esc(s.deps.join(','))}" data-task="${esc(taskId)}"
      style="--st:var(--s-${cssStatus(s.status)})">
    <div class="s-top"><span class="dot"></span><span class="s-id">${esc(s.id)}</span>
      ${s.agent ? `<span class="s-agent">${esc(s.agent)}</span>` : ''}</div>
    ${s.title ? `<div class="s-title">${esc(s.title)}</div>` : ''}
    <div class="s-status">${esc(s.status)}</div>
    ${s.detail ? `<div class="s-detail">${esc(s.detail)}</div>` : ''}
    ${meta.length ? `<div class="s-meta">${meta.join('<span>·</span>')}</div>` : ''}
  </div>`;
}

function renderTaskFindings(findings) {
  const rows = findings.map(normalizeFinding)
    .sort((a, b) => (SEV_RANK[a.severity] ?? 9) - (SEV_RANK[b.severity] ?? 9))
    .map(findingCard).join('');
  return `<div class="finds-inline">${rows}</div>`;
}

function findingCard(raw) {
  const f = normalizeFinding(raw);
  const prov = deriveProvenance(raw);
  const src = findingSources(prov);

  const taskChips = prov.tasks.map((t) => STATE.status.tasks[t]
    ? `<a class="chip link" href="#task-${esc(t)}">${esc(t)}</a>`
    : `<span class="chip">${esc(t)}</span>`).join('');
  const provChips = [
    taskChips,
    ...prov.steps.map((s) => `<span class="chip"><span class="k">step</span>${esc(s)}</span>`),
    ...prov.roles.map((r) => `<span class="chip"><span class="k">agent</span>${esc(r)}</span>`),
    ...prov.sessions.map((s) => `<span class="chip" title="${esc(s)}"><span class="k">ses</span>${esc(shortId(s))}</span>`),
  ].join('');
  const srcChips = src.map((p) =>
    `<span class="chip src" title="${esc(STATE.plansDir + p)}">${esc(STATE.plansDir + p)}</span>`).join('');

  const meta = [];
  if (f.status) meta.push(chipKey('status', truncate(f.status, 44), 'step-tag'));

  const ask = f.id
    ? `<button class="btn tiny" data-ctx="${esc(f.id)}" title="Copy this finding as a self-contained block to paste to the companion">Ask</button>`
    : '';

  return `<div class="finding" style="--sev:var(--sev-${f.severity})" id="finding-${esc(f.id)}">
    <div class="f-top">
      <span class="dot" style="--st:var(--sev-${f.severity})"></span>
      <span class="f-id">${esc(f.id)}</span>
      <span class="badge" style="--st:var(--sev-${f.severity})">${esc(f.severity)}</span>
      <span class="f-title">${esc(f.title)}</span>
      ${ask}
    </div>
    ${f.detail ? `<div class="f-detail">${esc(f.detail)}</div>` : ''}
    ${f.options.length ? `<div class="a-opts">${f.options.map((o) => `<span class="opt">${esc(o)}</span>`).join('')}</div>` : ''}
    ${provChips || srcChips ? `<div class="f-prov">${provChips}${srcChips}</div>` : ''}
    ${meta.length ? `<div class="f-meta">${meta.join('')}</div>` : ''}
  </div>`;
}

function renderAsks() {
  const all = STATE.status.pending;
  const open = all.filter(isOpenAsk).map(normalizeAsk);
  const resolved = all.filter((a) => !isOpenAsk(a)).map(normalizeAsk);

  const findingSev = (a) => {
    const ids = [a.findingRef, ...a.findingIds].filter(Boolean);
    for (const id of ids) { const f = findingById(id); if (f) return normalizeFinding(f).severity; }
    return '';
  };
  // severe finding asks first, then decisions, then the rest
  const rank = (a) => (a.kind === 'finding' ? (findingSev(a) === 'severe' ? 0 : 5) : (a.kind === 'decision' ? 1 : 2));

  const openRows = [...open].sort((x, y) => rank(x) - rank(y)).map((a) => {
    const sev = findingSev(a);
    const recIdx = recommendIndex(a);
    const opts = a.options.map((o, i) => `<span class="opt ${i === recIdx ? 'rec' : ''}">${esc(o)}</span>`).join('');
    return `<div class="ask ${a.kind === 'finding' ? 'finding' : ''}">
      <div class="a-top">
        <span class="a-id">${esc(a.id)}</span>
        <span class="badge ghost">${esc(a.kind)}</span>
        ${a.task ? `<span class="badge ghost">${esc(a.task)}</span>` : ''}
        ${sev ? `<span class="badge" style="--st:var(--sev-${sev})">${esc(sev)}</span>` : ''}
      </div>
      ${a.text ? `<div class="a-digest">${esc(a.text)}</div>` : ''}
      ${opts ? `<div class="a-opts">${opts}</div>` : ''}
    </div>`;
  }).join('');

  const resolvedBlock = resolved.length
    ? `<details class="resolved"><summary>Resolved <span class="count-pill">${resolved.length}</span></summary>
       <div class="cards-grid">${resolved.map(resolvedAskCard).join('')}</div></details>`
    : '';

  $('#asks-panel').innerHTML =
    `<div class="panel-head"><h2>Open questions</h2><span class="count-pill">${open.length}</span></div>
     ${open.length ? `<div class="cards-grid">${openRows}</div>` : '<div class="empty">Nothing waiting on you.</div>'}${resolvedBlock}`;
}

function resolvedAskCard(a) {
  return `<div class="ask resolved">
    <div class="a-top">
      <span class="a-id">${esc(a.id)}</span>
      <span class="badge ghost">${esc(a.kind)}</span>
      ${a.task ? `<span class="badge ghost">${esc(a.task)}</span>` : ''}
      ${a.status ? `<span class="badge ghost">${esc(a.status)}</span>` : ''}
    </div>
    ${a.text ? `<div class="a-digest">${esc(a.text)}</div>` : ''}
    ${a.answer ? `<div class="a-answer"><span class="k">answer</span>${esc(a.answer)}</div>` : ''}
  </div>`;
}

function recommendIndex(a) {
  const opts = a.options;
  if (!opts.length) return -1;
  if (typeof a.recommended === 'number') return a.recommended;
  if (typeof a.recommended === 'string' && a.recommended) {
    const i = opts.findIndex((o) => o.toLowerCase().includes(a.recommended.toLowerCase()));
    if (i >= 0) return i;
  }
  return opts.findIndex((o) => /recommended|\(rec\)|preferred/i.test(o));
}

function renderFindings() {
  const all = STATE.status.findings.map(normalizeFinding);
  const open = all.filter((f) => f.open);
  const resolved = all.filter((f) => !f.open);

  const bySeverity = (a, b) => {
    const s = (SEV_RANK[a.severity] ?? 9) - (SEV_RANK[b.severity] ?? 9);
    return s || String(a.id).localeCompare(String(b.id), undefined, { numeric: true });
  };

  if (!all.length) {
    $('#findings-panel').innerHTML =
      `<div class="panel-head"><h2>Findings</h2></div><div class="empty">No findings recorded.</div>`;
    return;
  }

  const openHtml = open.length
    ? `<div class="cards-grid">${[...open].sort(bySeverity).map(findingCard).join('')}</div>`
    : '<div class="empty">No open findings.</div>';

  // resolved/ignored findings keep their history, like resolved asks
  const resolvedBlock = resolved.length
    ? `<details class="resolved"><summary>Resolved <span class="count-pill">${resolved.length}</span></summary>
       <div class="cards-grid">${[...resolved].sort(bySeverity).map(findingCard).join('')}</div></details>`
    : '';

  $('#findings-panel').innerHTML =
    `<div class="panel-head"><h2>Findings</h2><span class="count-pill">${open.length}</span></div>
     ${openHtml}${resolvedBlock}`;
}

function renderRaw() {
  const box = (title, obj) =>
    `<div><h3>${esc(title)}</h3><pre>${esc(JSON.stringify(obj, null, 2) || '')}</pre></div>`;
  const parts = [box('status.json', STATE.status)];
  for (const [id, r] of Object.entries(STATE.runs)) if (r) parts.push(box(`runs/${id}.json`, r));
  for (const [id, r] of Object.entries(STATE.reports)) if (r) parts.push(box(`reports/${id}.json`, r));
  for (const [k, r] of Object.entries(STATE.stepReports)) parts.push(box(`reports/${k}.json`, r));
  $('#raw').innerHTML = parts.join('');
}

/* ------------------------------------------------------------ edge draw */

function drawAllEdges() {
  document.querySelectorAll('[data-dag]').forEach(drawEdges);
}

function drawEdges(container) {
  const svg = container.querySelector('.dag-edges');
  if (!svg) return;
  const rect = container.getBoundingClientRect();
  const W = container.scrollWidth;
  const H = container.scrollHeight;
  svg.setAttribute('width', W);
  svg.setAttribute('height', H);
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);

  const nodes = [...container.querySelectorAll('.step[data-step]')];
  const pos = new Map();
  for (const n of nodes) {
    const r = n.getBoundingClientRect();
    pos.set(n.dataset.step, {
      x1: r.left - rect.left, x2: r.right - rect.left,
      y1: r.top - rect.top, y2: r.bottom - rect.top,
    });
  }
  let paths = '';
  for (const n of nodes) {
    const deps = (n.dataset.deps || '').split(',').filter(Boolean);
    const to = pos.get(n.dataset.step);
    for (const d of deps) {
      const from = pos.get(d);
      if (!from || !to) continue;
      const x1 = from.x2, y1 = (from.y1 + from.y2) / 2;
      const x2 = to.x1, y2 = (to.y1 + to.y2) / 2;
      const mx = (x1 + x2) / 2;
      const active = statusBucket(n.querySelector('.s-status')?.textContent) === 'running';
      paths += `<path class="${active ? 'active' : ''}" marker-end="url(#arrow)"
        d="M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}"></path>`;
    }
  }
  svg.innerHTML = svg.querySelector('defs').outerHTML + paths;
}

/* ---------------------------------------------------------------- boot */

function statusBucket(s) {
  s = String(s || '').toLowerCase();
  if (/cancel/.test(s)) return 'cancelled';
  if (/fail|error/.test(s)) return 'failed';
  if (/block/.test(s)) return 'blocked';
  if (/await|approval/.test(s)) return 'awaiting_approval';
  if (/^plan|planning|planned|replan/.test(s)) return 'planning';
  if (/done|closed|merged|complete|success|resolved/.test(s)) return 'done';
  if (/progress|running|active|working|in[ _-]?flight|started/.test(s)) return 'running';
  return 'pending';
}

function cssStatus(s) {
  return statusBucket(s);
}

async function refresh() {
  try {
    STATE = await loadState();
    render();
    expandFromHash();
    document.body.dataset.ready = '1';
  } catch (e) {
    console.error(e);
  }
}

function init() {
  $('#btn-demo').addEventListener('click', () => {
    const u = new URL(location.href);
    if (u.searchParams.has('demo')) u.searchParams.delete('demo');
    else u.searchParams.set('demo', '1');
    location.href = u.toString();
  });
  if (new URLSearchParams(location.search).has('demo')) $('#btn-demo').classList.add('on');

  // "Ask" copies a self-contained context block for the companion; task
  // headers toggle; a finding's task link expands the target task. Tabs switch
  // the visible panel and jump back to Tasks when a link points there.
  document.addEventListener('click', (e) => {
    const tab = e.target.closest('[data-tab]');
    if (tab) { showTab(tab.dataset.tab); return; }
    const ctx = e.target.closest('[data-ctx]');
    if (ctx) {
      const text = findingContext(ctx.dataset.ctx);
      copyText(text).then((ok) => flashBtn(ctx, ok ? 'Copied ✓' : 'Copy failed'));
      return;
    }
    const link = e.target.closest('a.chip.link[href^="#task-"]');
    if (link) {
      showTab('tasks');
      const art = document.getElementById(link.getAttribute('href').slice(1));
      if (art) { setExpanded(art, true); return; }
    }
    const head = e.target.closest('.task-head');
    if (head) toggleTask(head.closest('.card.task'));
  });

  document.addEventListener('keydown', (e) => {
    if ((e.key === 'Enter' || e.key === ' ') && e.target.classList?.contains('task-head')) {
      e.preventDefault();
      toggleTask(e.target.closest('.card.task'));
    }
  });

  window.addEventListener('resize', () => {
    if (STATE) drawAllEdges();
  });
  window.addEventListener('hashchange', () => { if (STATE) expandFromHash(); });

  applyTab();
  autoTimer = setInterval(refresh, AUTO_MS);
  refresh();
  // fonts/layout settle: redraw edges once more
  setTimeout(() => { if (STATE) drawAllEdges(); }, 250);
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      ta.remove();
      return ok;
    } catch { return false; }
  }
}

function flashBtn(btn, label) {
  const prev = btn.textContent;
  btn.textContent = label;
  btn.disabled = true;
  setTimeout(() => { btn.textContent = prev; btn.disabled = false; }, 1500);
}

// open/close a task card, remembering the choice across re-renders
function setExpanded(art, open) {
  if (!art) return;
  const id = art.dataset.task;
  art.classList.toggle('expanded', open);
  const head = art.querySelector('.task-head');
  if (head) head.setAttribute('aria-expanded', open ? 'true' : 'false');
  if (open) EXPANDED.add(id); else EXPANDED.delete(id);
  requestAnimationFrame(drawAllEdges); // the DAG inside may have just become visible
}
function toggleTask(art) {
  if (art) setExpanded(art, !art.classList.contains('expanded'));
}

// deep link: /web/#task-T-9 opens (and scrolls to) that task
function expandFromHash() {
  const m = /^#task-(.+)$/.exec(location.hash || '');
  if (!m) return;
  const art = document.getElementById('task-' + m[1]);
  if (!art) return;
  showTab('tasks');
  setExpanded(art, true);
  art.scrollIntoView({ block: 'start' });
}

if (typeof document !== 'undefined') {
  init();
} else if (typeof module !== 'undefined') {
  module.exports = {
    parsePlanSteps, normalizeFinding, deriveProvenance, findingSources, findingContext,
    _setState: (s) => { STATE = s; },
  };
}
