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
    { "id": "F-2", "task": "T-015", "step": "s2", "title": "10k warnings/min in service A", "detail": "warn spam traced to a deprecated call; masks real errors", "severity": "severe", "status": "asked" }
  ]
}
```

Task status: `pending`, `planning`, `awaiting_approval`, `running`, `blocked`, `done`, `failed`, `cancelled`. Step status: `pending`, `in_progress`, `done`, `failed`, `blocked`. Finding status: `new`, `asked`, `issue-opened`, `step-added`, `ignored`.

Pending asks carry `kind`: `decision` or `finding`. A `finding` ask references its finding id; `severe` findings are presented first, `minor` ones queue behind blocking asks.

Plans and status are local working state and are never committed to git unless the user explicitly asks (`plans/` is in `.gitignore`).

### Reports: `<plansDir>/reports/<task-id>[-<step-id>].json`

Written by the spawned agent, read by its parent; one writer per file, so they never conflict.

- Schema: `outcome` (`success`, `pr-open`, `failed` with reason, `interrupted`), `evidence`, `findings` (title, where, why it matters), `questions` (blockers with suggested options). The planner's report carries the plan path, summary, open questions, and findings instead.
- Findings and questions are appended **as they are encountered**, so an agent killed mid-run keeps what it found; `outcome` and `evidence` land when the run finishes.
- The final message may be a pointer (`report: <path>`). The file is the source of truth: notifications can be truncated or lost, files cannot.
- A report file that exists is un-ingested. On completion and on reconcile the parent ingests it — the companion records findings and questions in `status.json`, the orchestrator merges step findings into its run file — then deletes the report file, only after the state write lands. Re-ingestion is safe: parents dedup by task + title/where.

### Agent sessions: `sessions` and `<plansDir>/runs/<task-id>.json`

Every agent is spawned as a **background subagent** (`background: true`): the spawning session goes idle right after the spawn, stays free for asks and steering, and can have its context cleared while agents run. Foreground is the exception for short lookups whose answer is needed to continue the current turn (e.g. a `worker-low` status digest); planners, orchestrators and task workers are never foreground. Dependencies are never expressed as chained spawns (spawn A, wait, spawn B): one agent gets the whole sequence in its prompt, and step dependencies are the orchestrator's job.

- At spawn time the companion records the returned child `sessionID` in the task's `sessions` map (`planner` / `orchestrator` / `worker` -> sessionID), and keeps it after completion as the trail.
- The orchestrator records each step's worker `sessionID` in `<plansDir>/runs/<task-id>.json` (`{"task": "T-015", "steps": {"s2": "ses_..."}, "findings": []}`, `steps` overwritten on retry) and merges step workers' findings into `findings` verbatim, tagged with their `step`. Its file; the companion reads it, never writes it.
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

- **`companion-start`:** loads `companion.json`, reads `status.json`, ingests children's report files, triages findings into asks (severe presented first, minor queued), then loops: present the next pending ask, collect the answer, record it, steer the blocked agent, repeat. Dispatches new requests per the rules above.
- **`companion-plan`:** planner contract. Read-only context gathering, questions returned to the companion when answers change the plan's shape, writes the `.md` and its report file (summary, open questions, findings), never modifies anything else.
- **`companion-orchestrate`:** orchestrator contract. Runs steps whose dependencies are done (independent ones in parallel), spawns the step's assigned worker agent, judges results against the step criteria rather than the worker's claim, adds or rewrites unstarted steps on failure, merges step workers' findings verbatim into its run file, and reports findings instead of silently fixing them. Started steps are never edited.
- **`companion-work`:** worker contract, shared by all three worker tiers. Stay in scope, verify acceptance criteria, and write a report file (`outcome`, `evidence`, `findings`, `questions`) for the parent to ingest — the final message may be just a pointer. Out-of-scope needs become findings or questions, never silent expansion.

## Harnesses

The design needs only a harness that can spawn sub-agents with a chosen model and read files. Should work with any harness. Background (async) subagent spawning is required: the companion idles while agents run, and ongoing agents are steered and reconciled by their recorded `sessionID`s after a context clear.

## Flows

1. **Simple task:** user asks, companion spawns a worker, worker finishes with evidence, companion records `done` and summarizes in two lines.
2. **Larger work:** companion spawns the planner, relays its questions one at a time, user approves the plan, companion spawns an orchestrator that runs steps through workers while the user is asked about other work.
3. **Failed step:** orchestrator can't patch a done step, so it asks the user with two options, then adds a new step and rewires unstarted ones. Changing the goal needs explicit approval.
4. **Off-path issue:** a worker writes a finding to its report file, the companion ingests and triages it, then asks the user — immediately if severe, at the next interaction otherwise: open an issue, add a step, or ignore.
5. **Status question:** companion spawns a worker that reads `status.json` and the plan and returns a short digest.
