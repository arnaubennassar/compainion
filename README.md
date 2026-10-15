# CompAInion

A single point of contact for all your workstreams. The companion keeps the human focused by asking one thing at a time, digesting context, suggesting answers, and surfacing issues, while all real work is delegated to agents.

## Principles

- **One session, no backend.** Every agent is spawned from the companion's session. State lives in plain files in the repo, nothing runs besides the harness.
- **Companion never works.** It talks to the user and dispatches. Even "how is task X going?" is answered by a spawned worker that reads the files.
- **Files are the source of truth.** Plans are `.md`, status is `.json`. Anything needed later must be on disk, so the companion context can be cleared at any time.
- **Thin skills.** Each skill is a short contract; behavior comes from the files they read, not from long prompts.

## What the companion does

- **Sequencing:** one ask at a time, related asks batched to limit context switching.
- **Digesting:** short, standalone context in the user's preferred tone and language.
- **Suggesting:** every ask carries at least one suggestion. The user can accept, accept with a comment, answer freely, or ask for details. The companion may push back on inconsistent answers.
- **Steering:** answers obvious stalls (`yes`, `continue`) on its own and logs it. Anything involving a decision, scope, cost or destructive action goes to the user.
- **Learning:** when the user states a style preference in passing ("summarize in under 200 words"), the companion answers, then asks whether to make it permanent. Only on a yes does it update `companionPreferences` in `companion.json`.
- **Surfacing:** agents write issues found off the main path (bugs, noisy logs, misleading docs) to their report files; the companion ingests them, triages severity, and turns each into an ask — severe findings are presented immediately with a notification, minor ones queue for the next interaction — with options: open an issue, add a step to the plan, ignore.

## Agents

| agent | spawned by | job |
|---|---|---|
| companion | user | talk to the user, dispatch, track status |
| planner | companion | write a plan `.md` for non-trivial work |
| orchestrator | companion | execute one approved plan, delegating steps to workers |
| worker-low | companion or orchestrator | execute one simple, mechanical task or step |
| worker-medium | companion or orchestrator | execute one typical task or step |
| worker-high | companion or orchestrator | execute one hard task or step (ambiguous, risky, cross-cutting) |

Dispatch:

