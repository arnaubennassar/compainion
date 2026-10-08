#!/usr/bin/env bash
# Tests for the capi @file staleness guard (capi exits 23 on stale/reused bodies).
# Run: bash scripts/test-capi-bodyguard.sh
set -u

cd "$(dirname "$0")/.."
CAPI=./scripts/capi
export CAPI_BODY_REUSE_WINDOW=120
# Unreachable daemon: a passing guard lets curl fail with exit 7; the guard
# itself exits 23 before curl runs, so exit codes cleanly separate the paths.
export COMPANIOND_URL=http://127.0.0.1:1

T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

# 1. Missing @file body is refused.
out=$("$CAPI" POST /tasks @"$T/nope.json" 2>&1); rc=$?
[ "$rc" -eq 23 ] || fail "missing file: want exit 23, got $rc (out: $out)"

# 2. Stale file (mtime before capi process start) is refused.
printf '{"x":1}' > "$T/stale.json"
touch -d '2020-01-01 00:00:00' "$T/stale.json"
out=$("$CAPI" POST /tasks @"$T/stale.json" 2>&1); rc=$?
[ "$rc" -eq 23 ] || fail "stale file: want exit 23, got $rc (out: $out)"
case "$out" in *STALE*) ;; *) fail "stale message missing (out: $out)" ;; esac

# 3. Fresh, unique file passes the guard (curl's connection error, exit 7).
printf '{"x":1}' > "$T/fresh.json"
"$CAPI" POST /tasks @"$T/fresh.json" >/dev/null 2>&1; rc=$?
[ "$rc" -eq 7 ] || fail "fresh file: want curl exit 7, got $rc"

# 4. Same path + unchanged mtime within the window is refused.
out=$("$CAPI" POST /tasks @"$T/fresh.json" 2>&1); rc=$?
[ "$rc" -eq 23 ] || fail "reuse: want exit 23, got $rc (out: $out)"
case "$out" in *reused*) ;; *) fail "reuse message missing (out: $out)" ;; esac

# 5. Rewriting the file (new mtime) clears the reuse refusal.
sleep 1
printf '{"x":2}' > "$T/fresh.json"
"$CAPI" POST /tasks @"$T/fresh.json" >/dev/null 2>&1; rc=$?
[ "$rc" -eq 7 ] || fail "rewritten file: want curl exit 7, got $rc"

# 6. Window 0 disables the reuse guard entirely.
"$CAPI" POST /tasks @"$T/fresh.json" >/dev/null 2>&1; rc=$?  # record 2nd use
export CAPI_BODY_REUSE_WINDOW=0
"$CAPI" POST /tasks @"$T/fresh.json" >/dev/null 2>&1; rc=$?
[ "$rc" -eq 7 ] || fail "window=0: want curl exit 7, got $rc"

echo "test-capi-bodyguard: all ok"