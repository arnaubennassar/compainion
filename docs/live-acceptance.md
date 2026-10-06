# Live acceptance — Hermes gateway path (2026-10-06)

Everything below was executed for real against the live gateway
(Hermes 0.21.5, multiplexed profiles) and companiond on 127.0.0.1:7788
(throwaway DB). Secrets redacted. Commits: runs.sh/install.sh/docs changes are
in the git log; this file records what was run and observed.

## 1. Worker runs on the companion profile

`scripts/hermes/runs.sh` defaults: `HERMES_API_URL=http://127.0.0.1:8642/p/companion`,
API key read from `~/.hermes/profiles/companion/.env`. Workers run AS the
companion profile with that profile\'s openrouter credentials.

## 2. Spike re-runs (docs/hermes-gateway-spike.md, "Re-run with provider")

- (b) Session continuity VERIFIED: spawn "remember the code word is MANGO" ->
  `ok`; resume same session "what was the code word?" -> `MANGO`.
- (c) Tool use VERIFIED: worker with `execute-task` skill ran `pwd` in a temp
  git repo (cd honoured) and `curl http://127.0.0.1:7788/healthz` ->
  `{"status":"ok"}`. Spawn->completed ~10 s.

## 3. Per-role model/provider (verified, runtime object)

| Role | spawn probe | runtime.provider | runtime.model |
|---|---|---|---|
| worker (`execute-task`) | probe-worker | openrouter | z-ai/glm-5.3-flash |
| planner (`create-plan`) | probe-planner | anthropic | claude-opus-5-5 |
| orchestrator (`execute-plan`) | probe-orch | anthropic | claude-sonnet-5-5 |

`resume` reuses the persisted spawn role (state file
`~/.local/state/companion/runs/<session>.json`); verified with probe-worker ->
openrouter / z-ai/glm-5.3-flash. Reasoning effort CANNOT be set per run: the
Runs API ignores `reasoning_effort`/`reasoning` body fields and
`runtime.requested` carries only provider+model (limitation, documented).

## 4. Automated worker acceptance (no human)

Workstream `01M48Y5TJ50MC580EWZYMGAVCH`, agent `acc-worker-1`, task
`01M48Y5TJJYHTXH4HM17S8MZ46` ("create /tmp/cmp-accept/cmp-hello.txt with hi"):

- Registered agent (status starting) BEFORE spawn; spawned with `runs.sh spawn`
  + `execute-task` skill; the worker claimed the task, created the file,
  finished with status `done` and outcome.evidence
  `["mkdir -p /tmp/cmp-accept && printf hi\n > ... -> exit 0", "cat ... -> hi"]`.
- Verified: task `done` via API, file exists containing `hi`, agent `finished`.
- Timing note: a completed Hermes run 404s within ~1 minute (aggressive
  retention) - capture `runs.sh output` immediately.

## 5. Blocked-worker scenario (human decision loop)

Task `01M48Y8FB80HEKT0K8QRWFDRG8` (choice staging vs production, explicitly
"do not pick one yourself"):

- Worker `acc-worker-2` raised a blocking decision interruption
  `01M48Y9A2DSAMM8QAGHKDYJCN5` with 2 suggestions (Staging recommended,
  Production), emitted `needs_input`, set itself `waiting`; task `in_progress`.
- `GET /interruptions/next` returned it (NOTE: calling /next marks the
  interruption `presented`; when only presented items exist /next answers 204).
- Answered via `POST /interruptions/{id}/answers` (author `user`, mode
  `suggestion`, suggestion "Production", final true) -> interruption
  `answered`.
- Resumed via `runs.sh resume` telling the human answer. The run hit the
  gateway approval gate (worker used `$C`-style variable commands, flagged
  "nested executable body"); resolved via
  `POST /v1/runs/{id}/approval {"choice":"once"}` (field is `choice`, not
  `decision`). Worker then finished: task `done` with evidence including
  "chose Production", agent `finished`.

## 6. Session-driven loop (2026-10-06, replaces the dropped webhook wake-up)

DECISION: the webhook wake-up path is gone. The companion is woken only by a
user message (Telegram chat / CLI session) or by its own blocking wait,
`scripts/companion-wait --max 240` (test-first: `scripts/test-companion-wait.sh`,
throwaway daemon on 127.0.0.1:7791, temp DB + temp XDG_STATE_HOME - prints
`companion-wait: OK`; covers timeout, interruption wake, events wake + cursor
advance, self-authored-event exclusion).

