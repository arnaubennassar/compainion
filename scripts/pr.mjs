#!/usr/bin/env node
// Companion PR helper: own a PR via `gh` without polling it from an agent loop.
//
// An agent that owns a PR does NOT waste tokens re-running `gh pr view`. It runs
// this script as a *background* task and goes idle. The watcher polls `gh` on a
// growing interval (10, 20, 30, … minutes), stays silent while nothing changes,
// and exits — which wakes the owning agent through its background-task
// notification — only when the PR needs a look: CI turned red/green, a review
// landed or requested changes, the branch conflicted, someone pushed, or the PR
// merged/closed. The agent reads the JSON summary and decides whether to act,
// then re-arms the watcher.
//
//   node scripts/pr.mjs watch --task T-n [--pr URL ...] [--interval 10] [--step 10]
//                             [--max 0] [--max-errors 6]
//                                   background poller; prints JSON and exits on a
//                                   trigger (0) or after --max minutes (3)
//   node scripts/pr.mjs status (--task T-n | --pr URL) [--once]
//                                   one-shot JSON snapshot (cheap, no store write)
//   node scripts/pr.mjs merged  (--task T-n | --pr URL) [--pr URL ...]
//                                   exit 0 iff every PR is merged (4 if settled but
//                                   some closed unmerged, 1 while any is open)
//   node scripts/pr.mjs open --repo <path> --head <branch> [--base main]
//                            [--title t] [--body b] [--fill] [--draft]
//                                   create the PR and print its URL
//   node scripts/pr.mjs selftest
//
// PR URLs come from `--pr` and/or the task's `pr`/`prUrls` in status.json, so a
// task's set of PRs stays the source of truth. A small per-task cache
// (<plansDir>/prs/<task-id>.json) holds the last-seen fingerprint used to detect
// changes; it is internal state, not part of the web contract.
//
// Global: --dir <plansDir>   (default: plansDir from ./companion.json, else plans/)

import { readFileSync, writeFileSync, renameSync, existsSync, mkdirSync } from 'node:fs';
import { join, dirname, resolve } from 'node:path';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const nowIso = () => new Date().toISOString();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/* ------------------------------------------------------------------ args */

const args = parseArgs(process.argv.slice(2));
const cmd = args._[0];

function parseArgs(argv) {
  const out = { _: [] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--') { out._.push(...argv.slice(i + 1)); break; }
    if (a.startsWith('--')) {
      const k = a.slice(2);
      const v = (i + 1 < argv.length && !argv[i + 1].startsWith('--')) ? argv[++i] : true;
      if (k in out) out[k] = Array.isArray(out[k]) ? [...out[k], v] : [out[k], v];
      else out[k] = v;
    } else out._.push(a);
  }
  return out;
}

function fail(msg) { console.error(`pr: ${msg}`); process.exit(1); }
function one(v) { return Array.isArray(v) ? v[v.length - 1] : v; }
function all(v) { return v === undefined || v === true ? [] : (Array.isArray(v) ? v : [v]); }
function num(v, d) { const n = Number(one(v)); return Number.isFinite(n) ? n : d; }
function print(obj) { process.stdout.write(JSON.stringify(obj, null, 2) + '\n'); }

/* ----------------------------------------------------------------- files */

function plansDir() {
  if (args.dir) return resolve(process.cwd(), String(args.dir));
  const cfg = join(process.cwd(), 'companion.json');
  if (existsSync(cfg)) {
    try { const c = JSON.parse(readFileSync(cfg, 'utf8')); if (c.plansDir) return resolve(process.cwd(), c.plansDir); } catch { /* ignore */ }
  }
  return resolve(process.cwd(), 'plans');
}

const DIR = plansDir();
const PRS_DIR = join(DIR, 'prs');

function readStatus() {
  const p = join(DIR, 'status.json');
  if (!existsSync(p)) return null;
  try { return JSON.parse(readFileSync(p, 'utf8')); } catch { return null; }
}

function extractUrl(x) {
  const m = /https?:\/\/[^\s)]+/.exec(String(x || ''));
  return m ? m[0] : null;
}

