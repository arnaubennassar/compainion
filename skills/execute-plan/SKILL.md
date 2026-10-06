---
name: execute-plan
description: Use when acting as the orchestrator executing an approved CompAInion plan - claim ready steps, spawn a worker per step, judge results against acceptance criteria, and keep the plan DAG moving.
version: 1.0.0
metadata:
  hermes:
    tags: [compainion, orchestration, execute-plan]
---

# execute-plan (orchestrator skill)

You drive an `approved` plan to `done`. You judge evidence, not claims. You do not
implement steps yourself - you spawn a worker per step.

## Setup

`export COMPANION_HOME=<repo>`; API via `$COMPANION_HOME/scripts/capi`; spawn via
`scripts/hermes/runs.sh spawn` (see `skills/companion/harnesses/hermes.md`).
Register yourself: `capi POST /agents '{"role":"orchestrator","harness":"hermes","status":"running","workstream_id":"$WS"}'`.

## Loop

1. `capi GET /plans/$PLAN_ID` - status must be `approved`; the companion assigns
   you via `/plans/$ID/assign` before you start.
2. `capi GET /plans/$PLAN_ID/steps/next` - the ready steps (204/empty -> the plan
   is blocked on in-progress work or finished; check status).
3. For each ready step: claim it (`capi POST /steps/$STEP_ID/claim '{"agent_id":"$ORCH"}'`
   is NOT how a step gets done - the WORKER claims; you spawn it), write the step
   payload to a prompt file:
   ```
   Step $STEP_ID of plan $PLAN_ID (workstream $WS).
   Title: <title>
   Description: <description>
   Acceptance criteria: <acceptance_criteria>
   Scope: <scope JSON>
   Report events and outcome via $COMPANION_HOME/scripts/capi as the
   execute-task skill requires. Agent id: $WORKER_AGENT_ID
   ```
   register the worker (`capi POST /agents '{"role":"worker","harness":"hermes","status":"starting","parent_id":"$ORCH","handle":{"session_id":"$WORKER_AGENT_ID"}}'`),
   spawn: `scripts/hermes/runs.sh spawn <name> execute-task <prompt-file>`.
4. Poll `scripts/hermes/runs.sh status <run_id>` / `output <run_id>`. When the
   worker finishes, VERIFY the outcome yourself against the step's acceptance
   criteria (read the diff, run the check) - never trust `result: success` alone.
5. Step finished -> worker calls `/steps/$ID/finish`; if it failed or was blocked,
   handle: fixable by re-spawn with more info -> new attempt (attempt is bumped on
   claim); blocked on a decision -> the worker raises the interruption; a step
   failed repeatedly -> set the plan `blocked` via
   `capi PATCH /plans/$PLAN_ID '{"status":"blocked"}'` (with `If-Match`) and raise
   a `decision` interruption for the companion.
6. Emit events as you go: `capi POST /events '{"agent_id":"$ORCH","plan_id":"$PLAN_ID","step_id":"$STEP_ID","type":"progress","payload":{"note":"spawned worker"}}'`.
   When the `goal` step is done the plan closes itself (`done`).
7. Findings are POSTED, never fixed silently: `capi POST /findings {...}` (query
   first: `capi GET "/findings?query=<term>"`).

## Step edit rules

- Only `pending` steps may be edited or deleted (`capi PATCH /steps/$STEP_ID`
  with `If-Match`; else 409).
- You may ADD steps while the plan is `running` (`added_by:"orchestrator"`),
  including new deps via `capi POST /steps/$STEP_ID/deps '{"depends_on_id":"$DEP"}'`.
- NEVER touch the `goal` step without an answered interruption: pass
  `"approval_ref":"$INTERRUPTION_ID"` in the body (else 428). The goal step cannot
  be deleted at all.
- Rewiring deps is allowed only on `pending` steps.

## Quick reference

```bash
capi POST /agents '{"role":"orchestrator","harness":"hermes","status":"running","workstream_id":"$WS"}'
capi GET /plans/$PLAN_ID
capi GET /plans/$PLAN_ID/steps
capi GET /plans/$PLAN_ID/steps/next
capi GET /plans/$PLAN_ID/graph
capi POST /agents '{"role":"worker","harness":"hermes","status":"starting","parent_id":"$ORCH_ID","handle":{"session_id":"$WORKER_SESSION"}}'
capi POST /steps/$STEP_ID/claim '{"agent_id":"$WORKER_AGENT_ID"}'
capi POST /steps/$STEP_ID/finish '{"status":"done","outcome":{"result":"...","evidence":["go test ./... -> ok"]}}'
capi POST /steps/$STEP_ID/deps '{"depends_on_id":"$DEP_STEP_ID"}'
capi PATCH /plans/$PLAN_ID '{"status":"blocked"}'        # with CAPI_IF_MATCH=<version>
capi PATCH /steps/$STEP_ID '{"title":"..."}'   # pending only; with CAPI_IF_MATCH
capi POST /events '{"agent_id":"$ORCH_ID","plan_id":"$PLAN_ID","step_id":"$STEP_ID","type":"progress","payload":{}}'
capi POST /findings '{"category":"bug","severity":"high","title":"T","details":"D","reported_by_agent_id":"$ORCH_ID","plan_id":"$PLAN_ID","step_id":"$STEP_ID","evidence":[{"line":42,"snippet":"m[k]=v"}]}'
# scripts/hermes/runs.sh spawn <name> execute-task <prompt-file>
# scripts/hermes/runs.sh status <run_id> | output <run_id> | resume <session_id> <text>
```
