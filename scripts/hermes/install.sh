#!/usr/bin/env bash
# install.sh - wire the CompAInion wake-up path (Amendment 1, task A4).
#
# Note: worker runs (scripts/hermes/runs.sh) execute on the companion profile
# (HERMES_API_URL default http://127.0.0.1:8642/p/companion) because the default
# profile has no LLM provider configured.
#
# Idempotent. With --dry-run it prints every command instead of running it.
#
# Steps:
#   1. Verify companiond answers on $COMPANIOND_URL/healthz.
#   2. Make the repo skills visible to the companion profile AND the default
#      profile (the webhook platform resolves route --skills against the
#      default profile) via skills.external_dirs.
#   2b. Enable terminal/file/skills/delegation toolsets on the companion
#       profile's webhook platform (otherwise the wake-up agent cannot run capi).
#   3. Create the Hermes webhook route `companion-wake` (deliver telegram,
#      mirror to session, bound to the companion profile) if absent.
#   4. Read the route's auto-generated secret from
#      ~/.hermes/webhook_subscriptions.json (never echoed).
#   5. Register the companiond -> Hermes webhook subscription via scripts/capi.
#   6. Print final instructions (start companiond, message the bot).
#
# Env overrides: COMPANIOND_URL (default http://127.0.0.1:7777),
# HERMES_WEBHOOK_PORT (default 8644), HERMES_HOME (default ~/.hermes),
# COMPANION_PROFILE (default companion).
set -euo pipefail

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
  [[ $# -eq 1 ]] || { echo "usage: install.sh [--dry-run]" >&2; exit 2; }
elif [[ $# -gt 0 ]]; then
  echo "usage: install.sh [--dry-run]" >&2
  exit 2
fi

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPANIOND_URL="${COMPANIOND_URL:-http://127.0.0.1:7777}"
HERMES_WEBHOOK_PORT="${HERMES_WEBHOOK_PORT:-8644}"
HERMES_HOME="${HERMES_HOME:-$HOME/.hermes}"
COMPANION_PROFILE="${COMPANION_PROFILE:-companion}"
ROUTE="companion-wake"
TARGET="http://127.0.0.1:${HERMES_WEBHOOK_PORT}/p/${COMPANION_PROFILE}/webhooks/${ROUTE}"
SUBS_FILE="$HERMES_HOME/webhook_subscriptions.json"

# run CMD... - print the command (always) and execute it unless --dry-run.
run() {
  printf '  $'
  printf ' %q' "$@"
  printf '\n'
  if (( ! DRY_RUN )); then "$@"; fi
}

# try CMD... - like run, but in dry-run mode only prints (never fails).
try() {
  if (( DRY_RUN )); then
    printf '  [dry-run] $'
    printf ' %q' "$@"
    printf '\n'
  else
    "$@"
  fi
}

manual_skills_instruction() {
  cat <<EOF
  MANUAL STEP REQUIRED: could not register the skills dir with the
  '$COMPANION_PROFILE' profile via the CLI. Run exactly:

    hermes -p $COMPANION_PROFILE config set skills.external_dirs '["$repo/skills"]'
    hermes -p $COMPANION_PROFILE config get skills.external_dirs   # must list $repo/skills

  (Never hand-edit ~/.hermes/config.yaml - the Hermes invariant.)
EOF
}

echo "CompAInion gateway install (repo: $repo)$( (( DRY_RUN )) && printf ' [DRY-RUN]' )"

# --- 1. companiond health -----------------------------------------------------
echo "1) Verify companiond at $COMPANIOND_URL"
if (( DRY_RUN )); then
  try curl -fsS "$COMPANIOND_URL/healthz"
else
  curl -fsS "$COMPANIOND_URL/healthz" >/dev/null \
    || { echo "   ERROR: companiond is not reachable; start it first (go run ./cmd/companiond)." >&2; exit 1; }
  echo "   OK"
fi

# --- 2. skills visible to the companion AND default profiles ------------------
echo "2) Skills external dir for profiles '$COMPANION_PROFILE' and default"
if (( DRY_RUN )); then
  try hermes -p "$COMPANION_PROFILE" config get skills.external_dirs
  try hermes -p "$COMPANION_PROFILE" config set skills.external_dirs "[\"$repo/skills\"]"
  try hermes config set skills.external_dirs "[\"$repo/skills\"]"
else
  for prof in "$COMPANION_PROFILE" ""; do
    if [[ -n "$prof" ]]; then
      cfg=(hermes -p "$prof" config set skills.external_dirs "[\"$repo/skills\"]")
      get=(hermes -p "$prof" config get skills.external_dirs)
    else
      cfg=(hermes config set skills.external_dirs "[\"$repo/skills\"]")
      get=(hermes config get skills.external_dirs)
      prof=default
    fi
    current="$("${get[@]}" 2>/dev/null || true)"
    if [[ -n "$current" ]] && grep -qF "$repo/skills" <<<"$current"; then
      echo "   $prof: already registered"
    else
      run "${cfg[@]}"
      current="$("${get[@]}" 2>/dev/null || true)"
      if [[ -z "$current" ]] || ! grep -qF "$repo/skills" <<<"$current"; then
        manual_skills_instruction
        exit 1
      fi
    fi
  done
fi

# --- 2b. webhook-platform toolsets on the companion profile --------------------
echo "2b) Toolsets for the '$COMPANION_PROFILE' webhook platform"
# Without terminal/file/skills/delegation the wake-up agent cannot run capi.
if (( DRY_RUN )); then
  try hermes -p "$COMPANION_PROFILE" tools enable terminal file skills delegation --platform webhook
else
  enabled="$(hermes -p "$COMPANION_PROFILE" tools list --platform webhook 2>/dev/null || true)"
  if grep -q 'enabled  terminal' <<<"$enabled" && grep -q 'enabled  delegation' <<<"$enabled"; then
    echo "   already enabled"
  else
    run hermes -p "$COMPANION_PROFILE" tools enable terminal file skills delegation --platform webhook
  fi
fi

# --- 3. Hermes webhook route ---------------------------------------------------
echo "3) Hermes webhook route '$ROUTE'"
# Static prompt (the route prompt cannot interpolate): absolute repo path and
# capi path so the wake-up agent knows exactly what to run.
# Use LITERAL paths, not $VAR indirection: the gateway command scanner flags
# nested/variable executable bodies and blocks the terminal call behind an
# approval the webhook agent cannot answer (live-verified failure mode).
WAKE_PROMPT="You are the CompAInion companion wake-up. Load the \`companion\` skill. Export COMPANIOND_URL=$COMPANIOND_URL (COMPANION_HOME is $repo). Then run exactly: $repo/scripts/capi GET /interruptions/next - and present the next interruption per the companion skill's Telegram presentation rules (digest line first, one question at a time, numbered suggestions, recommended first). If there is none, reply exactly: Nothing pending."
ROUTE_CMD=(hermes webhook subscribe "$ROUTE"
  --prompt "$WAKE_PROMPT"
  --skills companion
  --route-profile "$COMPANION_PROFILE"
  --deliver telegram
  --mirror-to-session)
# Re-running subscribe UPDATES the existing route (new prompt), but rotates the
# secret - the companiond subscription in step 5 is therefore always refreshed.
if (( DRY_RUN )); then
  echo "   (subscribe creates the route if absent and updates it otherwise)"
  run "${ROUTE_CMD[@]}"
else
  run "${ROUTE_CMD[@]}"
fi

# --- 4. route secret -----------------------------------------------------------
echo "4) Route secret from $SUBS_FILE (never echoed)"
if (( DRY_RUN )); then
  echo "  \$ jq -r --arg n $ROUTE '.[\$n].secret // empty' $SUBS_FILE   # -> <secret>"
  SECRET="<secret>"
else
  [[ -r "$SUBS_FILE" ]] || { echo "   ERROR: $SUBS_FILE not readable; was the route created?" >&2; exit 1; }
  SECRET="$(jq -r --arg n "$ROUTE" '.[$n].secret // empty' "$SUBS_FILE")"
  [[ -n "$SECRET" ]] || { echo "   ERROR: no secret found for route '$ROUTE' in $SUBS_FILE" >&2; exit 1; }
fi

# --- 5. companiond subscription -------------------------------------------------
echo "5) companiond webhook subscription -> $TARGET"
BODY_PRINT='{"method":"webhook","target":"'"$TARGET"'","types":["note"],"filter":{"kind":["interruption_created","agent_waiting","agent_lost"]},"secret":"<secret>"}'
if (( DRY_RUN )); then
  echo "  \$ COMPANION_HOME=$repo $repo/scripts/capi DELETE /subscriptions/<old-$ROUTE>"
  echo "  \$ COMPANION_HOME=$repo $repo/scripts/capi POST /subscriptions '$BODY_PRINT'"
else
  # delete stale subscriptions pointing at the same route (secret rotated above)
  old_ids="$(COMPANION_HOME="$repo" "$repo/scripts/capi" GET /subscriptions 2>/dev/null \
    | jq -r --arg t "$TARGET" '.items[]? | select(.target == $t) | .id' || true)"
  for old in $old_ids; do
    run env COMPANION_HOME="$repo" "$repo/scripts/capi" DELETE "/subscriptions/$old"
  done
  body="$(jq -n --arg target "$TARGET" --arg secret "$SECRET" \
    '{method:"webhook", target:$target, types:["note"],
      filter:{kind:["interruption_created","agent_waiting","agent_lost"]},
      secret:$secret}')"
  COMPANION_HOME="$repo" "$repo/scripts/capi" POST /subscriptions "$body"
  echo "   subscription registered"
fi

# --- 6. final instructions -------------------------------------------------------
cat <<EOF

Done. To run the companion:
  1. Start the backend:  cd $repo && go run ./cmd/companiond
     (listens on 127.0.0.1:7777 by default; set COMPANIOND_ADDR to override)
  2. Make sure the companion gateway is up: hermes -p $COMPANION_PROFILE gateway run
  3. Message your bot on Telegram. It presents one interruption at a time;
     webhook events wake it via the '$ROUTE' route.
Security: companiond has no auth and binds localhost only; secrets live only
in $HERMES_HOME/.env / $SUBS_FILE - never commit or echo them.
EOF