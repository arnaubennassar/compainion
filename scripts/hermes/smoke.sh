#!/usr/bin/env bash
# smoke.sh: bats-free smoke test of the hermes harness scripts using a fake agent
# (`cat` in tmux). Prints "smoke: OK" on success.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
name="cmp-smoke"
fails=0

check() { # check <desc> <cmd...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    echo "ok: $desc"
  else
    echo "FAIL: $desc"; fails=$((fails+1))
  fi
}

tmux kill-session -t "$name" 2>/dev/null || true
tmux new-session -d -s "$name" 'cat'

check "alive before kill" bash "$script_dir/alive.sh" "$name"

# say.sh with plain text
bash "$script_dir/say.sh" "$name" "hello world"
check "peek shows plain text" bash -c "bash '$script_dir/peek.sh' '$name' 20 | grep -q 'hello world'"

# say.sh with shell metacharacters must be echoed literally and safe
evil="\"; \$(rm -rf x) \`"
bash "$script_dir/say.sh" "$name" "$evil"
check "peek shows metachar text literally" \
  bash -c "bash '$script_dir/peek.sh' '$name' 20 | grep -qF '$evil'"
if [ -e x ]; then
  echo "FAIL: injection attempt created 'x'"; fails=$((fails+1))
else
  echo "ok: no 'x' file created by injection attempt"
fi

st=$(bash "$script_dir/state.sh" "$name")
case "$st" in
  idle|working) echo "ok: state is $st" ;;
  *) echo "FAIL: state is $st (wanted idle|working)"; fails=$((fails+1)) ;;
esac

tmux kill-session -t "$name"
check "alive exits 1 after kill" bash -c "bash '$script_dir/alive.sh' '$name' && exit 1 || exit 0"
st=$(bash "$script_dir/state.sh" "$name")
if [ "$st" = "gone" ]; then echo "ok: state is gone"; else echo "FAIL: state after kill is $st"; fails=$((fails+1)); fi

if [ "$fails" -gt 0 ]; then
  echo "smoke: FAILED ($fails check(s) failed)"
  exit 1
fi
echo "smoke: OK"