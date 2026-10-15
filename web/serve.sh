#!/usr/bin/env bash
# Serve the companion status web (this repo's web/) against a chosen state dir.
#
#   ./web/serve.sh [port] [state-dir]
#
# - port:      default 8765
# - state-dir: repo whose companion.json + plans/ hold the state. Defaults to
#              this repo. Pass another repo when the companion runs elsewhere
#              (its state is gitignored, so it lives wherever the companion runs).
#
# When state-dir != this repo, a temporary root is built with symlinks to web/,
# companion.json and the plans dir, so the page's relative reads (../) resolve.
# Bound to 0.0.0.0, so over Tailscale it is reachable at
# http://<tailscale-ip>:<port>/web/  (find it with `tailscale ip -4`).
set -euo pipefail

PORT="${1:-8765}"
BIND="0.0.0.0"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$(cd "${2:-$ROOT}" && pwd)"

TMP=""
cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; }
trap cleanup EXIT

if [ "$STATE_DIR" = "$ROOT" ]; then
  SERVE="$ROOT"
else
  PLANS_DIR="$(node -e "try{process.stdout.write(require('$STATE_DIR/companion.json').plansDir||'plans/')}catch(e){process.stdout.write('plans/')}" 2>/dev/null || echo 'plans/')"
  PLANS_DIR="${PLANS_DIR%/}"
  TMP="$(mktemp -d "${TMPDIR:-/tmp}/companion-web.XXXXXX")"
  ln -s "$ROOT/web" "$TMP/web"
  [ -f "$STATE_DIR/companion.json" ] && ln -s "$STATE_DIR/companion.json" "$TMP/companion.json"
  if [ -d "$STATE_DIR/$PLANS_DIR" ]; then
    mkdir -p "$TMP/$(dirname "$PLANS_DIR")"
    ln -s "$STATE_DIR/$PLANS_DIR" "$TMP/$PLANS_DIR"
  fi
  SERVE="$TMP"
  echo "State dir: ${STATE_DIR}  (plansDir: ${PLANS_DIR})"
fi

TS_IP="$(tailscale ip -4 2>/dev/null | head -1 || true)"
echo "Serving ${SERVE} on ${BIND}:${PORT}"
echo "Companion status:    http://localhost:${PORT}/web/"
[ -n "${TS_IP}" ] && echo "Over Tailscale:       http://${TS_IP}:${PORT}/web/"
echo "Demo (sample data):  http://localhost:${PORT}/web/?demo=1"

python3 -m http.server "${PORT}" --bind "${BIND}" --directory "${SERVE}"