Live acceptance WITHOUT the human: throwaway companiond on 127.0.0.1:7792 (temp
DB, `COMPANIOND_URL` exported), Runs API on the companion profile
(`scripts/hermes/runs.sh spawn acc-comp-N companion <prompt>` - the companion
skill body embedded as instructions, worker model
openrouter/z-ai/glm-5.3-flash), `COMPANION_WAIT_MAX=20` for speed.

(i) 'start' with an open interruption (seeded via
`COMPANIOND_URL=http://127.0.0.1:7792 scripts/capi POST /interruptions`,
2 suggestions): run acc-comp-1 registered agent `companion-main`, heartbeated,
called `GET /interruptions/next` (200 in companiond log), interruption status
became `presented`, run COMPLETED (~20 s) and its output presented digest +
numbered suggestions:

    Deploy to staging or production?

    1) Staging (recommended — safer)
    2) Production

    Reply with 1 or 2.

(ii) `runs.sh resume acc-comp-1 '1'`: answer POSTed
(`POST /interruptions/<id>/answers` 200 in the log, interruption `answered`),
then `GET /interruptions/next` 204 (queue empty) and an agents liveness sweep
(`GET /agents`). HONEST GAP: run 2's own companion-wait call was NOT directly
observed - its output was eaten by the ~1 min run retention (404), and the
run's terminal session had lost the `COMPANIOND_URL` export (fresh shell per
terminal call), so any wait it ran pointed at the DEFAULT daemon. Fixed the
skill (Setup: re-export env in EVERY terminal call / prefix with `env`) and
proved the wait directly on a fresh session: run acc-comp-2 ran
`scripts/companion-wait --max 20` against 7792 - companiond log shows the full
poll cycle (`GET /interruptions/next` 204 + `GET /events` 200 every 2 s,
~20 s) - and reported verbatim:

    {"wake":"timeout"}

(iii) wake while waiting: with run acc-comp-4 inside its wait, a new
interruption was created via capi (`db-migration-2`). The run woke,
`GET /interruptions/next` returned it (status `presented`), the run COMPLETED
and its output contained the script's JSON verbatim plus the presentation:

    {"wake":"interruption","interruption":{...db-migration-2, status:"presented"...}}

    Woken by a pending decision: a DB migration is awaiting your green light
    (suggestions: 1) Run it (recommended), 2) Postpone). ...

Failure modes found and FIXED during the session-driven work:

1. Fresh shell per terminal call loses exports -> a wait silently targeted the
   wrong daemon. Skill now requires re-exporting in every terminal call.
2. The command scanner flagged `"$COMPANION_HOME/scripts/companion-wait"`
   (variable executable body) -> run stuck in `waiting_for_approval`, which
   `runs.sh status` reports as `unknown`. Unstuck with
   `POST /v1/runs/{id}/approval {"choice":"once"}`; skill/harness now pin the
   bare literal `scripts/companion-wait` after a PATH export.
3. Run retention: a terminal run 404s within ~1 min - poll `status` at <= 5 s
   and capture `output` immediately, or it is gone (bit us three times).

Cleanup actually run on this host: `scripts/hermes/install.sh --uninstall-wake`
- removed the Hermes route `companion-wake` (verified via `hermes webhook
list`: "No dynamic webhook subscriptions"), disabled the companion profile's
webhook-platform toolsets; companiond had no `companion-wake` subscription
(`GET /subscriptions` empty) - nothing to delete. No gateway restart was
performed (none needed: the route is removed at runtime; restart only if
runs are in flight when removing it).

## 7. Human checklist (Telegram)

1. Open the Telegram chat (or a CLI session with the companion profile) and
   say 'start'. The companion pulls the next interruption and presents it.
2. From now on: reply to presented interruptions in Telegram (`1`,
   `1 + comment`, free text, or `details`); the companion reconciles against
   the API. When idle it blocks in `scripts/companion-wait --max 240`; a
   message during that wait interrupts it.
3. If the companion seems stuck while waiting, check its
   `COMPANIOND_URL`/`COMPANION_HOME` exports (fresh shell per terminal call)
   and, for gateway runs, `GET /v1/runs/{id}` for a `waiting_for_approval`
   approval block (see the harness doc's approval gate).
