#!/usr/bin/env bash
# say.sh <name> <text-or-@file>: paste text into the tmux pane via a buffer
# (never send-keys with raw text — shell metacharacters stay literal) then Enter.
set -euo pipefail
[ $# -eq 2 ] || { echo "usage: say.sh <name> <text-or-@file>" >&2; exit 2; }
name="$1"; arg="$2"

buf="$TMPDIR/say.$$"
if [ "${arg#@}" != "$arg" ]; then
  f="${arg#@}"
  [ -f "$f" ] || { echo "say.sh: file not found: $f" >&2; exit 2; }
  cp "$f" "$buf"
else
  printf '%s' "$arg" > "$buf"
fi

tmux load-buffer -b "$name-say" "$buf"
rm -f "$buf"
tmux paste-buffer -b "$name-say" -t "$name"
tmux send-keys -t "$name" Enter