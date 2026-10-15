# AGENTS.md

Guidance for anyone (human or agent) changing this repo.

This repo is **CompAInion**: a single point of contact that dispatches work to
agents and keeps all state in plain files. Read [`README.md`](README.md) for the
full design. Skills live in `skills/`.

## The state files are a typed contract

`web/` is a read-only viewer of the companion's state. It has **no backend**:
the browser fetches the state files and renders them. The **shape of those
files is a contract** shared by the companion skills (the writers) and the web
(the reader).

> ⚠️ **Any change to the format of the state files can break `web/`.**
> When you change a field's name, type, or meaning — or add/remove fields the
> web relies on — you **must update the web in the same change**, consistently:
>
> 1. the reader logic in `web/app.js`,
> 2. the sample data in `web/sample/` (so the demo still renders),
> 3. this file, if the field map below changes.
>
> Never change a state file shape in `skills/` without doing the web side.

### Files the web reads

| file | produced by | fields the web consumes |
|---|---|---|
| `companion.json` | the user (gitignored) | `plansDir`, `companionPreferences` |
| `<plansDir>/status.json` | companion (only writer) | `version`, `tasks`, `pending`, `findings` |
| `<plansDir>/runs/<task-id>.json` | orchestrator | `version`, `task`, `steps` (id → sessionID), `findings` |
| `<plansDir>/reports/<task-id>[-<step-id>].json` | spawned agent | `version`, `outcome`, `evidence`, `findings`, `questions` |
| `<plansDir>/<plan-id>.md` | planner | best-effort parse of step ids, titles, `depends on`, `agent`, `checkpoint`, `writes` (plan headings like `### s1 — Title`, fields anywhere on a bullet) |

The web does **not** read `<plansDir>/prs/<task-id>.json` (the `gh` watcher's
last-seen PR fingerprints, written by `scripts/pr.mjs`); it is companion state,
not part of the contract. PR *links* the web shows come from `pr`/`prUrls` in
`status.json`.

Inside `status.json`, the web uses these fields. **The schema has more than one
shape in the wild**, and the web normalizes all of them with `stepStatus`,
`normalizeFinding`, `normalizeAsk` and `prSummary` in `app.js`:

- `tasks.<id>`: `title`, `status`, `repo`, `branch`, `plan`, `issue`, `worktree`,
  `pr` **or** `prUrls` (string array or `{repo: url}` map), `workArea`,
  `accessBlocker`, `writes[]`, `sessions{}` (also read from the loose
  `plannerSession` / `orchestratorSession` / `workerSession` keys), `kind`
  (optional), `updated`.
- `tasks.<id>.steps.<step-id>`: **a status string** (`"done"`) **or an object**
  `{ status, commit|commits, prUrl, summary, note, workerSession }`.
- `pending[]`: `id`, `task`, `kind` (`decision`/`finding`), `digest` **or**
  `question`, `options` (array **or** `/`-separated string), `recommended`,
  `finding`/`findings` (references), and `status`/`answer`. An ask with an
  `answer` or a resolved `status` (answered/resolved/closed/…) is **closed**;
  the web shows only open asks (`isOpenAsk`) and lists the rest under "Resolved".
  `task` may name several tasks (`T-7/T-8`); `taskRefMatches` attaches those.
- `findings[]`: `id`, `title`, `detail` **or** `evidence`, `severity`
  (`severe`/`minor`; missing ⇒ `minor`), `status`, `options`, and the
  **provenance** fields `task`, `step`, `agent`, `session`, `source` (all
  optional but recorded by the companion at ingest).
- `plan`: may be `x.md`, `plans/x.md`, or `x.md (rev 2)` — the web strips the
  `plansDir` prefix and a trailing parenthetical before fetching.

Status strings are matched by `statusBucket` (in `app.js`), which tolerates
descriptive values like `done → closed` or `blocked (push only)`. The canonical
vocabularies are: task
`pending|planning|awaiting_approval|running|blocked|done|failed|cancelled`;
step `pending|in_progress|done|failed|blocked`; finding
`new|asked|issue-opened|step-added|ignored`.

The web **derives** the paths of `runs/` and `reports/` files from the task id
and step ids — it cannot list directories. So a new per-task state file must be
reachable as a name built from the task id (or the page will not find it).

### State versioning

The state files carry a top-level integer `version`. **A file without one is
v0** (the pre-versioning format). The **current version is v1**.

- The companion writes `version: 1` in every state file it creates; the
  orchestrator sets it on run files and agents on report files.
- At boot the companion migrates older files forward and refuses to downgrade
  newer ones (see the migration list in `skills/companion-start/SKILL.md`).
- The web reads the version: `SUPPORTED_STATE_VERSION` in `app.js` is the
  highest it understands. v0 is tolerated silently; a `version` **greater** than
  that shows a warning banner instead of breaking.
- **Any change to a state field means bumping the version** and updating, in the
  same change: the migration list in `companion-start`, the READMEs, the web
  (`SUPPORTED_STATE_VERSION` and the reader), the sample, and this file.

