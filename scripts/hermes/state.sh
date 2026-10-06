#!/usr/bin/env bash
# state.sh <name>: prints one of working|waiting|idle|gone for the agent's tmux pane.
# Heuristics tuned against tmux screen scraping; see the risk note in the plan.
# `waiting` = tail matches approval/confirm patterns; `working` = output changed
# between two captures 2s apart; else `idle`.
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: state.sh <name>" >&2; exit 2; }
name="$1"

# Approval/confirm heuristics — tune against real Hermes output.
WAITING_PATTERNS='\[y/N\]|\(y/n\)|Approve|Do you want|Press Enter'

if ! tmux has-session -t "$name" 2>/dev/null; then
  echo "gone"
  exit 0
fi

tail=$(tmux capture-pane -t "$name" -p -S -15 | sed '/^[[:space:]]*$/d' | tail -n 15)
if grep -Eq "$WAITING_PATTERNS" <<<"$tail"; then
  echo "waiting"
  exit 0
fi

t1=$(tmux capture-pane -t "$name" -p -S -15)
sleep 2
t2=$(tmux capture-pane -t "$name" -p -S -15)
if [ "$t1" != "$t2" ]; then
  echo "working"
else
  echo "idle"
fi