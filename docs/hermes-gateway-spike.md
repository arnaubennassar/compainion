# Hermes gateway spike (Amendment 1, task A1)

Date: 2026-10-06. Gateway: Hermes 0.21.5, multiplex_profiles=true (profiles:
`default`, `companion`). API server on 127.0.0.1:8642, webhook platform on
127.0.0.1:8644 (restricted — see Host binding). All commands below were run for
real; secrets are redacted.

## Host binding (webhook platform)

The webhook platform initially listened on `0.0.0.0:8644`. It **is**
restrictible via config:

```
hermes config set platforms.webhook.host 127.0.0.1
# -> ✓ Set platforms.webhook.host = 127.0.0.1 in /home/brolygon/.hermes/config.yaml
```

Requires a gateway restart to take effect (`hermes gateway restart`):

```
ss -tlnp | grep -E '8642|8644'
LISTEN 127.0.0.1:8642 ... hermes
LISTEN 127.0.0.1:8644 ... hermes          # was 0.0.0.0:8644 + [::]:8644 before
curl -s http://127.0.0.1:8642/health  -> {"status":"ok","platform":"hermes-agent","version":"0.21.5"}
curl -s http://127.0.0.1:8644/health  -> {"status":"ok","platform":"webhook"}
```

Note: `platforms.webhook.host` alone in config.yaml does not satisfy the CLI's
"webhook platform enabled" check used by `hermes webhook subscribe`;
`platforms.webhook.enabled: true` (or `WEBHOOK_ENABLED` env) is also needed.
Both are now set in the default profile config.

## BLOCKER: no LLM provider credentials on this host

Every agent run (API `/v1/runs`, webhook-triggered, anything that starts an
agent) fails immediately with:

```
ProviderNotConfiguredError: No LLM provider configured. Run `hermes model` to
select a provider, or run `hermes setup` for first-time configuration.
```

Cause: `OPENROUTER_API_KEY` is commented out in `~/.hermes/.env`
(`# OPENROUTER_API_KEY=***`), `auth.json` has `active_provider: null` and an
empty `providers` object; the credential pool references
`source: env:OPENROUTER_API_KEY` which is unset. This is a user-added secret;
the spike did not add it. Consequences: (a)-(c) could be verified at the API/lifecycle
level only — run creation, status polling, events, error semantics — but NOT
agent behaviour (tool use, context continuity, skill loading at runtime).

## (a) Runs API — POST /v1/runs, status, events, errors

Create (note: **202-style accepted**, run starts async):

```
curl -s http://127.0.0.1:8642/v1/runs -X POST \
  -H "Authorization: Bearer $API_SERVER_KEY" -H 'Content-Type: application/json' \
  -d '{"input":"...","instructions":"...","session_id":"spike-a1-worker"}'
-> {"run_id":"run_e5d92667...","status":"started","replayed":false}
```

Poll (short `sleep 5` loop):

```
curl -s http://127.0.0.1:8642/v1/runs/$RID -H "Authorization: Bearer $API_SERVER_KEY"
-> {"object":"hermes.run","run_id":"...","status":"failed",
    "updated_at":1791300180.55,"created_at":1791300180.52,
    "session_id":"spike-a1-worker","model":"hermes-agent",
    "error":"No LLM provider configured...","last_event":"run.failed"}
```

Statuses: `started` -> `running` -> `completed` | `failed` | `cancelled`
(retained briefly after terminal state; capabilities report a 86400s retention
for the runs idempotency store). A completed run additionally carries an
`output` field. Unknown run:

```
GET /v1/runs/run_bogus123 -> 404
{"error":{"message":"Run not found: run_bogus123","type":"invalid_request_error",
          "param":null,"code":"run_not_found"}}
```

Events/stream endpoint — **yes**, SSE at `GET /v1/runs/{id}/events`:

```
curl -N http://127.0.0.1:8642/v1/runs/$RID/events -H "Authorization: Bearer $API_SERVER_KEY"
: open
id: 0
data: {"event":"run.failed","run_id":"...","timestamp":...,"error":"No LLM provider...","seq":0}
: stream closed
```

`GET /v1/runs/{id}/stream` does not exist (404). Capabilities
(`GET /v1/capabilities` — the `/api/capabilities` path 404s) confirm:
`run_submission`, `run_status`, `run_events_sse`, `run_stop`, `run_steer`,
`run_approval_response`, `session_continuity_header: X-Hermes-Session-Id`,
plus `/api/sessions/{id}/chat(+stream)`, `session_fork`, `skills_api`
(`GET /v1/skills`). Full endpoint map is in the capabilities payload.

## (b) Context continuity across runs

