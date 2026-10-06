#!/usr/bin/env bash
# install.sh - wire the CompAInion session-driven companion (no webhooks).
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
#      profile (see docs/hermes-gateway-spike.md) via skills.external_dirs.
#
# The companion is session-driven: it is woken by user messages and by its own
# blocking wait (scripts/companion-wait). No webhook route, no companiond
# subscription, no webhook-platform toolsets are needed. To clean up a
# previously installed wake-up path run: install.sh --uninstall-wake
#
# Env overrides: COMPANIOND_URL (default http://127.0.0.1:7777),
# HERMES_HOME (default ~/.hermes), COMPANION_PROFILE (default companion).
set -euo pipefail

DRY_RUN=0
UNINSTALL=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --uninstall-wake) UNINSTALL=1 ;;
    *) echo "usage: install.sh [--dry-run] [--uninstall-wake]" >&2; exit 2 ;;
  esac
done
[[ $((DRY_RUN + UNINSTALL)) -lt 2 ]] || { echo "--dry-run cannot be combined with --uninstall-wake" >&2; exit 2; }

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPANIOND_URL="${COMPANIOND_URL:-http://127.0.0.1:7777}"
HERMES_HOME="${HERMES_HOME:-$HOME/.hermes}"
COMPANION_PROFILE="${COMPANION_PROFILE:-companion}"
ROUTE="companion-wake"

# run CMD... - print the command (always) and execute it unless --dry-run.
run() {
  printf '  $'
  printf ' %q' "$@"
  printf '\n'
  if (( ! DRY_RUN )); then "$@"; fi
}

# run_ok CMD... - like run, but failures are reported without aborting.
run_ok() {
  printf '  $'
  printf ' %q' "$@"
  printf '\n'
  if (( ! DRY_RUN )); then "$@" || echo "   (failed, continuing)"; fi
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

# --- uninstall-wake mode -------------------------------------------------------
if (( UNINSTALL )); then
  echo "CompAInion wake-up cleanup (repo: $repo)"
  echo "1) Remove the Hermes webhook route '$ROUTE'"
  # Note: grep -q would close the pipe early and make the hermes CLI fail with
  # a BrokenPipeError under pipefail; consume the full output instead.
  route_count="$(hermes webhook list 2>/dev/null | grep -c "$ROUTE" || true)"
  if [[ "${route_count:-0}" -gt 0 ]]; then
    run hermes webhook remove "$ROUTE"
  else
    echo "   route not present (nothing to do)"
  fi
  echo "2) Remove the companiond subscription for $ROUTE"
  sub_ids="$(COMPANION_HOME="$repo" "$repo/scripts/capi" GET /subscriptions 2>/dev/null \
    | jq -r '.items[]? | select((.target // "") | contains("companion-wake")) | .id' || true)"
  if [[ -z "$sub_ids" ]]; then
    echo "   no matching subscriptions (daemon unreachable or none registered)"
  else
    while IFS= read -r sid; do
      run env COMPANION_HOME="$repo" "$repo/scripts/capi" DELETE "/subscriptions/$sid"
    done <<<"$sub_ids"
  fi
  echo "3) Disable the wake-up-only webhook toolsets on the '$COMPANION_PROFILE' profile"
  run_ok hermes -p "$COMPANION_PROFILE" tools disable terminal file skills delegation --platform webhook
  echo "Done. The companion is now session-driven only (companion-wait + user messages)."
  echo "NOTE: if the companion gateway is running, restart it for the route removal to take effect - do so only when no runs are in flight."
  exit 0
fi

echo "CompAInion gateway install (repo: $repo)$( (( DRY_RUN )) && printf ' [DRY-RUN]' )"

# --- 1. companiond health -----------------------------------------------------
echo "1) Verify companiond at $COMPANIOND_URL"
if (( DRY_RUN )); then
  printf '  $ curl -fsS %q\n' "$COMPANIOND_URL/healthz"
else
  curl -fsS "$COMPANIOND_URL/healthz" >/dev/null \
    || { echo "   ERROR: companiond is not reachable; start it first (go run ./cmd/companiond)." >&2; exit 1; }
  echo "   OK"
fi

# --- 2. skills visible to the companion AND default profiles ------------------
echo "2) Skills external dir for profiles '$COMPANION_PROFILE' and default"
if (( DRY_RUN )); then
  printf '  $ hermes -p %q config set skills.external_dirs ["%q"]\n' "$COMPANION_PROFILE" "$repo/skills"
  printf '  $ hermes config set skills.external_dirs ["%q"]\n' "$repo/skills"
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

# --- final instructions --------------------------------------------------------
cat <<EOF

Done. To run the companion (session-driven, no webhooks):
  1. Start the backend:  cd $repo && go run ./cmd/companiond
     (listens on 127.0.0.1:7777 by default; set COMPANIOND_ADDR to override)
  2. Make sure the companion gateway is up: hermes -p $COMPANION_PROFILE gateway run
  3. Open the Telegram chat and say 'start'. The companion presents one
     interruption at a time and, when idle, blocks in
     scripts/companion-wait --max 240 (override with COMPANION_WAIT_MAX).
Security: companiond has no auth and binds localhost only; secrets live only
in $HERMES_HOME/.env - never commit or echo them.
EOF
