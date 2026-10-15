#!/usr/bin/env node
// Companion state CLI: validated CRUD over the state files.
//
//   node scripts/state.mjs <command> [--flags]
//
// Every mutating command loads the state, applies the change, validates the
// whole document against schema/status.schema.json, and only then writes it
// back atomically (temp file + rename). A newer state version is never
// downgraded; a v0 file (no `version`) is migrated forward on write.
//
//   validate [--all]                 validate status.json (and runs/reports)
//   init                             create an empty v1 status.json
//   migrate                          bring status.json up to the current version
//
//   task add   --title T [--id T-n] [--status s] [--kind k] [--repo r]
//              [--branch b] [--plan p] [--worktree w] [--issue i]
//              [--writes a,b] [--pr url] [--pr-urls a,b] [--session role=ses]
//              [--updated iso]
//   task set   --id T-n [--status s] [--title T] [...same fields...]
//   task rm    --id T-n
//   task step  --id T-n --step s2 [--status done] [--commit c] [--session ses]
//              [--summary s] [--note n]
//
//   finding add --title T [--id F-n] [--severity severe|minor] [--status s]
//              [--task T-n] [--step s2] [--agent role] [--session ses]
//              [--source path] [--detail d] [--evidence e] [--options a,b]
//   finding set --id F-n [...fields...]
//   finding rm  --id F-n
//
//   ask add    (--digest d | --question q) [--id Q-n] [--kind decision|finding]
//              [--task T-n] [--options a,b] [--recommended x] [--finding F-n]
//   ask rm     --id Q-n        (resolve/answer by removing from pending)
//
// Global: --dir <plansDir>   (default: plansDir from ./companion.json, else plans/)

import { readFileSync, writeFileSync, renameSync, existsSync, readdirSync } from 'node:fs';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { validate } from './lib/jsonschema.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const SCHEMA_DIR = resolve(HERE, '..', 'schema');
const SUPPORTED_VERSION = 1;
const nowIso = () => new Date().toISOString();

const schemas = {
  status: JSON.parse(readFileSync(join(SCHEMA_DIR, 'status.schema.json'), 'utf8')),
  run: JSON.parse(readFileSync(join(SCHEMA_DIR, 'run.schema.json'), 'utf8')),
  report: JSON.parse(readFileSync(join(SCHEMA_DIR, 'report.schema.json'), 'utf8')),
};

/* ------------------------------------------------------------------ args */

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

const args = parseArgs(process.argv.slice(2));
const cmd = args._[0];
const sub = args._[1];

function fail(msg) { console.error(`state: ${msg}`); process.exit(1); }
function one(v) { return Array.isArray(v) ? v[v.length - 1] : v; }
function list(v) {
  if (v === undefined || v === true) return [];
  const raw = Array.isArray(v) ? v : [v];
  return raw.flatMap((x) => String(x).split(',')).map((s) => s.trim()).filter(Boolean);
}
function kvObject(v) {
  const parts = list(v);
  const out = {};
  for (const p of parts) { const [k, val] = p.split('='); if (k && val) out[k] = val; }
  return out;
}

/* ------------------------------------------------------------------ files */

function plansDir() {
  if (args.dir) return resolve(process.cwd(), String(args.dir));
  const cfg = join(process.cwd(), 'companion.json');
  if (existsSync(cfg)) {
    try { const c = JSON.parse(readFileSync(cfg, 'utf8')); if (c.plansDir) return resolve(process.cwd(), c.plansDir); } catch { /* ignore */ }
  }
  return resolve(process.cwd(), 'plans');
}

const DIR = plansDir();
const STATUS_PATH = join(DIR, 'status.json');

function readJson(path) { return JSON.parse(readFileSync(path, 'utf8')); }
function writeJsonAtomic(path, obj) {
  const tmp = path + '.tmp';
  writeFileSync(tmp, JSON.stringify(obj, null, 2) + '\n');
  renameSync(tmp, path);
}

