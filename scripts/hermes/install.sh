#!/usr/bin/env bash
# install.sh: register skills dir + wake subscription, print how to start the companion.
# NOT idempotent-proof of hermes config list syntax: verifies at runtime.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$script_dir/../.." && pwd)"

echo "1) Registering skills external dir: $repo/skills"
# Verify the list syntax first; if unsupported, print the manual instruction
# and exit without touching the config by hand (Hermes invariant).
if ! hermes config set --help 2>&1 | grep -qi 'external_dirs'; then
  echo "   WARNING: could not confirm list syntax for 'hermes config set skills.external_dirs'."
  echo "   MANUAL INSTRUCTION: run 'hermes config set --help' and add"
  echo "   \"$repo/skills\" to skills.external_dirs via hermes config set"
  echo "   (never hand-edit ~/.hermes/config.yaml)."
else
  hermes config set skills.external_dirs "[$repo/skills]"
fi

echo "2) Registering wake subscription (command hook)"
export COMPANION_HOME="$repo"
"$script_dir/../capi" POST /subscriptions "$(printf '{
  "method": "command",
  "target": "%s/scripts/hermes/wake.sh",
  "types": ["note", "needs_input"],
  "filter": {"kind": ["interruption_created", "agent_waiting", "agent_lost"]}
}' "$repo")"
echo "   subscription registered (COMPANION_EVENT_ID/COMPANION_EVENT_TYPE are set by companiond)"

echo "3) Start the companion:"
echo "   tmux new-session -s companion 'hermes -s companion'"