// PR URLs recorded on a task, tolerating the shapes in the wild:
// `pr` (string), `prUrls` (array of strings, or {repo: url} map).
function prUrlsOfTask(t) {
  const out = [];
  const add = (v, repo) => {
    const u = extractUrl(v);
    if (u && !out.some((o) => o.url === u)) out.push({ url: u, repo: repo || null });
  };
  if (!t) return out;
  if (t.pr) add(t.pr);
  const p = t.prUrls;
  if (Array.isArray(p)) p.forEach((v) => add(v));
  else if (p && typeof p === 'object') for (const [k, v] of Object.entries(p)) add(v, k);
  else if (typeof p === 'string') add(p);
  return out;
}

function storePath(key) {
  const safe = String(key).replace(/[^A-Za-z0-9._-]/g, '_') || 'pr';
  if (!existsSync(PRS_DIR)) mkdirSync(PRS_DIR, { recursive: true });
  return join(PRS_DIR, safe + '.json');
}
function loadStore(path) {
  if (!existsSync(path)) return { version: 1, prs: {} };
  try {
    const o = JSON.parse(readFileSync(path, 'utf8'));
    o.version = o.version || 1; o.prs = o.prs || {};
    return o;
  } catch { return { version: 1, prs: {} }; }
}
function saveStore(path, store) {
  store.version = store.version || 1;
  store.updated = nowIso();
  const tmp = path + '.tmp';
  writeFileSync(tmp, JSON.stringify(store, null, 2) + '\n');
  renameSync(tmp, path);
}

/* ------------------------------------------------------------------- gh */

const GH_FIELDS = [
  'state', 'isDraft', 'mergeable', 'mergeStateStatus', 'reviewDecision',
  'statusCheckRollup', 'headRefOid', 'updatedAt', 'mergedAt', 'closedAt',
  'title', 'number', 'url', 'reviews', 'comments',
];

function ghView(url) {
  try {
    const out = execFileSync('gh', ['pr', 'view', url, '--json', GH_FIELDS.join(',')], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 32 * 1024 * 1024,
    });
    return { ok: true, data: JSON.parse(out) };
  } catch (e) {
    const msg = (e && e.stderr ? String(e.stderr) : '') || (e && e.message) || 'gh failed';
    return { ok: false, error: msg.trim().split('\n').slice(-3).join(' ') };
  }
}

/* ------------------------------------------------------------ fingerprint */

const FAIL = new Set(['FAILURE', 'ERROR', 'TIMED_OUT', 'CANCELLED', 'STARTUP_FAILURE', 'ACTION_REQUIRED', 'STALE']);
const PENDING = new Set(['PENDING', 'QUEUED', 'IN_PROGRESS', 'WAITING', 'REQUESTED', 'EXPECTED', '']);
const bucket = (raw) => (FAIL.has(raw) ? 'fail' : PENDING.has(raw) ? 'pending' : 'pass');

function checksSummary(rollup) {
  const arr = Array.isArray(rollup) ? rollup : [];
  const failing = [], pending = [];
  for (const c of arr) {
    const name = c.name || c.context || c.workflowName || 'check';
    const raw = String(c.conclusion || c.state || c.status || '').toUpperCase();
    if (bucket(raw) === 'fail') failing.push(name);
    else if (bucket(raw) === 'pending') pending.push(name);
  }
  const agg = failing.length ? 'FAILURE' : pending.length ? 'PENDING' : arr.length ? 'SUCCESS' : 'NONE';
  return { agg, failing, pending, total: arr.length };
}

// The meaningful, change-detecting view of a PR. Deliberately excludes
// `updatedAt` so metadata noise (bots touching labels) does not wake agents.
function fingerprint(view) {
  const c = checksSummary(view.statusCheckRollup);
  return {
    state: view.state || '',
    isDraft: !!view.isDraft,
    mergeable: view.mergeable || '',
    mergeStateStatus: view.mergeStateStatus || '',
    reviewDecision: view.reviewDecision || '',
    checks: c.agg, failing: c.failing, pending: c.pending,
    headRefOid: view.headRefOid || '',
    nReviews: Array.isArray(view.reviews) ? view.reviews.length : 0,
    nComments: Array.isArray(view.comments) ? view.comments.length : 0,
    mergedAt: view.mergedAt || null, closedAt: view.closedAt || null,
  };
}
const sigOf = (fp) => createHash('sha1').update(JSON.stringify(fp)).digest('hex').slice(0, 16);