**Could not be verified live** (provider blocker). Doc says runs accept
`session_id`, `instructions`, `conversation_history`, `previous_response_id`;
the Responses API chain (`previous_response_id`) reconstructs full context, and
the `conversation` parameter auto-chains to the latest response of a named
conversation. Probed field acceptance (all POSTed, all returned
`status:"started"`, i.e. no schema rejection; runtime effect unverified):

- `{"input","session_id"}` twice with same session_id — accepted
- `{"previous_response_id":"resp_nonexistent"}` — accepted at submit (validation
  happens at run time; a bogus id would fail the run once the agent can start)
- `{"conversation_history":[{"role":"user","content":"hi"}]}` — accepted

Design consequence for `scripts/hermes/runs.sh`: `resume` re-posts with the
same `session_id` (what the amendment already assumed). If live testing later
shows same-session runs do NOT inherit context, the fallback is
`previous_response_id` chaining: runs.sh would have to remember the last run's
response id (the run status payload of a *completed* run carries the response
identity) — add it then.

## (c) Working directory / skill preload / profile routing

API-level probes (all accepted with `status:"started"`, no 4xx — but runtime
effect unverified due to the provider blocker):

```
{"input":"x","skills":["companion"],...}        -> started   (skills field tolerated)
{"input":"x","cwd":"/tmp",...}                  -> started
{"input":"x","working_dir":"/tmp",...}          -> started
{"input":"x","profile":"companion",...}         -> started
{"input":"x","previous_response_id":"resp_...",}-> started
```

Webhook routes have a real, documented `skills:` list (loaded into the agent
run), so skill preloading is a supported concept at route level; for runs the
guaranteed mechanism is embedding the skill body into `instructions`
(what `runs.sh spawn` does).

Profile routing: plain `/v1/runs` executes on the **default** profile. A
`/p/companion/v1/runs` prefix exists but authenticates against **that
profile's own** `API_SERVER_KEY`:

```
curl http://127.0.0.1:8642/p/companion/v1/runs -H "Authorization: Bearer $API_SERVER_KEY"
-> {"error":{"message":"Invalid gateway API key (API_SERVER_KEY)",
             "type":"gateway_auth_error","code":"gateway_auth_failed"}}
```

The companion profile's `.env` currently has only
`TELEGRAM_BOT_TOKEN / TELEGRAM_ALLOWED_USERS / TELEGRAM_HOME_CHANNEL` (names
only). So to run workers as the companion profile, the user must add
`API_SERVER_ENABLED/API_SERVER_KEY` to `~/.hermes/profiles/companion/.env` and
call `/p/companion/v1/runs` with that key. With only the default key,
`profile:"companion"` in the body did not error at submit time.

## (d) Webhook signature scheme

Route created exactly as planned (deliver `log` to avoid pinging the user):

```
hermes webhook subscribe spike-wake --prompt 'spike: {type}' --deliver log --description spike
-> URL: http://localhost:8644/webhooks/spike-wake   Profile: default
   Secret: <auto-generated, per-route>              Events: (all)
```

Per-route secret (auto-generated) is stored in
`~/.hermes/webhook_subscriptions.json`; a route without its own secret falls
back to the global `WEBHOOK_SECRET`. Tested against the live adapter with
`sig = hex HMAC-SHA256(secret, body)`:

| Header sent | Result |
|---|---|
| none | 401 `{"error":"Invalid signature"}` |
| `X-Companion-Signature: <hex>` (companiond's current scheme) | **401 Invalid signature** — header unknown to the adapter |
| `X-Webhook-Signature: <hex>` (V1: raw hex digest of body) | **202 accepted** (deprecation warning logged once per route; no replay protection) |
| `X-Hub-Signature-256: sha256=<hex>` | **202 accepted** |
| `X-Webhook-Signature-V2: <hex>` + `X-Webhook-Timestamp: <unix>` where hex = HMAC-SHA256(secret, `<ts>.<body>`) | **202 accepted** — recommended |
| V2 with timestamp 400s stale | 401 (±300s window enforced → replay protection) |
| `X-Hub-Signature-256: <hex>` without `sha256=` prefix | 401 |

Accepted (agent-mode) POSTs return **202** `{"status":"accepted","route":...,
"event":...,"delivery_id":...}` (not 200; 200 is for `deliver_only` delivery).
Multiplexing: a route bound to profile `default` rejects
`POST /p/companion/webhooks/spike-wake` with **404** even with a valid
signature (log: `Route spike-wake is not authorized for profile 'companion'`).
URL shape under multiplexing: `/p/<route-profile>/webhooks/<name>` when the
route was created with `--route-profile`, otherwise plain
`/webhooks/<name>` on the default profile.

### Required change in companiond (task A2)

companiond signs `X-Companion-Signature: hex HMAC-SHA256(body)` — **rejected**.
Switch `internal/notify` to (pick one):

- **Preferred:** `X-Webhook-Signature-V2: <hex hmac-sha256("<ts>.<body>")>` +
  `X-Webhook-Timestamp: <unix seconds>` (replay-protected, non-deprecated); or
- Minimal diff: rename the header `X-Companion-Signature` -> `X-Webhook-Signature`
  (same hex-of-body algorithm, accepted today, deprecation-warned).

Also: the secret companiond uses must be the **route's** secret (the one
printed at subscribe time / in `webhook_subscriptions.json`), and the URL must
match the route's profile binding. Subscription removed afterwards:
`hermes webhook remove spike-wake` (verified `webhook list` empty).

