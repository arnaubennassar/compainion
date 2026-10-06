#!/usr/bin/env bash
# spawn.sh <name> <workdir> <skills-csv> <prompt-file> [--worktree]
# Spawns a detached Hermes agent in a tmux session, waits for the prompt,
# then loads the prompt file via a tmux buffer (never shell-interpolated).
# Prints the agent handle JSON: {"harness":"hermes","tmux_session":...,"workdir":...}
set -euo pipefail

usage() { echo "usage: spawn.sh <name> <workdir> <skills-csv> <prompt-file> [--worktree]" >&2; exit 2; }
[ $# -ge 4 ] || usage

name="$1"; workdir="$2"; skills="$3"; prompt_file="$4"
WT=""
if [ $# -ge 5 ]; then
  [ "$5" = "--worktree" ] || usage
  WT=1
fi

[ -f "$prompt_file" ] || { echo "spawn.sh: prompt file not found: $prompt_file" >&2; exit 2; }
[ -d "$workdir" ] || { echo "spawn.sh: workdir not found: $workdir" >&2; exit 2; }

cmd="hermes -s '$skills'"
[ -n "$WT" ] && cmd="$cmd -w"

tmux new-session -d -s "$name" -c "$workdir" "$cmd"

# Wait up to 30s until the pane shows a non-empty last line (prompt up).
ok=0
for _ in $(seq 1 30); do
  last=$(tmux capture-pane -t "$name" -p 2>/dev/null | sed '/^[[:space:]]*$/d' | tail -n 1 || true)
  if [ -n "${last:-}" ]; then ok=1; break; fi
  sleep 1
done
if [ "$ok" -ne 1 ]; then
  echo "spawn.sh: hermes session '$name' did not show a prompt within 30s" >&2
  exit 1
fi

tmux load-buffer -b "$name" "$prompt_file"
tmux paste-buffer -b "$name" -t "$name"
tmux send-keys -t "$name" Enter

printf '{"harness":"hermes","tmux_session":"%s","workdir":"%s"}\n' "$name" "$workdir"