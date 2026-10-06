#!/usr/bin/env bash
# peek.sh <name> [lines=60]: read the last N lines of the agent's tmux pane. Read only.
set -euo pipefail
if [ $# -lt 1 ] || [ $# -gt 2 ]; then echo "usage: peek.sh <name> [lines=60]" >&2; exit 2; fi
lines="${2:-60}"
case "$lines" in ''|*[!0-9]*) echo "peek.sh: lines must be an integer" >&2; exit 2;; esac
tmux capture-pane -t "$1" -p -S -"$lines"