/* ------------------------------------------------------------- validation */

function statusErrors(obj, { relaxed = false } = {}) {
  let schema = schemas.status;
  if (relaxed) {
    schema = JSON.parse(JSON.stringify(schema));
    schema.required = schema.required.filter((r) => r !== 'version');
  }
  return validate(obj, schema);
}

function assertValid(obj) {
  const errs = statusErrors(obj);
  if (errs.length) {
    console.error('state: refusing to write, status.json is invalid:');
    for (const e of errs.slice(0, 25)) console.error(`  ${e.path}: ${e.message}`);
    if (errs.length > 25) console.error(`  … and ${errs.length - 25} more`);
    process.exit(1);
  }
}

function checkVersion(obj) {
  const v = obj.version === undefined ? 0 : Number(obj.version);
  if (Number.isInteger(v) && v > SUPPORTED_VERSION) {
    fail(`status.json is v${v}, newer than this tool (supports v${SUPPORTED_VERSION}); refusing to touch it`);
  }
  return Number.isInteger(v) ? v : 0;
}

// load status, migrating a v0 file forward to v1 in memory
function loadStatus({ mustExist = true } = {}) {
  if (!existsSync(STATUS_PATH)) {
    if (mustExist) fail(`no status.json at ${STATUS_PATH} (run: state init)`);
    return null;
  }
  const obj = readJson(STATUS_PATH);
  const v = checkVersion(obj);
  if (v < SUPPORTED_VERSION) {
    obj.version = SUPPORTED_VERSION; // v0 -> v1: add the version field
    obj.tasks = obj.tasks || {};
    obj.pending = obj.pending || [];
    obj.findings = obj.findings || [];
  }
  return obj;
}

/* ---------------------------------------------------------------- ids */

const nums = (ids, prefix) => ids
  .map((x) => { const m = new RegExp(`^${prefix}-(\\d+)$`).exec(x); return m ? Number(m[1]) : 0; })
  .reduce((a, b) => Math.max(a, b), 0);
const nextTaskId = (s) => `T-${nums(Object.keys(s.tasks), 'T') + 1}`;
const nextFindingId = (s) => `F-${nums(s.findings.map((f) => f.id), 'F') + 1}`;
const nextAskId = (s) => `Q-${nums(s.pending.map((a) => a.id), 'Q') + 1}`;

/* -------------------------------------------------------------- commands */

function save(s) {
  assertValid(s);
  writeJsonAtomic(STATUS_PATH, { version: s.version, ...s }); // version first, for readability
}

function applyTaskFields(t, a) {
  for (const f of ['title', 'status', 'kind', 'plan', 'repo', 'branch', 'worktree', 'issue', 'workArea']) {
    if (a[f] !== undefined) t[f] = a[f];
  }
  if (a.writes !== undefined) t.writes = list(a.writes);
  if (a.pr !== undefined) t.pr = one(a.pr);
  if (a['pr-urls'] !== undefined) t.prUrls = list(a['pr-urls']);
  const sess = kvObject(a.session);
  if (Object.keys(sess).length) t.sessions = { ...(t.sessions || {}), ...sess };
}

