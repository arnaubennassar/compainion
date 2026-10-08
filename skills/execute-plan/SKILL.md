---
name: execute-plan
description: Use when acting as the orchestrator supervising an approved plan in Paperclip - review each child issue's evidence, assign the next child, revise the plan document, escalate to a human only when needed.
version: 2.0.0
metadata:
  hermes:
    tags: [compainion, orchestration, execute-plan, paperclip]
    related_skills: [paperclip-api, paperclip-control-plane, paperclip-planner-run]
---

# execute-plan (orchestrator skill, Paperclip model)

Paperclip owns the plan (a `plan` document on the parent issue) and the steps
(child issues). You are the parent issue's assignee. You judge evidence; you do
not implement steps yourself - each child is assigned to a worker agent.
HTTP mechanics (base URL, urllib only, `X-Paperclip-Run-Id` on writes): see the
`paperclip-api` skill. Never pipe curl into an interpreter.

## Wake semantics (there is no polling)

You only run when Paperclip wakes you. Do the work for this wake, then end the
turn. Do not sleep, loop, or poll for a worker.

- The parent is `blocked` with the children in `blockedBy`. When ALL blockers
  reach `done`, Paperclip wakes the parent's assignee with reason
  `issue_blockers_resolved`. A child reaching `done` is the normal trigger.
- A comment on the parent, or an answered interaction with
  `continuationPolicy: "wake_assignee"`, also wakes you.