const isConflict = (fp) => fp.mergeable === 'CONFLICTING'
  || ['DIRTY', 'CONFLICTING'].includes(fp.mergeStateStatus);

// Compare the fresh fingerprint with the last-seen one. Returns the list of
// triggers ("what changed"). On the very first observation (no previous state)
// only real problems are reported, so a healthy new PR stays silent.
function diffTriggers(prev, fp) {
  const out = [];
  if (!prev) {
    if (fp.state === 'MERGED') return ['merged'];
    if (fp.state === 'CLOSED') return ['closed'];
    if (fp.checks === 'FAILURE') out.push('ci-failure');
    if (isConflict(fp)) out.push('conflict');
    if (fp.reviewDecision === 'CHANGES_REQUESTED') out.push('changes-requested');
    return out;
  }
  if (prev.state !== fp.state) out.push(fp.state === 'MERGED' ? 'merged' : fp.state === 'CLOSED' ? 'closed' : 'state');
  // Only terminal CI transitions (red/green) wake the owner; PENDING/NONE churn is noise.
  if (prev.checks !== fp.checks && ['FAILURE', 'SUCCESS'].includes(fp.checks)) out.push('ci-' + fp.checks.toLowerCase());
  if (prev.reviewDecision !== fp.reviewDecision) out.push('review-' + (fp.reviewDecision || 'none').toLowerCase().replace(/_/g, '-'));
  if (prev.mergeable !== fp.mergeable || prev.mergeStateStatus !== fp.mergeStateStatus) out.push('mergeability');
  if (prev.headRefOid !== fp.headRefOid) out.push('new-commit');
  if (prev.nComments !== fp.nComments) out.push('new-comment');
  if (prev.nReviews !== fp.nReviews) out.push('new-review');
  if (prev.isDraft !== fp.isDraft) out.push(fp.isDraft ? 'draft' : 'ready');
  return out;
}

/* ------------------------------------------------------------ snapshots */

function snapshot(target) {
  const r = ghView(target.url);
  if (!r.ok) return { url: target.url, repo: target.repo || null, error: r.error };
  const v = r.data;
  const fp = fingerprint(v);
  return {
    url: v.url || target.url, repo: target.repo || null,
    number: v.number, title: v.title,
    state: fp.state, isDraft: fp.isDraft,
    mergeable: fp.mergeable, mergeStateStatus: fp.mergeStateStatus,
    reviewDecision: fp.reviewDecision,
    checks: fp.checks, failing: fp.failing, pending: fp.pending,
    headRefOid: fp.headRefOid, nReviews: fp.nReviews, nComments: fp.nComments,
    mergedAt: fp.mergedAt, closedAt: fp.closedAt, updatedAt: v.updatedAt || null,
    signature: sigOf(fp), fingerprint: fp,
  };
}

function nextActions(results) {
  const acts = [];
  for (const r of results) {
    if (r.error) { acts.push(`${r.url}: gh error (${r.error})`); continue; }
    if (r.state === 'MERGED') { acts.push(`${r.url}: merged`); continue; }
    if (r.state === 'CLOSED') { acts.push(`${r.url}: closed without merge`); continue; }
    if (r.checks === 'FAILURE') acts.push(`${r.url}: CI failing (${(r.failing || []).join(', ') || 'unknown'})`);
    if (isConflict(r)) acts.push(`${r.url}: merge conflict`);
    if (r.reviewDecision === 'CHANGES_REQUESTED') acts.push(`${r.url}: changes requested`);
  }
  return acts;
}

