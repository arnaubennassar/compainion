#!/usr/env bash
# Self-test for the runs.sh spawn session_id guard (task 01M49GJ1DV4QZHGS134TAC0C8R).
# Does not spawn anything: invalid names must be rejected at the guard; a valid
# name must get past the guard (it then fails on the missing prompt file).
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNS="$HERE/runs.sh"
fails=0

expect_invalid() { # name
  local out rc
  out=$(bash "$RUNS" spawn "$1" '' /dev/null 2>&1); rc=$?
  if [[ $rc -eq 2 && "$out" == *'invalid spawn name/session_id'* ]]; then
    echo "ok: rejected '$1' (rc=$rc): $out"
  else
    echo "FAIL: expected guard rejection for '$1', got rc=$rc: $out"; fails=$((fails+1))
  fi
}

expect_guard_pass() { # name
  local out rc
  out=$(HERMES_API_KEY=dummy-test bash "$RUNS" spawn "$1" '' /nonexistent-prompt-file 2>&1); rc=$?
  if [[ $rc -eq 1 && "$out" == *'prompt file not found'* ]]; then
    echo "ok: guard passed for '$1' (failed later on prompt file, rc=$rc)"
  elif [[ "$out" == *'invalid spawn name/session_id'* ]]; then
    echo "FAIL: guard wrongly rejected valid name '$1': $out"; fails=$((fails+1))
  else
    echo "FAIL: unexpected result for '$1' rc=$rc: $out"; fails=$((fails+1))
  fi
}

expect_invalid ""
expect_invalid "worker-task-"
expect_invalid "Bad_Name"
expect_invalid "has space"
expect_invalid "UPPER"
expect_invalid "dot.name"
expect_guard_pass "worker-task-01m49abc"
expect_guard_pass "test-worker"

echo "---"
if [[ $fails -eq 0 ]]; then echo "ALL PASS"; else echo "$fails FAILURES"; exit 1; fi
