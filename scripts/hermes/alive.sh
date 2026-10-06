#!/usr/bin/env bash
# alive.sh <name>: exit 0 if tmux session <name> exists, else 1.
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: alive.sh <name>" >&2; exit 2; }
tmux has-session -t "$1" 2>/dev/null