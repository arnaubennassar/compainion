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
- **Surfacing:** agents report issues found off the main path (bugs, noisy logs, misleading docs). The companion presents them at natural breaks with options: open an issue, add a step to the plan, ignore.

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

## Files

### `companion.json`

Single local config at the repo root, gitignored. Copy `companion.example.json` to `companion.json` to start.

```json
{
  "models": {
    "companion": ["anthropic/claude-opus-5-5/high", "openrouter/anthropic/claude-opus-5-5/high"],
    "planner": ["anthropic/claude-opus-5-5/high", "openrouter/anthropic/claude-opus-5-5/high"],
    "orchestrator": ["anthropic/claude-sonnet-5-5/medium", "openrouter/anthropic/claude-sonnet-5-5/medium"],
    "worker-low": ["anthropic/claude-haiku-5-5", "openrouter/anthropic/claude-haiku-5-5"],
    "worker-medium": ["anthropic/claude-sonnet-5-5/medium", "openrouter/anthropic/claude-sonnet-5-5/medium"],
    "worker-high": ["anthropic/claude-opus-5-5/high", "openrouter/anthropic/claude-opus-5-5/high"]
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

- `models`: per agent type, an ordered list of `provider/model[/effort]` entries. The model id may itself contain slashes (e.g. `openrouter/anthropic/claude-sonnet-5-5`); effort is optional and is the last segment only when it is one of `low`, `medium`, `high`, `xhigh`, `max` (e.g. `openai/sol/medium`). Without an effort, the harness default applies. The harness tries them in order and uses the first one whose provider is configured and available; if a spawn fails because the provider or model is unavailable, it moves to the next entry. Every spawn reads it.
- `plansDir`: where plans and status live.
- `companionPreferences`: free-form guidance on how the companion talks to the user. The companion reads it at the start of every interaction.

### Plans: `<plansDir>/<plan-id>.md`

Written by the planner, executed by the orchestrator. Sections:

- **Summary:** what, why, how.
- **Goals:** including expected output.
- **Acceptance criteria:** preferably executable checks (tests, commands), added to CI when relevant.
- **Scope / non-goals:** what may be read, written, or is off limits.
- **Steps:** each with id, title, self-contained description, dependencies, acceptance criteria, narrower scope, and the assigned worker agent (`worker-low`, `worker-medium` or `worker-high`, chosen by difficulty), and optional `checkpoint` (user review before moving on). Steps must fit a single worker context and form a DAG ending in the goal. The last step before the goal verifies the plan acceptance criteria end to end.

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
      "updated": "2026-10-13T10:02:00Z"
    },
    "T-015": {
      "title": "Feature Y across A and B",
      "kind": "plan",
      "status": "running",
      "plan": "feature-y.md",
      "steps": { "s1": "done", "s2": "in_progress", "s3": "pending" },
      "updated": "2026-10-13T10:40:00Z"
    }
  },
  "pending": [
    { "id": "Q-3", "task": "T-015", "kind": "decision", "digest": "...", "options": ["..."] }
  ],
  "findings": [
    { "id": "F-2", "task": "T-015", "title": "10k warnings/min in service A", "status": "new" }
  ]
}
```

Task status: `pending`, `planning`, `awaiting_approval`, `running`, `blocked`, `done`, `failed`, `cancelled`. Step status: `pending`, `in_progress`, `done`, `failed`, `blocked`.

Plans and status are local working state and are never committed to git unless the user explicitly asks (`plans/` is in `.gitignore`).

## Skills

All thin, all inside the repo (`skills/`, exposed to Claude Code via the `.claude/skills` symlink). The user only ever invokes `/companion-start`.

Names are `companion-<role>`: lowercase letters and hyphens only, matching the directory name, so they are valid in Claude Code, OpenCode, Hermes and Codex (OpenCode forbids `:`), and they don't clash with built-in commands like `plan`.

- **`companion-start`:** loads `companion.json`, reads `status.json`, then loops: present the next pending ask, collect the answer, record it, steer the blocked agent, repeat. Dispatches new requests per the rules above and batches findings into asks.
- **`companion-plan`:** planner contract. Read-only context gathering, questions returned to the companion when answers change the plan's shape, writes the `.md`, never modifies anything else.
- **`companion-orchestrate`:** orchestrator contract. Runs steps whose dependencies are done (independent ones in parallel), spawns the step's assigned worker agent, judges results against the step criteria rather than the worker's claim, adds or rewrites unstarted steps on failure, and reports findings instead of silently fixing them. Started steps are never edited.
- **`companion-work`:** worker contract, shared by all three worker tiers. Stay in scope, verify acceptance criteria, return evidence and an outcome (`success`, `failed` with reason, `interrupted`). Out-of-scope needs become findings or questions, never silent expansion.

## Harnesses

The design needs only a harness that can spawn sub-agents with a chosen model and read files. Should work with any harness.

## Flows

1. **Simple task:** user asks, companion spawns a worker, worker finishes with evidence, companion records `done` and summarizes in two lines.
2. **Larger work:** companion spawns the planner, relays its questions one at a time, user approves the plan, companion spawns an orchestrator that runs steps through workers while the user is asked about other work.
3. **Failed step:** orchestrator can't patch a done step, so it asks the user with two options, then adds a new step and rewires unstarted ones. Changing the goal needs explicit approval.
4. **Off-path issue:** a worker reports noisy logs, the companion presents it at the next break, and the user picks issue, new step, or ignore.
5. **Status question:** companion spawns a worker that reads `status.json` and the plan and returns a short digest.
