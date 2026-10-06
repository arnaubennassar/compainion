#!/usr/bin/env bash
# test-companion-wait.sh - black-box tests for scripts/companion-wait against a
# throwaway companiond (127.0.0.1:7791, temp DB, temp XDG_STATE_HOME).
# Prints 'companion-wait: OK' and exits 0 when all cases pass.
set -euo pipefail
cd "$(dirname "$0")/.."

tmp="$(mktemp -d)"
daemon=""
cleanup() {
  if [[ -n "$daemon" ]]; then kill "$daemon" 2>/dev/null || true; wait "$daemon" 2>/dev/null || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT

export COMPANIOND_URL="http://127.0.0.1:7791"
export XDG_STATE_HOME="$tmp/state"
mkdir -p "$tmp/state"

go build -o "$tmp/companiond" ./cmd/companiond
COMPANIOND_ADDR="127.0.0.1:7791" COMPANIOND_DB="$tmp/db.sqlite" "$tmp/companiond" &
daemon=$!
for _ in $(seq 1 50); do
  curl -fsS "$COMPANIOND_URL/healthz" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS "$COMPANIOND_URL/healthz" >/dev/null

fail() { echo "FAIL $1" >&2; exit 1; }

# --- case 1: --max 3 with nothing pending -> {"wake":"timeout"} after >= 3s ---
start="$(date +%s)"
out="$(scripts/companion-wait --max 3)"
elapsed=$(( $(date +%s) - start ))
[[ "$(jq -r .wake <<<"$out")" == "timeout" ]] || fail "case1 wake=$out"
[[ "$elapsed" -ge 3 ]] || fail "case1 elapsed=$elapsed < 3"
[[ "$elapsed" -lt 10 ]] || fail "case1 elapsed=$elapsed >= 10"

# --- case 2: interruption created after 2s wakes with wake=interruption <6s ---
( sleep 2; scripts/capi POST /interruptions \
    '{"topic":"t","kind":"decision","priority":"high","digest":"d","blocking":true,"questions":[{"position":1,"text":"q?","answer_type":"confirm","suggestions":[{"label":"A","recommended":true},{"label":"B"}]}]}' \
    >/dev/null ) &
bg=$!
start="$(date +%s)"
out="$(scripts/companion-wait --max 15)"
elapsed=$(( $(date +%s) - start ))
wait "$bg" || true
[[ "$(jq -r .wake <<<"$out")" == "interruption" ]] || fail "case2 wake=$out"
[[ "$(jq -r .interruption.digest <<<"$out")" == "d" ]] || fail "case2 body=$out"
[[ "$elapsed" -lt 6 ]] || fail "case2 elapsed=$elapsed >= 6"

# --- case 3: a finished event wakes with wake=events; cursor advances --------
scripts/capi POST /events '{"type":"finished","payload":{"kind":"done"}}' >/dev/null
out="$(scripts/companion-wait --max 15)"
[[ "$(jq -r .wake <<<"$out")" == "events" ]] || fail "case3 wake=$out"
[[ "$(jq -r '.events | length' <<<"$out")" -ge 1 ]] || fail "case3 empty events=$out"
[[ "$(jq -r '.events[0].type' <<<"$out")" == "finished" ]] || fail "case3 type=$out"
cursor_file="${XDG_STATE_HOME:-$HOME/.local/state}/companion/cursor"
[[ -s "$cursor_file" ]] || fail "case3 cursor file missing"

# immediate second call must NOT re-see the same event -> timeout
out="$(scripts/companion-wait --max 2)"
[[ "$(jq -r .wake <<<"$out")" == "timeout" ]] || fail "case3b re-wake=$out"

# --- case 4: self-authored events are ignored when COMPANION_AGENT_ID is set --
scripts/capi POST /agents '{"id":"companion-main","role":"companion","harness":"hermes","status":"running"}' >/dev/null
scripts/capi POST /events '{"type":"finished","payload":{},"agent_id":"companion-main"}' >/dev/null
out="$(COMPANION_AGENT_ID=companion-main scripts/companion-wait --max 2)"
[[ "$(jq -r .wake <<<"$out")" == "timeout" ]] || fail "case4 self-wake=$out"
scripts/capi POST /agents '{"id":"worker-1","role":"worker","harness":"hermes","status":"running"}' >/dev/null
scripts/capi POST /events '{"type":"finished","payload":{"kind":"other"},"agent_id":"worker-1"}' >/dev/null
out="$(scripts/companion-wait --max 2)"
[[ "$(jq -r .wake <<<"$out")" == "events" ]] || fail "case4b no-filter=$out"

echo "companion-wait: OK"
