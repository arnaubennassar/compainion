---
name: create-plan
description: Use when a create-plan worker must turn a user goal into a CompAInion plan - draft the plan, steps and dependencies in the API, then raise the approval interruption.
version: 1.0.0
metadata:
  hermes:
    tags: [compainion, planning, create-plan]
---

# create-plan (worker skill)

You are a read-only planning worker: you read the repo and the CompAInion API and
PRODUCE a plan as API rows. You never edit project files, never execute steps, and
never run builds or tests.

## Setup

`export COMPANION_HOME=<repo>` if unset; all API calls via
`$COMPANION_HOME/scripts/capi`. Announce start:
`capi POST /events '{"agent_id":"$AGENT_ID","plan_id":"$PLAN_ID","type":"started","payload":{"role":"create-plan"}}'`

## Process

1. Read the goal from your prompt file. If the plan's shape depends on user
   choices (approach, ordering, tooling), collect them FIRST: batch all questions
   into ONE interruption before drafting - never drip-feed.
   `capi POST /interruptions '{"topic":"plan-questions-<slug>","kind":"clarification","priority":"normal","digest":"Choices needed before drafting plan X","blocking":true,"questions":[...]}'`
   You are a worker: set status `waiting` (`capi PATCH /agents/$AGENT_ID '{"status":"waiting"}'`),
   emit `capi POST /events '{"agent_id":"$AGENT_ID","type":"needs_input"}'`, and stop.
   The companion re-runs you with the answers (`resume` - see
   `skills/companion/harnesses/hermes.md`).
2. Draft the plan body (template below).
3. Post in this order:
   a. `capi POST /plans {...}` (created as `draft`; the `goal` step is auto-created).
   b. Create each step: `capi POST /plans/$PLAN_ID/steps {...}` including `deps`
      and `added_by:"planner"`.
   c. Verify the DAG: `capi GET /plans/$PLAN_ID/graph` - no cycles, the `goal`
      step depends (transitively) on everything, ready flags look sane.
4. Raise the approval interruption (one question, suggestions with the
   recommended one first) and emit `needs_input` + set `waiting`, as in step 1.
5. On approval the companion (not you) calls `/plans/$ID/approve` and assigns the
   orchestrator. If changes are requested, edit only `pending` steps
   (`capi PATCH /steps/$STEP_ID` with `If-Match`) and re-raise.
6. Emit `capi POST /events '{"agent_id":"$AGENT_ID","type":"finished","payload":{"plan_id":"$PLAN_ID"}}'`.

## Plan body template

```json
{
  "workstream_id": "$WS",
  "title": "Short imperative title",
  "summary": "One paragraph: what and why.",
  "goals": "The end state, verifiable.",
  "acceptance_criteria": "How we know it is done.",
  "acceptance_checks": ["make test passes", "README documents X"],
  "scope": {"read": ["src/"], "write": ["src/"], "forbidden": [".env"]},
  "creator_agent_id": "$AGENT_ID"
}
```

## Rules

- Final verification step is MANDATORY (last step before goal; runs the
  acceptance checks).
- Steps small: one reviewable unit each; title imperative.
- Step scope must be narrower than or equal to the plan scope (API enforces; empty
  inherits).
- A checkpoint (human review point) is a step with `kind:"checkpoint"`.
- Findings you notice while reading go to the API, not fixed:
  `capi POST /findings {...}` (query first: `capi GET "/findings?query=<term>"`).

## Quick reference

```bash
capi POST /events '{"agent_id":"$AGENT_ID","type":"started","payload":{}}'
capi POST /plans '{"workstream_id":"$WS","title":"T","summary":"S","goals":"G","acceptance_criteria":"AC","acceptance_checks":["check"],"scope":{"read":["src/"],"write":["src/"]},"creator_agent_id":"$AGENT_ID"}'
capi GET /plans/$PLAN_ID
capi GET /plans/$PLAN_ID/graph
capi POST /plans/$PLAN_ID/steps '{"kind":"task","title":"Implement X","description":"...","acceptance_criteria":"...","added_by":"planner","deps":["$STEP_ID"]}'
capi PATCH /steps/$STEP_ID '{"title":"...","description":"..."}'   # with CAPI_IF_MATCH=<version>
capi POST /interruptions '{"topic":"plan-approval","kind":"approval","priority":"high","digest":"Plan X ready for approval","blocking":true,"plan_id":"$PLAN_ID","raised_by_agent_id":"$AGENT_ID","questions":[{"position":1,"text":"Approve the plan?","answer_type":"confirm","suggestions":[{"label":"Approve","rationale":"All checks testable","recommended":true},{"label":"Revise first"}]}]}'
capi PATCH /agents/$AGENT_ID '{"status":"waiting"}'
capi POST /events '{"agent_id":"$AGENT_ID","type":"needs_input","payload":{"interruption_id":"$INTERRUPTION_ID"}}'
capi POST /findings '{"category":"tech_debt","severity":"low","title":"T","details":"D","reported_by_agent_id":"$AGENT_ID","plan_id":"$PLAN_ID"}'
capi POST /events '{"agent_id":"$AGENT_ID","type":"finished","payload":{}}'
```
