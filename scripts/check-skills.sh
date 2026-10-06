#!/usr/bin/env bash
# check-skills.sh - validate skills/*/SKILL.md frontmatter and capi call paths
# against api/openapi.yaml. Usage: scripts/check-skills.sh
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
n=0

# 1. Frontmatter check
for f in skills/*/SKILL.md; do
  head=""; in_fm=0
  while IFS= read -r line; do
    if [ "$in_fm" = 0 ] && [ "$line" = "---" ]; then in_fm=1; continue; fi
    if [ "$in_fm" = 1 ]; then
      [ "$line" = "---" ] && break
      head+="$line"$'\n'
    fi
  done < "$f"
  grep -q '^name:' <<<"$head" || { echo "FAIL $f: frontmatter missing name:"; fail=1; }
  grep -q '^description:' <<<"$head" || { echo "FAIL $f: frontmatter missing description:"; fail=1; }
done

# 2. Every `capi <METHOD> <PATH>` must match a route in api/openapi.yaml
check_py() {
python3 - "$@" <<'PY'
import re, sys, glob, json

spec_path = "api/openapi.yaml"
def norm(p):
    p = p.split("?", 1)[0]                      # strip query string
    segs = []
    for s in p.strip("/").split("/"):
        if not s:
            continue
        if s.startswith("{") and s.endswith("}") or "$" in s or "<" in s:
            segs.append("{param}")
        else:
            segs.append(s)
    return "/" + "/".join(segs)

routes = set()
try:
    import yaml
    with open(spec_path) as fh:
        doc = yaml.safe_load(fh)
    for path, item in doc.get("paths", {}).items():
        for m in item:
            if m.lower() in ("get", "post", "patch", "put", "delete"):
                routes.add((m.upper(), norm(path)))
except ImportError:
    # grep fallback: parse "  /path:" blocks and the indented method keys
    cur = None
    for line in open(spec_path):
        m = re.match(r"^  (/.+?):\s*$", line)
        if m:
            cur = m.group(1)
            continue
        m = re.match(r"^    (get|post|patch|put|delete):\s*$", line)
        if m and cur:
            routes.add((m.group(1).upper(), norm(cur)))

failures = []
count = 0
for f in sorted(glob.glob("skills/**/*", recursive=True)):
    if not f.endswith(".md"):
        continue
    text = open(f).read()
    for m in re.finditer(r"capi\s+(GET|POST|PATCH|PUT|DELETE)\s+(\"([^\"]+)\"|'([^']+)'|(\S+))", text):
        raw = next(g for g in m.groups()[1:] if g)
        path = raw.strip("\"'")
        path = re.sub(r"[`.,;:]+$", "", path)
        count += 1
        got = (m.group(1), norm(path))
        if got not in routes:
            failures.append(f"{f}: {m.group(1)} {path} -> no route {got[0]} {got[1]}")

if failures:
    print("\n".join(failures))
    sys.exit(1)
print(count)
PY
}

out=$(check_py) || { echo "FAIL capi route check:"; echo "$out"; exit 1; }
n=$out

if [ "$fail" = 1 ]; then exit 1; fi
echo "skills: OK ($n capi calls checked)"