- A child run that FAILS (error, timeout) is a failed run on an in-progress
  issue: it appears in `GET /api/companies/{cid}/attention` as a `failed_run`
  item and enters stranded-issue recovery. A run that "succeeded" yet advanced
  nothing (Paperclip's `livenessState: needs_followup`) raises nothing by itself
  — the stall watcher owns that case (see "Stalled child").
- Diagnose wake questions with `GET /api/issues/{id}/diagnostics/wakes` (recent
  wake reasons, run ids, failure class) and `.../diagnostics/blockers`.

## On every wake

1. `GET /api/issues/{parentId}` - read `status`, `blockedBy` (each entry has the
   child's status and assignee), `documentSummaries`.
2. `GET /api/issues/{parentId}/documents/plan` - the plan; note `latestRevisionId`.
3. `GET /api/companies/{cid}/issues?parentId={parentId}` - the children
   (identifier, status, assigneeAgentId, blockedBy).
4. Decide:
   - a child just reached `done` -> review it, then assign the next one;
   - all children `done` -> verify the goal, close the parent;
   - a child `in_progress` without a live run -> stalled child;
   - nothing actionable -> stop (comment only if it helps the human).

## Reviewing a child's evidence

Never accept `done` on status alone.
1. `GET /api/issues/{childId}/comments` - the worker's final comment must carry
   evidence: commands with exit codes, commit SHAs, paths, PR links.
2. Check every acceptance criterion in the child's `description` against it.
   Re-run cheap checks yourself (read the diff, `git log`, run the test) when
   the claim is locally verifiable. Also `GET .../work-products` and `.../documents`.
3. Verdict:
   - satisfied -> proceed;
   - not satisfied -> `PATCH /api/issues/{childId}` `{"status":"in_progress"}`
     plus a comment stating exactly what is missing (the comment wakes the
     assignee). Never mark a child done yourself;
   - a problem outside the child's scope -> report it as a comment on the
     parent (a finding for the human); do not fix it silently.

## Assigning the next child

Children are created with `blockedBy` edges that encode order. A child with no
assignee whose blockers are all `done` is ready.
1. `PATCH /api/issues/{nextChildId}` `{"assigneeAgentId":"<worker id>","status":"todo"}`.
   Assignment wakes the worker with reason `issue_assigned`. Worker ids:
   `GET /api/companies/{cid}/agents`.
2. Add a handoff comment before or with the assignment — this is the standard,
   not optional: it must name the step, the workspace/worktree to work in, and
   the acceptance check/evidence expected. The worker only sees its own issue,
   and a dispatch whose prompt says nothing about the step is how the
   no-progress class starts.
3. Leave the parent `blocked` on the remaining children.
4. Adding a step: `POST /api/issues/{parentId}/children` with
   `{title, description (incl. acceptance criteria), priority, blockedByIssueIds:[...]}`,
   then reflect it in the plan document.
5. The worker setting its issue `done` is what wakes you next.

## Stalled child

Signs: `in_progress`, last run `failed`/`timed_out`, or a run that "succeeded" but
advanced nothing, no new comment. Check `GET /api/issues/{childId}/diagnostics/wakes`
and the run (`GET /api/companies/{cid}/heartbeat-runs?limit=50` or
`GET /api/issues/{childId}/runs`; match `contextSnapshot.issueId`, then read
`status`, `errorCode`, `livenessState`, `livenessReason`, `lastUsefulActionAt`).
- `livenessState: needs_followup` ("no concrete action evidence") on a run whose
  status is `succeeded` is the empty-model-reply class: the run did nothing and
  no attention item is raised for it. Paperclip will not retry it, and the
  platform fix is out of bounds, so the remedy is ours: re-wake the owner **with
  a handoff comment** that names the step, the worktree and the acceptance
  check, so the next run does not start from an empty prompt. The stall watcher
  in the companion profile already does exactly this, on its 2-minute tick.
- Transient failure -> one comment on the child ("continue; last run failed:
  <reason>"), or
  `POST /api/agents/{workerId}/wakeup` `{"source":"on_demand","triggerDetail":"manual","reason":"orchestrator_retry"}`.
- Same failure twice -> not transient; raise a human interaction.

## Revising the plan

Edit the document, do not recreate it:
`PUT /api/issues/{parentId}/documents/plan` with
`{"format":"markdown","body":"<full new body>","changeSummary":"<why>","baseRevisionId":"<latestRevisionId you read>"}`.
Re-read and merge if the revision is stale. History: `GET .../documents/plan/revisions`.
Small corrections (a path, an ordering fix, a step you also created as a child)
are yours; note them in a parent comment. A change to the goal, scope, or an
approved decision is NOT yours: raise an interaction and leave the old text
until it is answered.

## Raising a human-only interaction

Only for what nobody else can decide: goal/scope change, an approved decision
proven wrong, a repeated non-transient failure, missing access, approval of a
risky or irreversible action. Not for things you can decide or verify.

`POST /api/issues/{parentId}/interactions`
```
{"kind":"request_confirmation",
 "continuationPolicy":"wake_assignee",
 "requestedResolverPolicy":"human_only",
 "title":"<short>",
 "idempotencyKey":"<stable key so retries do not duplicate>",
 "payload":{"version":1,"prompt":"<one question>","acceptLabel":"...","rejectLabel":"...",
            "rejectRequiresReason":true,"detailsMarkdown":"<context + options>",
            "supersedeOnUserComment":true,
            "target":{"type":"issue_document","issueId":"<parentId>","key":"plan",
                      "documentId":"<doc id>","revisionId":"<latestRevisionId>"}}}
```
Other kinds exist (`suggest_tasks`, `ask_user_questions`); copy the shape from
an existing one via `GET /api/issues/{id}/interactions`. After posting, STOP.
Never auto-answer, work around, or time out a human interaction; the answer
wakes you. State: `GET /api/issues/{parentId}/interactions`.

## Finishing

When every child is `done` and reviewed: verify the plan's stated goal
end-to-end (the promised artifact exists, the check passes), post a summary
comment with the evidence on the parent, then `PATCH /api/issues/{parentId}`
`{"status":"done"}`.

## Rules

- Evidence over claims.
- One wake = decide, act, end the turn.
- Do not change the state of issues outside the plan you supervise.
- Findings are reported, never silently fixed.
- Route layout: list = `/api/companies/{cid}/issues`; per-issue =
  `/api/issues/{id}/...` (no `/api/companies/{cid}/issues/{id}`).