const commands = {
  init() {
    if (existsSync(STATUS_PATH) && !args.force) fail(`${STATUS_PATH} already exists (use --force)`);
    const s = { version: SUPPORTED_VERSION, tasks: {}, pending: [], findings: [] };
    save(s);
    console.log(`state: initialized v${SUPPORTED_VERSION} at ${STATUS_PATH}`);
  },

  migrate() {
    const obj = readJson(STATUS_PATH);
    const v = checkVersion(obj);
    if (v === SUPPORTED_VERSION) { console.log(`state: already v${SUPPORTED_VERSION}`); return; }
    obj.version = SUPPORTED_VERSION;
    obj.tasks = obj.tasks || {}; obj.pending = obj.pending || []; obj.findings = obj.findings || [];
    save(obj);
    console.log(`state: migrated v${v} -> v${SUPPORTED_VERSION}`);
  },

  validate() {
    if (args.all) return validateAll();
    const obj = readJson(STATUS_PATH);
    const v = obj.version === undefined ? 0 : Number(obj.version);
    if (v > SUPPORTED_VERSION) fail(`status.json is v${v}, newer than this tool (v${SUPPORTED_VERSION})`);
    const errs = statusErrors(obj, { relaxed: v === 0 });
    if (v === 0) console.log('state: v0 (no version field; legacy, tolerated)');
    if (!errs.length) { console.log(`state: ${STATUS_PATH} valid (v${v || 0})`); return; }
    console.error(`state: ${STATUS_PATH} INVALID (v${v || 0}):`);
    for (const e of errs) console.error(`  ${e.path}: ${e.message}`);
    process.exit(1);
  },

  task() {
    const s = loadStatus();
    if (sub === 'add') {
      const id = one(args.id) || nextTaskId(s);
      if (s.tasks[id]) fail(`task ${id} already exists`);
      const t = { title: one(args.title) || fail('--title required'), status: one(args.status) || 'pending' };
      applyTaskFields(t, args);
      t.updated = one(args.updated) || nowIso();
      s.tasks[id] = t;
      save(s);
      console.log(`state: task ${id} added (${t.status})`);
    } else if (sub === 'set') {
      const id = one(args.id) || fail('--id required');
      const t = s.tasks[id] || fail(`no task ${id}`);
      applyTaskFields(t, args);
      t.updated = one(args.updated) || nowIso();
      save(s);
      console.log(`state: task ${id} updated`);
    } else if (sub === 'rm') {
      const id = one(args.id) || fail('--id required');
      if (!s.tasks[id]) fail(`no task ${id}`);
      delete s.tasks[id];
      save(s);
      console.log(`state: task ${id} removed`);
    } else if (sub === 'step') {
      const id = one(args.id) || fail('--id required');
      const sid = one(args.step) || fail('--step required');
      const t = s.tasks[id] || fail(`no task ${id}`);
      const prev = (t.steps || {})[sid];
      const obj = (prev && typeof prev === 'object') ? { ...prev } : {};
      obj.status = one(args.status) || (typeof prev === 'string' ? prev : 'pending');
      if (args.commit !== undefined) obj.commit = one(args.commit);
      if (args.session !== undefined) obj.workerSession = one(args.session);
      if (args.summary !== undefined) obj.summary = one(args.summary);
      if (args.note !== undefined) obj.note = one(args.note);
      obj.updated = nowIso();
      t.steps = { ...(t.steps || {}), [sid]: obj };
      t.updated = nowIso();
      save(s);
      console.log(`state: task ${id} step ${sid} -> ${obj.status}`);
    } else fail('task <add|set|rm|step>');
  },

  finding() {
    const s = loadStatus();
    if (sub === 'add') {
      const id = one(args.id) || nextFindingId(s);
      if (s.findings.some((f) => f.id === id)) fail(`finding ${id} already exists`);
      const f = { id, title: one(args.title) || fail('--title required'), status: one(args.status) || 'new' };
      for (const k of ['severity', 'task', 'step', 'agent', 'session', 'source', 'detail', 'evidence', 'where']) {
        if (args[k] !== undefined) f[k] = one(args[k]);
      }
      if (args.options !== undefined) f.options = list(args.options);
      f.updated = nowIso();
      s.findings.push(f);
      save(s);
      console.log(`state: finding ${id} added (${f.severity || 'minor'})`);
    } else if (sub === 'set') {
      const id = one(args.id) || fail('--id required');
      const f = s.findings.find((x) => x.id === id) || fail(`no finding ${id}`);
      for (const k of ['title', 'status', 'severity', 'task', 'step', 'agent', 'session', 'source', 'detail', 'evidence', 'where']) {
        if (args[k] !== undefined) f[k] = one(args[k]);
      }
      if (args.options !== undefined) f.options = list(args.options);
      f.updated = nowIso();
      save(s);
      console.log(`state: finding ${id} updated`);
    } else if (sub === 'rm') {
      const id = one(args.id) || fail('--id required');
      const before = s.findings.length;
      s.findings = s.findings.filter((f) => f.id !== id);
      if (s.findings.length === before) fail(`no finding ${id}`);
      save(s);
      console.log(`state: finding ${id} removed`);
    } else fail('finding <add|set|rm>');
  },

  ask() {
    const s = loadStatus();
    if (sub === 'add') {
      const id = one(args.id) || nextAskId(s);
      if (s.pending.some((a) => a.id === id)) fail(`ask ${id} already exists`);
      const a = { id, kind: one(args.kind) || 'decision' };
      if (args.task !== undefined) a.task = one(args.task);
      if (args.digest !== undefined) a.digest = one(args.digest);
      if (args.question !== undefined) a.question = one(args.question);
      if (!a.digest && !a.question) fail('need --digest or --question');
      if (args.options !== undefined) a.options = list(args.options);
      if (args.recommended !== undefined) a.recommended = one(args.recommended);
      if (args.finding !== undefined) a.finding = one(args.finding);
      if (args.findings !== undefined) a.findings = list(args.findings);
      a.updated = nowIso();
      s.pending.push(a);
      save(s);
      console.log(`state: ask ${id} added (${a.kind})`);
    } else if (sub === 'rm' || sub === 'resolve') {
      const id = one(args.id) || fail('--id required');
      const before = s.pending.length;
      s.pending = s.pending.filter((a) => a.id !== id);
      if (s.pending.length === before) fail(`no ask ${id}`);
      save(s);
      console.log(`state: ask ${id} removed`);
    } else fail('ask <add|rm>');
  },

  help() { printUsage(); },
};