function summary(reason, results, extra = {}) {
  return {
    ok: true, reason, settled: !!extra.settled, needsAttention: !!extra.needsAttention,
    prs: results.map((r) => ({
      url: r.url, repo: r.repo || null, number: r.number || null, title: r.title || null,
      state: r.state || null, isDraft: r.isDraft || false,
      mergeable: r.mergeable || null, mergeStateStatus: r.mergeStateStatus || null,
      reviewDecision: r.reviewDecision || null, checks: r.checks || null,
      failingChecks: r.failing || [], pendingChecks: r.pending || [],
      triggers: r.triggers || [], error: r.error || null,
    })),
    nextActions: nextActions(results),
  };
}

/* ------------------------------------------------------------- targets */

// Collect the PR URLs to look at: those recorded on the task in status.json,
// those already known to the watcher's cache for that task, and any explicit
// `--pr`. Explicit first, then task + cache; deduped by URL.
function collectTargets() {
  const seen = new Map();
  const add = (v) => {
    const url = extractUrl(v) || (/^https?:\/\//.test(String(v || '')) ? String(v) : null);
    if (url && !seen.has(url)) seen.set(url, { url, repo: null });
  };
  for (const p of all(args.pr)) add(p);

  const task = one(args.task);
  if (task) {
    const status = readStatus();
    const t = status && status.tasks ? status.tasks[task] : null;
    if (!t) console.error(`pr: no task ${task} in ${join(DIR, 'status.json')} (using --pr only)`);
    for (const { url, repo } of prUrlsOfTask(t)) {
      if (!seen.has(url)) seen.set(url, { url, repo });
      else if (!seen.get(url).repo) seen.get(url).repo = repo;
    }
    const store = loadStore(storePath(task));
    for (const [url, rec] of Object.entries(store.prs || {})) {
      if (!seen.has(url)) seen.set(url, { url, repo: rec.repo || null });
    }
  }
  return { task, list: [...seen.values()] };
}

function storeKeyFor(task, list) {
  if (task) return task;
  return 'pr-' + createHash('sha1').update(list[0].url).digest('hex').slice(0, 10);
}

/* ------------------------------------------------------------ commands */

// watch: the wake primitive. Blocks, polls, exits on a change (0), on timeout
// (3, only with --max) or on a hard error (1). Prints a JSON summary.
async function cmdWatch() {
  const { task, list } = collectTargets();
  if (!list.length) fail('no PRs to watch: pass --pr URL or a --task whose pr/prUrls are in status.json');
  const path = storePath(storeKeyFor(task, list));
  const store = loadStore(path);

  const base = Math.max(0.05, num(args.interval, 10));   // minutes
  const step = Math.max(0, num(args.step, 10));          // minutes added per cycle
  const cap = 30;                                        // minutes between checks
  const maxMin = Math.max(0, num(args.max, 0));          // 0 = unbounded
  const maxErrors = Math.max(1, num(args['max-errors'], 6));

  let interval = base, elapsed = 0, consecutiveErrors = 0;
  for (;;) {
    const results = list.map((t) => {
      const snap = snapshot(t);
      if (snap.error) return snap;
      const prev = store.prs[snap.url] || null;
      snap.triggers = diffTriggers(prev ? prev.fingerprint : null, snap.fingerprint);
      return snap;
    });

    const failed = results.filter((r) => r.error);
    if (failed.length === results.length) {
      consecutiveErrors++;
      if (consecutiveErrors >= maxErrors) {
        saveStore(path, store);
        print(summary('error', results));
        return 1;
      }
    } else consecutiveErrors = 0;

    // Persist the fresh baseline for every PR that answered.
    for (const r of results) {
      if (r.error) continue;
      const { fingerprint: fp, ...rest } = r;
      store.prs[r.url] = { ...rest, fingerprint: fp, seenAt: nowIso() };
    }
    const live = results.filter((r) => !r.error);
    const settled = live.length === results.length && live.every((r) => ['MERGED', 'CLOSED'].includes(r.state));
    const triggered = live.filter((r) => (r.triggers || []).length);

    if (settled) {
      saveStore(path, store);
      print(summary('settled', results, { settled: true, needsAttention: live.some((r) => r.state === 'CLOSED') }));
      return 0;
    }
    if (triggered.length) {
      saveStore(path, store);
      print(summary('change', results, { needsAttention: true }));
      return 0;
    }

    saveStore(path, store);
    if (maxMin && elapsed + interval > maxMin) {
      print(summary('timeout', results));
      return 3;
    }
    await sleep(interval * 60000);
    elapsed += interval;
    interval = Math.min(interval + step, cap);
  }
}

// status: one-shot, read-only snapshot (does not update the cache).
function cmdStatus() {
  const { list } = collectTargets();
  if (!list.length) fail('no PRs: pass --pr URL or a --task whose pr/prUrls are in status.json');
  const store = loadStore(storePath(storeKeyFor(one(args.task), list)));
  const results = list.map((t) => {
    const snap = snapshot(t);
    const prev = store.prs[snap.url] || null;
    if (!snap.error) snap.triggers = diffTriggers(prev ? prev.fingerprint : null, snap.fingerprint);
    return snap;
  });
  if (results.every((r) => r.error)) { print(summary('error', results)); return 1; }
  print(summary('status', results, { needsAttention: results.some((r) => (r.triggers || []).length) }));
  return 0;
}

// merged: can the task be finished? exit 0 all merged, 4 settled (some closed
// unmerged), 1 still open, 2 gh error.
function cmdMerged() {
  const { task, list } = collectTargets();
  if (!list.length) fail('no PRs: pass --pr URL or a --task whose pr/prUrls are in status.json');
  const results = list.map((t) => snapshot(t));
  if (results.every((r) => r.error)) { print(summary('error', results)); return 2; }
  const live = results.filter((r) => !r.error);
  const errors = results.filter((r) => r.error);
  const open = live.filter((r) => !['MERGED', 'CLOSED'].includes(r.state));
  const closedUnmerged = live.filter((r) => r.state === 'CLOSED');
  print({
    ok: true,
    task: task || null,
    allMerged: errors.length === 0 && live.length > 0 && live.every((r) => r.state === 'MERGED'),
    open: open.map((r) => ({ url: r.url, state: r.state, mergeable: r.mergeable })),
    closedUnmerged: closedUnmerged.map((r) => r.url),
    errors: errors.map((r) => ({ url: r.url, error: r.error })),
  });
  if (errors.length) return 2;   // unknown state: never claim the task is done
  if (open.length) return 1;
  if (closedUnmerged.length) return 4;
  return 0;
}

function cmdOpen() {
  const repo = one(args.repo);
  if (!repo) fail('--repo <path> required');
  const ghArgs = ['pr', 'create'];
  if (args.base) ghArgs.push('--base', one(args.base));
  if (args.head) ghArgs.push('--head', one(args.head));
  if (args.title) ghArgs.push('--title', one(args.title));
  if (args.body) ghArgs.push('--body', one(args.body));
  if (args.draft) ghArgs.push('--draft');
  if (args.fill || !args.title) ghArgs.push('--fill');
  try {
    const out = execFileSync('gh', ghArgs, { cwd: String(repo), encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
    const url = extractUrl(out);
    print({ ok: true, url, repo: String(repo) });
    return url ? 0 : 1;
  } catch (e) {
    const msg = (e && e.stderr ? String(e.stderr) : '') || (e && e.message) || 'gh pr create failed';
    console.error(`pr: ${msg.trim()}`);
    return 1;
  }
}

/* --------------------------------------------------------------- selftest */

function selftest() {
  const eq = (a, b, what) => { if (JSON.stringify(a) !== JSON.stringify(b)) fail(`selftest: ${what}: ${JSON.stringify(a)} != ${JSON.stringify(b)}`); };
  eq(extractUrl('https://github.com/a/b/pull/12 (open, CI green)'), 'https://github.com/a/b/pull/12', 'extractUrl/paren');
  eq(extractUrl('no url here'), null, 'extractUrl/none');
  eq(prUrlsOfTask({ pr: 'https://github.com/a/b/pull/1' }), [{ url: 'https://github.com/a/b/pull/1', repo: null }], 'prUrlsOfTask/pr');
  eq(prUrlsOfTask({ prUrls: { 'a': 'https://github.com/a/b/pull/1 (open)' } }), [{ url: 'https://github.com/a/b/pull/1', repo: 'a' }], 'prUrlsOfTask/map');
  eq(prUrlsOfTask({ prUrls: ['https://github.com/a/b/pull/1', 'https://github.com/a/b/pull/2'] }).length, 2, 'prUrlsOfTask/array');

  eq(checksSummary([{ conclusion: 'SUCCESS' }, { state: 'FAILURE', context: 'build' }]).agg, 'FAILURE', 'checks/failure');
  eq(checksSummary([{ conclusion: 'SUCCESS' }]).agg, 'SUCCESS', 'checks/success');
  eq(checksSummary([{ status: 'IN_PROGRESS' }]).agg, 'PENDING', 'checks/pending');
  eq(checksSummary([]).agg, 'NONE', 'checks/none');

  const open = { state: 'OPEN', mergeable: 'MERGEABLE', reviewDecision: '', statusCheckRollup: [{ conclusion: 'SUCCESS' }], headRefOid: 'a', comments: [], reviews: [] };
  const fp = fingerprint(open);
  eq(diffTriggers(null, fp), [], 'diff/healthy-first');
  eq(diffTriggers(null, fingerprint({ ...open, statusCheckRollup: [{ conclusion: 'FAILURE', name: 'build' }] })), ['ci-failure'], 'diff/first-ci');
  eq(diffTriggers(null, fingerprint({ ...open, mergeable: 'CONFLICTING' })), ['conflict'], 'diff/first-conflict');
  eq(diffTriggers(null, fingerprint({ ...open, state: 'MERGED', mergedAt: 'x' })), ['merged'], 'diff/first-merged');
  eq(diffTriggers(null, fingerprint({ ...open, state: 'CLOSED', closedAt: 'x' })), ['closed'], 'diff/first-closed');
  const prev = fp;
  eq(diffTriggers(prev, fingerprint({ ...open, reviewDecision: 'CHANGES_REQUESTED' })), ['review-changes-requested'], 'diff/review');
  eq(diffTriggers(prev, fingerprint({ ...open, state: 'MERGED', mergedAt: 'x' })), ['merged'], 'diff/merged');
  eq(diffTriggers(prev, fingerprint({ ...open, comments: [1, 2] })), ['new-comment'], 'diff/comment');
  eq(diffTriggers(prev, fingerprint({ ...open, headRefOid: 'b' })), ['new-commit'], 'diff/commit');
  eq(diffTriggers(prev, fingerprint({ ...open, statusCheckRollup: [{ conclusion: 'FAILURE', name: 'build' }] })), ['ci-failure'], 'diff/ci-failure');
  eq(diffTriggers(prev, fingerprint({ ...open, statusCheckRollup: [{ status: 'IN_PROGRESS' }] })), [], 'diff/ci-pending-silent');
  eq(sigOf(fp) === sigOf(fingerprint({ ...open })), true, 'sig/stable');

  console.log('pr: selftest ok');
}

/* -------------------------------------------------------------- dispatch */

const commands = {
  watch: cmdWatch,
  status: cmdStatus,
  merged: cmdMerged,
  open: cmdOpen,
  selftest: () => { selftest(); return 0; },
};

function usage() {
  console.log(`Companion PR helper

  watch   --task T-n [--pr URL ...] [--interval 10] [--step 10] [--max 0]
          background poller; JSON + exit 0 on a trigger, 3 on timeout, 1 on error
  status  --task T-n | --pr URL        one-shot JSON snapshot
  merged  --task T-n | --pr URL ...    exit 0 all merged, 4 settled-unmerged, 1 open
  open    --repo <path> --head <b> [--base main] [--title t] [--body b] [--fill] [--draft]
  selftest                             run the pure-logic self-test

  Global: --dir <plansDir>   default: plansDir from ./companion.json, else ./plans`);
}

if (!cmd || cmd === 'help' || cmd === '-h' || cmd === '--help') { usage(); process.exit(0); }
if (!commands[cmd]) fail(`unknown command "${cmd}" (try: help)`);
const code = await commands[cmd]();
process.exit(code || 0);
