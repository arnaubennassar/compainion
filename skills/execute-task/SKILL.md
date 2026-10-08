---
name: execute-task
description: Use when acting as a CompAInion worker executing a single task or plan step - do the work, report events and outcome via the API, and raise an interruption when blocked.
version: 1.0.0
metadata:
  hermes:
    tags: [compainion, worker, execute-task]
---

# execute-task (worker skill)

You execute exactly one assigned unit of work (a task or a plan step) given in
your prompt, then report honestly. You do not decide scope changes and you do not
spawn further workers.

## Setup

`export COMPANION_HOME=<repo>`; API via `$COMPANION_HOME/scripts/capi`. Your agent
id and the work/plan ids are in the prompt. Immediately:
`capi POST /agents/$AGENT_ID/heartbeat` and
`capi POST /events '{"agent_id":"$AGENT_ID","type":"started","payload":{"title":"<work title>"}}'`.

## Work

1. Claim your unit:
   - step: `capi POST /steps/$STEP_ID/claim '{"agent_id":"$AGENT_ID"}'`
   - task: `capi POST /tasks/$TASK_ID/claim '{"agent_id":"$AGENT_ID"}'`
2. Do the work within the given scope (`scope.read`/`write`/`forbidden`). Heartbeat
   every few minutes.
3. Report progress events at meaningful checkpoints:
   `capi POST /events '{"agent_id":"$AGENT_ID","step_id":"$STEP_ID","type":"progress","payload":{"note":"...","pct":50}}'`

## Finish - outcome format (exact)

```json
{"result": "success|failed|interrupted", "evidence": ["command -> output", "file changed"], "notes": "..."}
```

- step: `capi POST /steps/$STEP_ID/finish '{"status":"done","outcome":{"result":"...","evidence":["go test ./... -> ok"],"notes":"..."}}'`
  (`done` REQUIRES non-empty `evidence`, else 422; `status` may be
  `done|failed|blocked|interrupted`).
- task: `capi POST /tasks/$TASK_ID/finish '{"status":"done","outcome":{...}}'`
- then: `capi POST /events '{"agent_id":"$AGENT_ID","type":"finished","payload":{"result":"success"}}'`
  and `capi PATCH /agents/$AGENT_ID '{"status":"finished"}'`.
- `result: failed` -> the finish `status` is `failed`; include the failing command
  and output in `evidence`. Never fake success.

## Blocked

If you cannot proceed without a decision (ambiguous requirement, failing dep,
scope conflict):

1. Raise the interruption with concrete options (suggestions, recommended first):
   `capi POST /interruptions '{"topic":"worker-blocked-<slug>","kind":"decision","priority":"high","digest":"Worker blocked on <thing>","blocking":true,"step_id":"$STEP_ID","raised_by_agent_id":"$AGENT_ID","questions":[{"position":1,"text":"...?","answer_type":"choice","suggestions":[{"label":"...","recommended":true},{"label":"..."}]}]}'`
2. `capi POST /events '{"agent_id":"$AGENT_ID","step_id":"$STEP_ID","type":"needs_input","payload":{"interruption_id":"$INTERRUPTION_ID"}}'`
3. `capi PATCH /agents/$AGENT_ID '{"status":"waiting"}'` and stop. The companion
   resolves it and resumes you. Do not spawn workers; do not guess.

## capi body files (staleness guard)

Write API bodies to a file and pass its path to capi as an @file body - NEVER compose
JSON inline in bash. Body files MUST be unique per task and per call, e.g.
`body-$TASK_ID-$NNN.json` in the scratch dir, and written immediately before
the call. NEVER reuse a generic name like `body.json`: concurrent workers share
the scratch dir, and a refused write once made capi silently send another
task's stale body (wrong outcome digest + duplicate interruption). capi
refuses (exit 23) a @file body that is missing, whose mtime predates the capi
process start, or that is reused unmodified within CAPI_BODY_REUSE_WINDOW
seconds (default 120). On a 23 refusal, rewrite the body file fresh with a new
mtime (or a new unique name) - never retry the same unmodified file.

## Findings

Anything wrong you notice that is not your task is reported, not fixed:

1. Query first for near-duplicates: `capi GET "/findings?query=<keyword>"`.
2. `capi POST /findings '{"category":"bug|docs|config|observability|tech_debt|security|other","severity":"low|medium|high","title":"...","details":"...","location":"path/file.go:12","reported_by_agent_id":"$AGENT_ID","plan_id":"$PLAN_ID","step_id":"$STEP_ID","evidence":[{"line":12,"snippet":"..."}]}'`

## Quick reference

```bash
capi POST /agents/$AGENT_ID/heartbeat
capi POST /events '{"agent_id":"$AGENT_ID","type":"started","payload":{}}'
capi POST /events '{"agent_id":"$AGENT_ID","type":"progress","payload":{"note":"..."}}'
capi POST /steps/$STEP_ID/claim '{"agent_id":"$AGENT_ID"}'
capi POST /steps/$STEP_ID/finish '{"status":"done","outcome":{"result":"All tests pass","evidence":["go test ./... -> ok"]}}'
capi POST /tasks/$TASK_ID/claim '{"agent_id":"$AGENT_ID"}'
capi POST /tasks/$TASK_ID/finish '{"status":"failed","outcome":{"result":"build broken on step 2","evidence":["make build -> error E1"]}}'
capi GET "/findings?query=logs"
capi POST /findings '{"category":"tech_debt","severity":"low","title":"Duplicate retry logic","details":"...","location":"internal/store/steps.go:42","reported_by_agent_id":"$AGENT_ID"}'
capi POST /interruptions '{"topic":"worker-blocked","kind":"decision","priority":"high","digest":"Blocked on X","blocking":true,"raised_by_agent_id":"$AGENT_ID","questions":[{"position":1,"text":"Proceed with Y?","answer_type":"choice","suggestions":[{"label":"Yes","recommended":true},{"label":"No"}]}]}'
capi POST /events '{"agent_id":"$AGENT_ID","type":"needs_input","payload":{"interruption_id":"$INTERRUPTION_ID"}}'
capi PATCH /agents/$AGENT_ID '{"status":"waiting"}'
capi POST /events '{"agent_id":"$AGENT_ID","type":"finished","payload":{"result":"success"}}'
```