### State schema & CLI

- `schema/*.schema.json` pin the state format (JSON Schema Draft 2020-12):
  `status`, `run`, `report`.
- `scripts/state.mjs` is a dependency-free validated CRUD CLI over `status.json`
  (`validate`, `init`, `migrate`, `task|finding|ask …`); it validates against the
  schema before every write and refuses to downgrade a newer version.
  `scripts/lib/jsonschema.mjs` is the small validator it uses (a documented
  subset — extend it if a schema uses a keyword it does not cover).
- `scripts/pr.mjs` is the `gh`-backed PR watcher: an agent that owns a PR runs it
  in the background instead of polling `gh pr view`, and is woken only when the
  PR changes (CI, review, conflict, commit, merge/close). It reads PR URLs from
  the task's `pr`/`prUrls` and keeps its last-seen fingerprint in
  `<plansDir>/prs/<task-id>.json` — companion state the web ignores, **not** part
  of the web contract; `merged --task <id>` tells the companion when a task's PRs
  are all merged so it can finish the task.
- Changing a schema and its migration are the same change. When you bump the
  version, bump `SUPPORTED_VERSION` in `scripts/state.mjs` **and**
  `SUPPORTED_STATE_VERSION` in `web/app.js`, and add the migration to
  `companion-start`.
- Smoke test: `node scripts/state.mjs validate --all --dir <plansDir>`.

### Traceability & Q&A

Every finding records its **provenance**: `task`, `step`, `agent` (role),
`session` (`sessionID`) and `source` (the report/run file it was ingested from).
The companion (`companion-start`) stamps these at ingest, and the orchestrator
tags step findings with `step`/`agent`/`session` in `runs/<task>.json`. The web
renders them as chips and links a finding to its task card.

For older state files that lack these fields, the web falls back to best-effort
reconstruction in `deriveProvenance` / `findingSources`: identifiers mentioned
in the finding text (`T-n`, `sN`, `ses_…`, worker tiers) and the source files it
can reach for that task/step.

Every finding also has an **Ask** button (`findingContext`) that copies a
self-contained context block — finding, provenance, source paths and referenced
task context — to paste back to the companion, so a worker can answer follow-up
questions when the recorded info is not enough.

If you add or rename provenance fields, update `normalizeFinding`,
`deriveProvenance`, `findingSources`, `findingContext` and the sample together.

### Activity states (derived, not recorded)

`status` alone does not separate "busy" from "waiting": a `running` task with an
open PR, an unanswered ask, or a stale `updated` is not progressing. `app.js`
derives an **effective state** in `effectiveState` from data already on disk:

- `waiting · you` — the task has open `pending` asks;
- `PR / review` — `running` with a PR (`pr`/`prUrls`), not updated recently;
- `active` — `running` and `updated` within `FRESH_MS` (10 min);
- `idle` — `running` but stale (no recent activity);
- `blocked` — recorded `blocked`, or all steps blocked with none running;
- plus the recorded `done`/`failed`/`cancelled`/`planning`/`awaiting_approval`.

It only reads; it never changes the state files, so it works on legacy v0 data.
If you ever want it **recorded** instead of derived, add a field (e.g. `activity`)
to the schema, bump the version and add a migration — see State versioning.

### Task card layout

When **collapsed** (the default), a task card shows only the title, its id and
the effective-status badge — nothing else. Expanding it reveals everything
inside `.task-body`: the plan **summary** (`task.summary` or `parsePlanSummary`)
and **repo/issue links** (`ghRepo`/`ghIssue`; PR links via `prLinkChips`); a
**Needs attention** block (`renderAttention`) when the effective state needs you
(`waiting`/`blocked`/`awaiting_approval`/`failed`/`review`), listing the open
asks, blocked steps and severe findings; then the meta, sessions, writes,
collapsible findings (`<details>`) and the steps DAG.

## Previewing the web

`fetch()` is blocked over `file://`, so serve the repo root:

```sh
./web/serve.sh          # then open http://localhost:8765/web/
./web/serve.sh 9000     # custom port
```

- Real state: <http://localhost:8765/web/>
- Sample data (no state needed): <http://localhost:8765/web/?demo=1>
- The page is split into three tabs — **Tasks**, **Open questions** (the open
  asks) and **Findings**. The active tab is remembered in `localStorage`; the
  page auto-refreshes every 10s (no header controls). Switch demo mode with the
  footer **Demo** link.
- Tasks are collapsed by default; click a task header to expand it. A
  `#task-<id>` hash (e.g. `…?demo=1#task-T-015`) expands and scrolls to that task.

The demo reads `web/sample/`, which mirrors the real layout and is the fixture
to keep in sync with the format.

## House rules

- Never commit `plans/`, `status.json`, or `companion.json` unless the user
  explicitly asks (`plans/` and `companion.json` are in `.gitignore`).
- `web/` is a static asset: no build step, no dependencies, vanilla
  HTML/CSS/JS. Keep it that way.
- Don't let the web become a source of truth. It only renders files; the
  companion skills own the state.
