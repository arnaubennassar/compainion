# Hermes harness for CompAInion agents

How detached Hermes agents (workers, orchestrators, digest workers) are spawned,
observed, steered and inspected when the harness is the Hermes gateway Runs API
(companion profile: `http://127.0.0.1:8642/p/companion`) — no tmux anywhere. The companion itself is NOT spawned this
way: it is the Hermes profile `companion` serving Telegram (see "How the
companion is woken").

All operations go through `scripts/hermes/runs.sh` (env: `HERMES_API_URL`
default `http://127.0.0.1:8642/p/companion` (workers run AS the companion
profile — the default profile has no LLM provider), `HERMES_API_KEY` default
`API_SERVER_KEY` from `~/.hermes/profiles/companion/.env`, `COMPANION_HOME` = repo root so skills can be embedded).
Never log or echo the API key.

## The four questions

### 1. How to spawn a detached agent

Use `scripts/hermes/runs.sh spawn <name> <skills-csv> <prompt-file>`:

```bash
name=worker-task-<task-id>          # the agent id AND the Runs API session_id
cat > /tmp/prompt.md <<'EOF'        # full task text; never shell-interpolate
<what the worker must do, incl. any capi calls it needs to make>
EOF
handle=$(COMPANION_HOME="$COMPANION_HOME" scripts/hermes/runs.sh spawn "$name" execute-task /tmp/prompt.md)
```

`spawn` embeds the bodies of the listed skills (`COMPANION_HOME/skills/<name>/SKILL.md`)
into the run's `instructions` and posts `POST /v1/runs {session_id, instructions, input}`.
The response is a handle JSON: `{"harness":"hermes","run_id":"run_...","session_id":"<name>"}`.

Registration order (companiond is the source of truth, so register BEFORE the
run can report anything):

1. Write the prompt file.
2. Register the agent WITHOUT the handle (you cannot have it yet — it only
   exists after the spawn):
   `capi POST /agents '{"id":"'"$name"'","role":"worker","harness":"hermes","status":"starting","workstream_id":"$WS"}'`
   (supply the `id` — it must equal `<name>`, the run's `session_id` — so
   registration is idempotent).
3. Spawn the run (`runs.sh spawn`, above) and PATCH the handle onto the agent:
   `capi PATCH /agents/$AGENT_ID -d '{"handle":'$handle'}'`.
   Note: `GET /agents/{id}` carries NO `ETag` — PATCH agents without `If-Match`
   (If-Match applies to tasks/plans/steps only).
4. `capi PATCH /agents/$AGENT_ID '{"status":"running"}'` once `runs.sh status`
   reports `running`. Do this early: a fast worker may finish and PATCH itself
   `finished` first, and your late PATCH then 409s (invalid transition) — that
   is harmless, just do not retry it.

Names/ids: agent id = run `session_id` = the spawn `<name>` (one stable id per
worker; respawns reuse it — see "Lost workers" below). `run_id` identifies one
execution and changes on every spawn/resume; it lives only in `handle`.

### 2. How to know if an agent is alive / idle / waiting

`scripts/hermes/runs.sh status <run_id>` maps the Runs API status:
`started|running` -> `running`, `completed` -> `completed`, `failed|cancelled`
-> `failed`, 404/gone (retention elapsed or gateway restarted) -> `unknown`.

Interpretation for the agent record:

| runs.sh status | agent status |
|---|---|
| `running` | `running` |
| `completed` AND the agent has an open **blocking** interruption (or the backend already shows `waiting`) | `waiting` — run finished while waiting on a question |
| `completed` and no open interruption | `finished` (its outcome is recorded; check the interruption it was answering) |
| `failed` or `unknown` **while the agent still holds an in_progress step/task** | `lost` — `capi PATCH /agents/$ID '{"status":"lost"}'` and raise a decision interruption (respawn recommended) |
| no in_progress work | `idle` |

`unknown` after a gateway restart with held work is the classic `lost` case —
the run object is gone but the worker's step/task is still claimed.

### 3. How to send a message to an agent (steer / answer)

`scripts/hermes/runs.sh resume <session_id> <text|@file>` posts a NEW run on
the same `session_id`, so the agent continues with its previous context:

```bash
scripts/hermes/runs.sh resume "$name" @/tmp/steer.md   # or "continue"
```

Use it for pure continuation ("continue", "yes, proceed" — auto-steer rules in
the companion skill apply: never introduce new scope) and for answering a
blocking question that reached the worker. Always record the steer event:
`capi POST /agents/$ID/events '{"type":"steer","payload":{...}}'`, and refresh
the agent's `handle` (resume returns a new handle JSON).

### 4. How to read output without joining its context

`scripts/hermes/runs.sh output <run_id>` prints the run's `output` (or its
`error` if the run failed). This is the ONLY way to inspect a worker — never
attach to its session; the point of the Runs API is that the worker's context
stays its own.