function validateAll() {
  let bad = 0, ok = 0;
  const check = (path, schema, { relaxed = false } = {}) => {
    const obj = readJson(path);
    let sch = schema;
    if (relaxed) { sch = JSON.parse(JSON.stringify(schema)); if (sch.required) sch.required = sch.required.filter((r) => r !== 'version'); }
    const errs = validate(obj, sch);
    if (errs.length) {
      bad++;
      console.error(`INVALID ${path}`);
      for (const e of errs.slice(0, 15)) console.error(`  ${e.path}: ${e.message}`);
    } else ok++;
  };
  const v0 = (obj) => obj.version === undefined;
  check(STATUS_PATH, schemas.status, { relaxed: v0(readJson(STATUS_PATH)) });
  for (const [dir, schema, name] of [[join(DIR, 'runs'), schemas.run, 'run'], [join(DIR, 'reports'), schemas.report, 'report']]) {
    if (!existsSync(dir)) continue;
    for (const f of readdirSync(dir).filter((x) => x.endsWith('.json'))) {
      const p = join(dir, f);
      check(p, schema, { relaxed: v0(readJson(p)) });
    }
  }
  console.log(`state: ${ok} valid, ${bad} invalid`);
  if (bad) process.exit(1);
}

function printUsage() {
  console.log(`Companion state CLI — validated CRUD over ${STATUS_PATH}

  validate [--all]     validate status.json (and runs/reports)
  init                 create an empty v1 status.json
  migrate              bring status.json up to the current version
  task add|set|rm|step ...
  finding add|set|rm ...
  ask add|rm ...

  --dir <plansDir>     default: plansDir from ./companion.json, else ./plans
Run "node scripts/state.mjs help" for the full command list (top of the file).`);
}

/* ---------------------------------------------------------------- dispatch */

if (!cmd || cmd === 'help' || cmd === '-h' || cmd === '--help') { printUsage(); process.exit(0); }
if (!commands[cmd]) fail(`unknown command "${cmd}" (try: help)`);
commands[cmd]();
