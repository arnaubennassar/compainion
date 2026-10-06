# CompAInion

A Go REST backend (`companiond`) plus a set of Hermes skills so a Hermes
"companion" profile can dispatch plans/tasks to detached Hermes workers and
serve the user one interruption at a time over Telegram.

- Backend: stdlib `net/http` + SQLite (`modernc.org/sqlite`), OpenAPI-documented.
- Companion: Hermes profile `companion` chatting with you on Telegram; its
  state lives entirely in the API, so context resets are safe.
- Workers/orchestrators: headless Hermes runs via the gateway Runs API.

## Quick start

```bash
# 1. Build and run the backend (localhost only, no auth)
make build && ./bin/companiond          # or: go run ./cmd/companiond

# 2. One-time user setup (secrets are user-only, never agent-written):
#    hermes profile create companion
#    hermes -p companion gateway setup    # Telegram token, webhook + API server keys

# 3. Wire the gateway: skills dir, 'companion-wake' route, companiond subscription
scripts/hermes/install.sh               # add --dry-run to preview every command

# 4. Start the companion gateway and message your bot on Telegram
hermes -p companion gateway run
```

The wake-up path: companiond webhook subscription → Hermes route
`companion-wake` (`/p/companion/webhooks/companion-wake`, delivers to Telegram,
mirrors to the chat session) → the companion runs
`GET /interruptions/next` and presents one interruption → your reply is
reconciled against `GET /interruptions?status=presented`.

Worker lifecycle (spawn / status / resume / output) goes through
`scripts/hermes/runs.sh`; workers run on the companion profile with per-role
models (`worker` = openrouter/z-ai-glm-5.3-flash, `planner` = anthropic
claude-opus-5-5, `orchestrator` = anthropic claude-sonnet-5-5; the role is
inferred from the skills list). See `skills/companion/harnesses/hermes.md` for
the full harness contract (registration order, waiting/lost semantics, approval
gate, Telegram presentation rules, limitations).

## Layout

```
api/openapi.yaml            REST contract (embedded, served at GET /openapi.yaml)
cmd/companiond/             daemon entry point
internal/domain|store|api|events|notify|ids|config|testutil
skills/companion/           companion skill + harness doc (harnesses/hermes.md)
skills/create-plan|execute-plan|execute-task/   worker skills
scripts/capi                curl+jq REST client used by all skills
scripts/hermes/runs.sh      gateway Runs API helper (spawn/status/resume/output;
                            companion profile: /p/companion)
scripts/hermes/install.sh   gateway wiring (idempotent, --dry-run)
scripts/check-skills.sh     validates skill docs against openapi.yaml
docs/hermes-gateway-spike.md  verified gateway behaviour (signatures, profiles, ports)
```

## Security notes

- `companiond` has **no authentication** and binds `127.0.0.1:7777` by default;
  it refuses non-loopback binds unless `COMPANIOND_ALLOW_NON_LOOPBACK=1`.
  Never expose it beyond localhost.
- Secrets (Telegram bot token, `API_SERVER_KEY`, webhook HMAC secrets) live
  only in `~/.hermes/.env`, `~/.hermes/profiles/companion/.env` and
  `~/.hermes/webhook_subscriptions.json`. Never commit, log or echo them.
- The Hermes webhook platform should listen on `127.0.0.1`
  (`hermes config set platforms.webhook.host 127.0.0.1`).