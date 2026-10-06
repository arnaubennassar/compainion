# Hermes harness for CompAInion agents

How detached Hermes agents (workers, orchestrators, digest workers) are spawned,
observed, steered and inspected when the harness is the Hermes gateway Runs API
(companion profile: `http://127.0.0.1:8642/p/companion`) — no tmux anywhere. The companion itself is NOT spawned this
way: it is the Hermes profile `companion` serving Telegram (see "Wake-up flow").

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
2. Register the agent, holding the handle JSON:
   `capi POST /agents '{"id":"'"$name"'","role":"worker","harness":"hermes","status":"starting","handle":'"$handle"',"workstream_id":"$WS"}'`
   (supply the `id` — it must equal `<name>`, the run's `session_id` — so
   registration is idempotent and the handle round-trips).
3. Spawn the run (`runs.sh spawn`, above). If it returns a different `run_id`
   than you expected (e.g. after a retry), PATCH the handle:
   `capi PATCH /agents/$AGENT_ID --header "If-Match: $etag" -d '{"handle":'"$new_handle"'}'`
4. `capi PATCH /agents/$AGENT_ID '{"status":"running"}'` (with If-Match) once
   `runs.sh status` reports `running`.

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

| Role | Skill passed to `runs.sh spawn` | Notes |
|---|---|---|
| `worker` | `execute-task` | one task or one step |
| `orchestrator` | `execute-plan` | executes an approved plan step by step |
| `create-plan` worker | `create-plan` | drafts a plan; never executes |
| digest worker | `execute-task` (read-only usage) | status questions, "details" lookups; must not mutate |

## Code-editing workers

The Runs API accepts (but has NOT proven at runtime) a `cwd`/`working_dir`
field — do not rely on it. Instead, instruct the worker in its prompt to create
its own isolated worktree first and work there:

```text
Before any edit: run `git worktree add /tmp/wt-<task-id> -b work/<task-id>`
inside the repo, do all work in that directory, and report its path and branch.
```

The companion/orchestrator merges or reviews the branch afterwards.

## Wake-up flow (Telegram)

1. companiond emits `interruption_created` / `agent_waiting` / `agent_lost`
   notes; its webhook subscription (registered by `scripts/hermes/install.sh`)
   POSTs to `http://127.0.0.1:8644/p/companion/webhooks/companion-wake`
   signed with the route's secret (`X-Webhook-Signature-V2` + `X-Webhook-Timestamp`).
2. The Hermes route `companion-wake` (profile `companion`) runs the companion
   agent with the fixed prompt `reconcile: present the next interruption` and
   delivers the reply to Telegram (`--mirror-to-session` also writes it into
   the chat session so replies there have context).
3. The notification is a SIGNAL only: the companion calls
   `capi GET /interruptions/next` itself and presents per the Telegram rules.
4. The user replies in Telegram. The companion reconciles via
   `capi GET "/interruptions?status=presented"` — the API is the source of
   truth, not chat history.

Telegram presentation rules: short messages, digest line first, ONE question
at a time (same-`group` questions together; same-`topic` batch presented after),
numbered suggestions with the recommended one first and labelled, accepted
replies `1`, `1 + comment`, free text, or `details` (never answered inline —
spawn a read-only worker and re-present with a one-paragraph summary).

## Known limitations / unverified items (from the gateway spike)

- **Session continuity across runs is unverified**: same-`session_id` runs are
  accepted but their context inheritance could not be proven live (no LLM
  provider credentials on the host at spike time). If resume turns out not to
  inherit context, fall back to `previous_response_id` chaining (a completed
  run's status payload carries the response identity) and add it to runs.sh.
- **Companion-profile Runs API needs its own key**: plain `/v1/runs` runs on
  the default profile. To run workers AS the companion profile, the user must
  add `API_SERVER_ENABLED`/`API_SERVER_KEY` to
  `~/.hermes/profiles/companion/.env` and use
  `HERMES_API_URL=http://127.0.0.1:8642/p/companion` with that key. Until then
  workers run on the default profile.
- `skills`, `cwd`/`working_dir` and `profile` fields in `POST /v1/runs` bodies
  are tolerated at submit time but their runtime effect is unverified; the
  guaranteed skill mechanism is embedding skill bodies into `instructions`
  (what `runs.sh spawn` does) and the route-level `--skills` list.
- Runs are retained only briefly after a terminal state; status may become
  `unknown` after retention or a gateway restart (that is what `lost` covers).
- `POST /v1/runs` returns 202-style accepted and runs asynchronously; use
  `runs.sh status` / `runs.sh output`, or SSE at `GET /v1/runs/{id}/events`.