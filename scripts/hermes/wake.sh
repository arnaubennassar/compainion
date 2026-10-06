#!/usr/bin/env bash
# wake.sh: command-hook subscription handler. Reads COMPANION_EVENT_ID, debounces,
# and only when the companion tmux pane is idle pastes a reconcile signal.
# Env: COMPANION_EVENT_ID, COMPANION_EVENT_TYPE, COMPANION_TMUX (default "companion").
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
harness_dir="$script_dir"
companion="${COMPANION_TMUX:-companion}"
lock_dir="${XDG_RUNTIME_DIR:-/tmp}"
lock="$lock_dir/companion-wake.lock"

# Debounce: skip if invoked < 3s ago.
now=$(date +%s)
if [ -f "$lock" ]; then
  last=$(cat "$lock" 2>/dev/null || echo 0)
  if [ $((now - last)) -lt 3 ]; then
    exit 0
  fi
fi
echo "$now" > "$lock"

state=$(bash "$harness_dir/state.sh" "$companion")
case "$state" in
  working|waiting|gone) exit 0 ;;  # not idle: do nothing; the companion polls anyway
esac

event_id="${COMPANION_EVENT_ID:-unknown}"
printf '[companion-signal] reconcile: GET /interruptions/next (event %s)' "$event_id" \
  > "$TMPDIR/wake.$$"
tmux load-buffer -b "$companion-wake" "$TMPDIR/wake.$$"
rm -f "$TMPDIR/wake.$$"
tmux paste-buffer -b "$companion-wake" -t "$companion"
tmux send-keys -t "$companion" Enter