## (e) Telegram delivery (documented, not executed — no messages sent)

From the webhooks docs and `hermes webhook subscribe --help`:

```
hermes webhook subscribe companion-wake \
  --prompt 'reconcile: present the next interruption' \
  --deliver telegram --deliver-chat-id <chat_id> \
  --mirror-to-session \
  --route-profile companion
```

- `--deliver telegram` sends the agent's response to Telegram. If
  `--deliver-chat-id` is omitted, delivery falls back to the **home channel**
  configured for the platform — on the companion profile that comes from
  `TELEGRAM_HOME_CHANNEL` in `~/.hermes/profiles/companion/.env` (a user
  secret; value not read here). `TELEGRAM_ALLOWED_USERS` gates which users the
  bot accepts.
- `--mirror-to-session` also writes the delivered message into that chat's
  session, so the companion's replies there have context (the webhook wake-up
  text becomes part of the Telegram conversation session).
- The mirrored session belongs to the route's bound profile: with
  `--route-profile companion` the route lives at
  `/p/companion/webhooks/companion-wake` and its runs/sessions execute as the
  `companion` profile; the chat session is that profile's Telegram session.
- Cross-platform delivery requires the target platform connected in the
  gateway (Telegram is configured on `companion`).
- `deliver_only` mode exists for zero-LLM literal notification, but the wake-up
  route must run the agent, so it does not apply.
## Re-run with provider (2026-10-06, later same day)

The user added `API_SERVER_KEY` to `~/.hermes/profiles/companion/.env`, so
runs now execute on the companion profile
(`HERMES_API_URL=http://127.0.0.1:8642/p/companion`; runs.sh was switched to
this default). The former BLOCKER is resolved.

### (b) Context continuity — VERIFIED

- Run 1: `runs.sh spawn spike-memory '' <prompt>` with input "remember the code
  word is MANGO" -> completed, output `ok`.
- Run 2: `runs.sh resume spike-memory "What was the code word?"` (same
  session_id, no conversation_history/previous_response_id) -> completed,
  output `MANGO`.

Same-`session_id` runs DO inherit full context. No `previous_response_id`
chaining needed; `runs.sh resume` works as designed (harness doc section 3 is
confirmed, the "unverified" limitation is removed).

### (c) Tool use, cwd instruction, timing — VERIFIED

companiond started on 127.0.0.1:7788 (throwaway DB). Run spawned with the
`execute-task` skill embedded in `instructions` and a prompt: `cd` into a
fresh `git init`-ed temp repo, run `curl -s http://127.0.0.1:7788/healthz`,
report pwd/curl output/shell access.

- The run executed real shell commands: `pwd` showed the temp git repo
  (the `cd` instruction was honoured) and curl returned
  `{"status":"ok"}` (companiond reachable).
- Wall time spawn->completed: ~10 s.
- Runtime cwd starts in the Hermes scratch dir; the worker must be told to
  `cd` explicitly (as the harness doc already prescribes for worktrees).

### (e/4) Wake-up path — live-verified (see docs/live-acceptance.md)

- `install.sh` created/updated the `companion-wake` route at
  `/p/companion/webhooks/companion-wake` (deliver telegram,
  mirror-to-session) and registered the companiond subscription with the
  route secret; signature scheme worked as documented (companiond's
  X-Webhook-Signature-V2 was accepted).
- Three live defects found and fixed: route `--skills` resolve against the
  DEFAULT profile (register `skills.external_dirs` there too + gateway
  restart); the companion webhook platform needs
  `tools enable terminal file skills delegation --platform webhook`;
  route prompts must use literal absolute paths (variable indirection is
  blocked by the command scanner). Final test: wake agent ran
  `GET /interruptions/next` (200) and presented the interruption on Telegram.