For digests and inspections prefer a dedicated read-only worker spawned the
same way with the `execute-task` skill (its task being "digest plan $PLAN_ID:
list steps, statuses, blockers" etc.), then read with `runs.sh output`. Inside
the companion, the `delegate_task` tool is the alternative for quick read-only
lookups — its children cannot ask the user or spawn further agents, so give
them everything in the prompt.

## Role-to-skill table

`runs.sh spawn` accepts `[--role worker|planner|orchestrator]`; without it the
role is inferred from the skills list (create-plan => planner, execute-plan =>
orchestrator, otherwise worker). The role selects the per-run model/provider
(sent on every spawn AND resume; resume reuses the spawn's persisted role):

| Role | Skill passed to `runs.sh spawn` | Model/provider (per run) | Notes |
|---|---|---|---|
| `worker` | `execute-task` | openrouter / z-ai/glm-5.3-flash | one task or one step |
| `orchestrator` | `execute-plan` | anthropic / claude-sonnet-5-5 | executes an approved plan step by step |
| `planner` | `create-plan` | anthropic / claude-opus-5-5 | drafts a plan; never executes |
| digest worker | `execute-task` (read-only usage) | as worker | status questions, "details" lookups; must not mutate |

Env overrides: `HERMES_{WORKER,PLANNER,ORCHESTRATOR}_{MODEL,PROVIDER}` (empty
value = omit the field). Reasoning effort CANNOT be set per run: the Runs API
ignores `reasoning_effort`/`reasoning` body fields and `runtime.requested`
carries only provider+model - configure reasoning in the profile instead.

## Code-editing workers

The Runs API accepts (but has NOT proven at runtime) a `cwd`/`working_dir`
field — do not rely on it. Instead, instruct the worker in its prompt to create
its own isolated worktree first and work there:

```text
Before any edit: run `git worktree add /tmp/wt-<task-id> -b work/<task-id>`
inside the repo, do all work in that directory, and report its path and branch.
```

The companion/orchestrator merges or reviews the branch afterwards.

## How the companion is woken (session-driven, no webhooks)

The companion has NO webhook wake-up path any more (it was removed: see
"Live acceptance - Session-driven loop" in docs/live-acceptance.md). Signals
reach it in exactly two ways:

1. The user messages the companion (Telegram chat with the Hermes profile
   `companion`, or a CLI session) - this starts/continues a turn.
2. Its own blocking wait returns: the companion runs the single terminal call
   `scripts/companion-wait --max 240` (override with `COMPANION_WAIT_MAX`)
   when nothing is pending, and the script's one-line JSON output
   (`{"wake":"interruption",...}` / `{"wake":"events","events":[...]}` /
   `{"wake":"timeout"}`) is the signal. A user message arriving during the wait
   interrupts it and takes priority.

Per-turn contract: a reply only reaches the user when the turn ends, so the
loop is pull next -> present -> END THE TURN; the user answers in the next
turn, where the companion reconciles via
`capi GET "/interruptions?status=presented"` — the API is the source of
truth, not chat history.

Liveness sweep, auto-steer and findings surfacing run on each loop iteration
and on every `events` wake. Context may be cleared at any time: everything
reconciles from the API on the next turn.

Telegram presentation rules: short messages, digest line first, ONE question
at a time (same-`group` questions together; same-`topic` batch presented after),
numbered suggestions with the recommended one first and labelled, accepted
replies `1`, `1 + comment`, free text, or `details` (never answered inline —
spawn a read-only worker and re-present with a one-paragraph summary).

## Approval gate (live-verified)

A worker's shell commands pass the gateway's command security scanner. Benign
capi sequences using shell variables (`C=$COMPANION_HOME/scripts/capi; $C ...`)
were flagged as "nested executable body could not be resolved" and the run
moved to `waiting_for_approval`. The run status payload then carries an
`approval` object (`request_id`, `command`, `choices: [once, session, deny]`).
Answer it with:
`POST /v1/runs/{run_id}/approval {"choice":"once"}` (field is `choice`, not
`decision`; `session` approves for the rest of the run). The run then continues
to completion on its own. Prefer writing plain `scripts/capi ...` commands in
worker prompts to avoid the flag. LIVE-RECONFIRMED (2026-10-06, session-driven
loop): the variable form `"$COMPANION_HOME/scripts/companion-wait"` was
flagged ("nested executable body could not be resolved") even though the prompt
asked for plain `scripts/companion-wait` - models "helpfully" expand paths.
Instruct: export PATH to include `$COMPANION_HOME/scripts`, then run the bare
literal `scripts/companion-wait --max 20`. Note also that `runs.sh status`
maps the `waiting_for_approval` Runs-API status to `unknown` (it is not
started/running/completed/failed/cancelled) - read the raw run object via
`GET /v1/runs/{id}` to see the approval block.

## Known limitations / unverified items (from the gateway spike)

- ~~Session continuity across runs~~ VERIFIED (2026-10-06): same-`session_id`
  runs inherit full context; `resume` works as designed. No
  `previous_response_id` chaining needed.
- ~~Companion-profile Runs API needs its own key~~ DONE: `runs.sh` defaults to
  `http://127.0.0.1:8642/p/companion` and reads `API_SERVER_KEY` from
  `~/.hermes/profiles/companion/.env`. Workers run AS the companion profile
  with that profile's provider credentials, and runs.sh overrides
  model/provider per run (see role table above).
- `skills`, `cwd`/`working_dir` and `profile` fields in `POST /v1/runs` bodies
  are tolerated at submit time but their runtime effect is unverified (the
  worker starts in the gateway scratch dir — instruct workers to `cd` where
  they must work); the guaranteed skill mechanism is embedding skill bodies
  into `instructions` (what `runs.sh spawn` does) and the route-level
  `--skills` list. Tool use (shell, curl) and `cd` into a target repo are
  LIVE-VERIFIED.
- Runs are retained only VERY briefly after a terminal state (observed: a
  completed run 404s within ~1 minute). Capture `runs.sh output` immediately
  after completion; treat later `status` 404 as `unknown` after retention or a
  gateway restart (that is what `lost` covers).
- `POST /v1/runs` returns 202-style accepted and runs asynchronously; use
  `runs.sh status` / `runs.sh output`, or SSE at `GET /v1/runs/{id}/events`.