- Small, well scoped request: a **worker** directly, tier chosen by the companion (`worker-low`, `worker-medium` or `worker-high`) by difficulty.
- Several steps, unclear scope, or multiple areas: **planner**, user approves the plan, then one **orchestrator** per plan.
- Workers never spawn agents. Orchestrators never edit the goal without user approval.
- Spawn mode: every agent as a **background subagent**, its child `sessionID` recorded at spawn time (task `sessions` map; step workers in `<plansDir>/runs/<task-id>.json`); foreground only for short in-turn lookups. See [Agent sessions](#agent-sessions-sessions-and-plansdrrunsplan-idjson).

## Files

### `companion.json`

Single local config at the repo root, gitignored. Copy `companion.example.json` to `companion.json` to start.

```json
{
  "models": {
    "companion": ["openrouter/glm-flash", "anthropic/sonnet/medium", "openai/terra/medium"],
    "planner": ["openrouter/opus/medium", "anthropic/opus/medium", "openai/sol/high"],
    "orchestrator": ["openrouter/sonnet/medium", "anthropic/sonnet/medium", "openai/sol/medium"],
    "worker-low": ["openrouter/glm-flash", "openrouter/deepseek-flash", "anthropic/sonnet/medium", "openai/terra/medium"],
    "worker-medium": ["openrouter/glm-flash", "openrouter/deepseek-flash", "anthropic/sonnet/medium", "openai/terra/medium"],
    "worker-high": ["openrouter/glm", "anthropic/opus/medium", "openai/sol/medium"]
  },
  "plansDir": "plans/",
  "companionPreferences": {
    "tone": "concise, friendly",
    "language": "en",
    "askStyle": "one question at a time, always with a recommended option",
    "autoSteer": true
  }
}
```

- `models`: per agent type, an ordered list of `provider/model[/effort]` entries. Model names are intentionally versionless (`sonnet`, not `sonnet-5.5`): the harness resolves each to the newest version the provider offers. Effort is optional and is the last segment only when it is one of `low`, `medium`, `high`, `xhigh`, `max` (e.g. `openai/sol/medium`); without it the harness default applies. The model part may itself contain slashes. The harness tries entries in order and uses the first whose provider is available and whose model is found. If none of the entries can be found, the user is shown the available alternatives and picks one.
- `plansDir`: where plans and status live.
- `companionPreferences`: free-form guidance on how the companion talks to the user. The companion reads it at the start of every interaction.

### Plans: `<plansDir>/<plan-id>.md`

Written by the planner, executed by the orchestrator. Sections:

- **Summary:** what, why, how.
- **Goals:** including expected output.
- **Acceptance criteria:** preferably executable checks (tests, commands), added to CI when relevant.
- **Scope / non-goals:** what may be read and, explicitly, the paths that may be written (repo + dirs/files/globs), plus what is off limits. The companion uses the write paths to prevent collisions.
- **Steps:** each with id, title, self-contained description, dependencies, acceptance criteria, narrower scope (a `writes` subset of the plan's write paths), and the assigned worker agent (`worker-low`, `worker-medium` or `worker-high`, chosen by difficulty), and optional `checkpoint` (user review before moving on). Steps must fit a single worker context and form a DAG ending in the goal. The last step before the goal verifies the plan acceptance criteria end to end.

### Status: `<plansDir>/status.json`

Owned by the companion, the only writer. Agents report outcomes to the companion, which records them.

```json
{
  "version": 1,
  "tasks": {
    "T-014": {
      "title": "Bump linter in repo X",
      "kind": "worker",
      "status": "done",
      "plan": null,
      "writes": ["repo-x/.golangci.yml"],
      "worktree": "plans/worktrees/T-014",
      "sessions": { "worker": "ses_..." },
      "updated": "2026-10-13T10:02:00Z"
    },
    "T-015": {
      "title": "Feature Y across A and B",
      "kind": "plan",
      "status": "running",
      "plan": "feature-y.md",
      "writes": ["repo-a/src/y/", "repo-b/api/"],
      "worktree": "plans/worktrees/T-015",
      "sessions": { "planner": "ses_...", "orchestrator": "ses_..." },
      "steps": { "s1": "done", "s2": "in_progress", "s3": "pending" },
      "updated": "2026-10-13T10:40:00Z"
    }
  },
  "pending": [
    { "id": "Q-3", "task": "T-015", "kind": "decision", "digest": "...", "options": ["..."] },
    { "id": "Q-4", "task": "T-015", "kind": "finding", "finding": "F-2", "digest": "...", "options": ["open an issue", "add a step to the plan", "ignore"] }
  ],
  "findings": [
    { "id": "F-2", "task": "T-015", "step": "s2", "agent": "worker-medium", "session": "ses_...", "source": "reports/T-015-s2.json", "title": "10k warnings/min in service A", "detail": "warn spam traced to a deprecated call; masks real errors", "severity": "severe", "status": "asked" }
  ]
}
```

Task status: `pending`, `planning`, `awaiting_approval`, `running`, `blocked`, `done`, `failed`, `cancelled`. Step status: `pending`, `in_progress`, `done`, `failed`, `blocked`. Finding status: `new`, `asked`, `issue-opened`, `step-added`, `ignored`.

Every finding keeps its **provenance**, so it can be traced back to who reported it and where: `task`, `step` (`null` for task-level findings), `agent` (the role that found it: `planner`, `orchestrator`, a `worker-<tier>`, or `companion` for companion-side observations), `session` (that agent's `sessionID`) and `source` (the report or run file it was ingested from, relative to `plansDir`). The companion, the only writer of `status.json`, stamps these at ingest; a finding whose `source`/`session` are unknown is still recorded, just without them.

Pending asks carry `kind`: `decision` or `finding`. A `finding` ask references its finding id; `severe` findings are presented first, `minor` ones queue behind blocking asks.

An ask stays in `pending` only while it needs the user. Once answered, the companion records `status: "answered"` and the user's `answer` (or removes it); readers treat any ask that carries an `answer`, or a resolved `status`, as closed. The status web (`web/`) shows only open asks and tucks the resolved ones into a collapsed section.

Plans and status are local working state and are never committed to git unless the user explicitly asks (`plans/` is in `.gitignore`).

### State versioning

Every state file carries a top-level integer `version`. A file without one is **v0** (the pre-versioning format).

- **Current version: v1.** The companion writes `"version": 1` in every `status.json`, run file and report file it creates. A missing `version` is read as v0.
- **Forward migrations at boot.** At the start of every interaction, after reading `status.json`, the companion reconciles its version: a file at `version < 1` (or absent) is migrated forward and rewritten at v1; a file at `version > 1` was written by a newer companion and is **never** downgraded — the companion warns the user and works read-only on the fields it understands.
- **Migrations (apply in order):**
  - **v0 → v1:** add `"version": 1`. No data rewrite. Findings ingested from v1 onward carry provenance (`task`, `step`, `agent`, `session`, `source`); existing findings are left as they are (backfill `task`/`step` only when clearly derivable, never invent a `session`).
- Adding or renaming a state field means **bumping the version and adding the matching migration**, plus the README and web updates (see [`AGENTS.md`](AGENTS.md)).

### State schema and CLI

The format is pinned by JSON Schema and a dependency-free CLI:

- `schema/status.schema.json`, `schema/run.schema.json`, `schema/report.schema.json` — Draft 2020-12 schemas for the three state files (current version v1).
- `scripts/state.mjs` — validated CRUD over `status.json`. It loads the state, applies the change, **validates the whole document against the schema**, and only then writes it atomically; it never downgrades a newer version.

```sh
node scripts/state.mjs validate [--all]         # schema-check the state files
node scripts/state.mjs migrate                  # v0 -> current version
node scripts/state.mjs task add --title "Try X" --status running --writes a,b --session worker=ses_...
node scripts/state.mjs task step --id T-1 --step s2 --status done --commit abc123
node scripts/state.mjs finding add --title "log spam" --severity severe --task T-1 --step s2 --agent worker-medium --session ses_... --source reports/T-1-s2.json
node scripts/state.mjs ask add --kind decision --digest "which cache?" --options reuse,new --recommended reuse
```

`--dir <plansDir>` selects the state directory (default: `plansDir` from `companion.json`). Writers validate before writing — an invalid document is never persisted.

### PRs: `<companionRepo>/scripts/pr.mjs`

Owning a PR is delegated to a small `gh`-backed watcher, so an agent never burns tokens re-checking `gh pr view`. The **owner** of a PR — a direct worker or an orchestrator — does not poll it; it runs the watcher as a background task and goes idle. The watcher polls on a growing interval (10, 20, 30, … minutes), stays silent while nothing changes, and **exits only when the PR needs a look** (CI turned red/green, a review landed or requested changes, a merge conflict, a new commit, or merged/closed); the harness background-task notification then wakes the owner, which decides whether to intervene and re-arms the watcher.

```sh
node scripts/pr.mjs watch  --dir <plansDir> --task T-15 [--pr <url> …]   # background poller; exits on a trigger
node scripts/pr.mjs status --dir <plansDir> --task T-15                  # one-shot snapshot
node scripts/pr.mjs merged --dir <plansDir> --task T-15                  # can the task be finished?
node scripts/pr.mjs open   --repo <path> --head <branch> [--base main]   # create the PR
```

PR URLs come from the task's `pr`/`prUrls` in `status.json` (or `--pr`), so the task entry stays the source of truth; the watcher's last-seen fingerprint lives in `<plansDir>/prs/<task-id>.json` (companion state, not read by the web). `watch` exits `0` on a trigger (with a JSON summary), `3` on a `--max` timeout and `1` on a persistent `gh` error; re-arming after a wake fires only on the *next* change. `merged` exits `0` when every PR is merged, `4` when settled but some closed unmerged, and `1` while any is still open — on `0`, the companion marks the task `done`. The web already surfaces the PR links and a `PR / review` activity state from `pr`/`prUrls`.

### Reports: `<plansDir>/reports/<task-id>[-<step-id>].json`

Written by the spawned agent, read by its parent; one writer per file, so they never conflict.

- Schema: `outcome` (`success`, `pr-open`, `failed` with reason, `interrupted`), `evidence`, `findings` (title, where, why it matters), `questions` (blockers with suggested options). The planner's report carries the plan path, summary, open questions, and findings instead.
- Findings and questions are appended **as they are encountered**, so an agent killed mid-run keeps what it found; `outcome` and `evidence` land when the run finishes.
- The final message may be a pointer (`report: <path>`). The file is the source of truth: notifications can be truncated or lost, files cannot.
- Report files carry `"version": 1` like the other state files.
- A report file that exists is un-ingested. On completion and on reconcile the parent ingests it — the companion records findings and questions in `status.json`, the orchestrator merges step findings into its run file — then deletes the report file, only after the state write lands. Re-ingestion is safe: parents dedup by task + title/where.

### Agent sessions: `sessions` and `<plansDir>/runs/<task-id>.json`

Every agent is spawned as a **background subagent** (`background: true`): the spawning session goes idle right after the spawn, stays free for asks and steering, and can have its context cleared while agents run. Foreground is the exception for short lookups whose answer is needed to continue the current turn (e.g. a `worker-low` status digest); planners, orchestrators and task workers are never foreground. Dependencies are never expressed as chained spawns (spawn A, wait, spawn B): one agent gets the whole sequence in its prompt, and step dependencies are the orchestrator's job.

- At spawn time the companion records the returned child `sessionID` in the task's `sessions` map (`planner` / `orchestrator` / `worker` -> sessionID), and keeps it after completion as the trail.
- The orchestrator records each step's worker `sessionID` in `<plansDir>/runs/<task-id>.json` (`{"task": "T-015", "version": 1, "steps": {"s2": "ses_..."}, "findings": []}`, `steps` overwritten on retry) and merges step workers' findings into `findings` verbatim, tagged with their `step`, `agent` and `session` so the companion can stamp each finding's provenance at ingest. Its file; the companion reads it, never writes it.
- Completions arrive as `<subagent sessionID="..." state="completed">` notifications; the sessionID is matched against these files to attribute the result to a task and step, and the attributed agent's report file is ingested.
- After a context clear the companion reconciles from these files alone: for each `running` task it steers ongoing agents by their recorded `sessionID` (a prompt to a running session is delivered to it mid-run) instead of assuming they finished or failed, and ingests any report files left un-ingested.

## Collisions and worktrees

The companion guarantees that no two running tasks write to the same place.

- Every task records the paths it may write (`writes`) in `status.json`: from the plan's scope, or decided by the companion for a direct worker task.
- Before starting a task, the companion compares its `writes` with those of every running task. Overlap (same path, or one inside the other, in the same repo) means the new task waits (`blocked`) until the other finishes, or the user is asked.
- Tasks that write to a git repo get their own git worktree and branch (`companion/<task-id>`) under `<plansDir>/worktrees/`. The companion creates and removes them. Worktrees isolate files; they do not make overlapping writes safe, so the overlap rule still applies.
- Orchestrators may create extra worktrees under the task's one to parallelize steps, only for steps with disjoint `writes`.
- Merging into the main branch, pushing, and deleting unmerged worktrees need the user's approval.
- `status.json`, run files, and `<plansDir>/reports/` are companion state: never part of any task's `writes`, never checked for overlap.

## Skills

All thin, all inside the repo (`skills/`, exposed to Claude Code via the `.claude/skills` symlink). The user only ever invokes `/companion-start`.

Names are `companion-<role>`: lowercase letters and hyphens only, matching the directory name, so they are valid in Claude Code, OpenCode, Hermes and Codex (OpenCode forbids `:`), and they don't clash with built-in commands like `plan`.

- **`companion-start`:** loads `companion.json`, reads `status.json`, ingests children's report files, triages findings into asks (severe presented first, minor queued), then loops: present the next pending ask, collect the answer, record it, steer the blocked agent, repeat. Watches open PRs through `scripts/pr.mjs` (background, change-triggered, never polled) and marks a task `done` once every PR is merged. Dispatches new requests per the rules above.
- **`companion-plan`:** planner contract. Read-only context gathering, questions returned to the companion when answers change the plan's shape, writes the `.md` and its report file (summary, open questions, findings), never modifies anything else.
- **`companion-orchestrate`:** orchestrator contract. Runs steps whose dependencies are done (independent ones in parallel), spawns the step's assigned worker agent, judges results against the step criteria rather than the worker's claim, adds or rewrites unstarted steps on failure, merges step workers' findings verbatim into its run file, and reports findings instead of silently fixing them. Open PRs are handed to `scripts/pr.mjs` (background, change-triggered) instead of polled. Started steps are never edited.
- **`companion-work`:** worker contract, shared by all three worker tiers. Stay in scope, verify acceptance criteria, and write a report file (`outcome`, `evidence`, `findings`, `questions`) for the parent to ingest — the final message may be just a pointer. A PR is opened and then handed to `scripts/pr.mjs`, which wakes the worker only on a change. Out-of-scope needs become findings or questions, never silent expansion.

## Harnesses

The design needs only a harness that can spawn sub-agents with a chosen model and read files. Should work with any harness. Background (async) subagent spawning is required: the companion idles while agents run, and ongoing agents are steered and reconciled by their recorded `sessionID`s after a context clear.

## Status web

`web/` is a static, dependency-free viewer of the companion state: it reads `companion.json`, `status.json`, run and report files, and the plans, and renders them as a hierarchical dashboard (tasks → steps DAG → findings, plus the open asks). No backend and no build step; the browser does the reading. Tasks show a **derived activity state** — `waiting · you`, `PR / review`, `active`, `idle`, `blocked` — so a task recorded `running` that is really waiting is visible at a glance (see [`AGENTS.md`](AGENTS.md)).

```sh
./web/serve.sh          # serve the repo root, then open http://localhost:8765/web/
./web/serve.sh 9000     # custom port
# real state: http://localhost:8765/web/   ·   sample data: http://localhost:8765/web/?demo=1
```

It must be served over HTTP (`fetch()` is blocked on `file://`). The state files are a contract with the web: **changing their format can break the viewer**, so the web, the sample data (`web/sample/`) and the state writers in `skills/` must be updated together. See [`AGENTS.md`](AGENTS.md).

## Flows

1. **Simple task:** user asks, companion spawns a worker, worker finishes with evidence, companion records `done` and summarizes in two lines.
2. **Larger work:** companion spawns the planner, relays its questions one at a time, user approves the plan, companion spawns an orchestrator that runs steps through workers while the user is asked about other work.
3. **Failed step:** orchestrator can't patch a done step, so it asks the user with two options, then adds a new step and rewires unstarted ones. Changing the goal needs explicit approval.
4. **Off-path issue:** a worker writes a finding to its report file, the companion ingests and triages it, then asks the user — immediately if severe, at the next interaction otherwise: open an issue, add a step, or ignore.
5. **Status question:** companion spawns a worker that reads `status.json` and the plan and returns a short digest.
6. **PR lifecycle:** the owner (worker or orchestrator) opens the PR and hands it to `scripts/pr.mjs watch` in the background; it wakes only on a change (CI, review, conflict, merge). The owner fixes CI/conflict, relays scope decisions, and re-arms. Once every PR of a task is merged, the companion marks the task `done`.
