#!/usr/bin/env bash
# Hermes Runs API helper (Amendment 1 task A3).
#
#   runs.sh spawn  <name> <skills-csv> <prompt-file> [--role R]  -> {harness,run_id,session_id}
#   runs.sh status <run_id>                            -> running|completed|failed|unknown
#   runs.sh output <run_id>                            -> run output text
#   runs.sh resume <session_id> <text|@file> [--role R]          -> new handle JSON
#
# Role (spawn, optional): worker|planner|orchestrator. If omitted it is
# inferred from the skills list (create-plan => planner, execute-plan =>
# orchestrator, otherwise worker). The role picks the per-run model/provider
# and is persisted so `resume` reuses it (--role overrides).
#
# Env:
#   HERMES_API_URL  default http://127.0.0.1:8642/p/companion
#   HERMES_API_KEY  default: API_SERVER_KEY read from ~/.hermes/profiles/companion/.env (never echoed)
#   COMPANION_HOME  repo root; skills read from $COMPANION_HOME/skills/<name>/SKILL.md
#   Per-role model/provider overrides (empty value = omit the field):
#     HERMES_WORKER_{MODEL,PROVIDER}        default openrouter / z-ai/glm-5.3-flash
#     HERMES_PLANNER_{MODEL,PROVIDER}       default anthropic / claude-opus-5-5
#     HERMES_PLANNER_REASONING              default medium (NOT sent: see below)
#     HERMES_ORCHESTRATOR_{MODEL,PROVIDER}  default anthropic / claude-sonnet-5-5
#   The profile default model (claude-opus) is expensive: never run workers on
#   it - every spawn/resume sends explicit model/provider per role.
#   Reasoning effort CANNOT be set per run: the Runs API ignores
#   reasoning_effort/reasoning body fields (runtime.requested carries only
#   provider+model). Set reasoning effort in the profile, not here.
set -euo pipefail

HERMES_API_URL="${HERMES_API_URL:-http://127.0.0.1:8642/p/companion}"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/companion/runs"
HERMES_HOME="${HERMES_HOME:-$HOME/.hermes}"

api_key() {
  if [[ -n "${HERMES_API_KEY:-}" ]]; then
    printf '%s' "$HERMES_API_KEY"
    return
  fi
  local key
  key=$(grep -E '^API_SERVER_KEY=' "$HERMES_HOME/profiles/companion/.env" 2>/dev/null | head -n1 | cut -d= -f2- || true)
  if [[ -z "$key" ]]; then
    echo "runs.sh: HERMES_API_KEY unset and API_SERVER_KEY not found in $HERMES_HOME/profiles/companion/.env" >&2
    exit 1
  fi
  printf '%s' "$key"
}

# json_escape: emit a JSON string literal for stdin content via jq.
json_escape() {
  jq -Rs .
}

# role_from_skills <skills-csv> -> worker|planner|orchestrator
role_from_skills() {
  case ",$1," in
    *,create-plan,*) echo planner ;;
    *,execute-plan,*) echo orchestrator ;;
    *) echo worker ;;
  esac
}

# set_role <role> -> sets ROLE, ROLE_MODEL, ROLE_PROVIDER from env or defaults
set_role() {
  case "$1" in
    worker)
      ROLE_MODEL="${HERMES_WORKER_MODEL-z-ai/glm-5.3-flash}"
      ROLE_PROVIDER="${HERMES_WORKER_PROVIDER-openrouter}" ;;
    planner)
      ROLE_MODEL="${HERMES_PLANNER_MODEL-claude-opus-5-5}"
      ROLE_PROVIDER="${HERMES_PLANNER_PROVIDER-anthropic}" ;;
    orchestrator)
      ROLE_MODEL="${HERMES_ORCHESTRATOR_MODEL-claude-sonnet-5-5}"
      ROLE_PROVIDER="${HERMES_ORCHESTRATOR_PROVIDER-anthropic}" ;;
    *) echo "runs.sh: invalid role '$1' (worker|planner|orchestrator)" >&2; exit 2 ;;
  esac
  ROLE="$1"
}

# save_role <session_id> -> persist ROLE for later resume
save_role() {
  mkdir -p "$STATE_DIR"
  jq -n --arg s "$1" --arg r "$ROLE" '{session_id:$s, role:$r}' \
    > "$STATE_DIR/$1.json"
}

# load_role <session_id> -> ROLE (empty if unknown)
load_role() {
  if [[ -r "$STATE_DIR/$1.json" ]]; then
    jq -r '.role // empty' "$STATE_DIR/$1.json"
  fi
}

# role_fields: emit {"model":...,"provider":...} from ROLE_MODEL/ROLE_PROVIDER
# (omit empty). Reasoning effort is intentionally absent: not supported per run.
model_fields() {
  jq -n --arg m "${ROLE_MODEL:-}" --arg p "${ROLE_PROVIDER:-}" \
    '{ model: (if $m != "" then $m else empty end),
       provider: (if $p != "" then $p else empty end) }'
}

# with_model_fields <body-json> -> body-json merged with model/provider fields
with_model_fields() {
  local mf
  mf=$(model_fields)
  jq -cn --argjson base "$1" --argjson mf "$mf" '$base + $mf'
}

runs_post() { # body-json -> response
  curl -fsS "$HERMES_API_URL/v1/runs" -X POST \
    -H "Authorization: Bearer $(api_key)" \
    -H 'Content-Type: application/json' \
    -d "$1"
}

runs_get() { # path -> response
  curl -fsS "$HERMES_API_URL$1" -H "Authorization: Bearer $(api_key)"
}

embed_skill() { # skill-name -> markdown body appended to buffer (name var: SKILL_PARTS)
  local name="$1" path
  path="$COMPANION_HOME/skills/$name/SKILL.md"
  if [[ ! -r "$path" ]]; then
    echo "runs.sh: skill '$name' not found at $path" >&2
    exit 1
  fi
  SKILL_PARTS+="
### Skill: $name

$(cat "$path")
"
}

handle() { # run_id session_id
  jq -cn --arg r "$1" --arg s "$2" \
    '{harness:"hermes", run_id:$r, session_id:$s}'
}

cmd_spawn() { # name skills-csv prompt-file [--role worker|planner|orchestrator]
  local name="$1" skills_csv="$2" prompt_file="$3"
  shift 3
  local role_arg="${2:-}"
  if [[ "${1:-}" == "--role" ]]; then
    [[ -n "$role_arg" ]] || { echo "runs.sh: --role needs a value" >&2; exit 2; }
  elif [[ $# -gt 0 ]]; then
    echo "runs.sh: unexpected spawn argument: $1" >&2; exit 2
  fi
  set_role "${role_arg:-$(role_from_skills "$skills_csv")}"
  if [[ ! -r "$prompt_file" ]]; then
    echo "runs.sh: prompt file not found: $prompt_file" >&2
    exit 1
  fi
  if [[ -z "${COMPANION_HOME:-}" ]]; then
    echo "runs.sh: COMPANION_HOME must be set to embed skills" >&2
    exit 1
  fi
  SKILL_PARTS=""
  if [[ -n "$skills_csv" ]]; then
    local IFS=','
    for skill in $skills_csv; do
      embed_skill "$(echo "$skill" | tr -d '[:space:]')"
    done
  fi
  local instructions
  instructions="You are Hermes worker '$name'. Follow these skill instructions exactly.

$SKILL_PARTS"
  local input
  input=$(cat "$prompt_file")
  local body
  body=$(with_model_fields "$(jq -n \
    --arg session_id "$name" \
    --argjson instructions "$(printf '%s' "$instructions" | json_escape)" \
    --argjson input "$(printf '%s' "$input" | json_escape)" \
    '{session_id:$session_id, instructions:$instructions, input:$input}')")
  local resp run_id
  resp=$(runs_post "$body")
  run_id=$(jq -r '.run_id' <<<"$resp")
  [[ -n "$run_id" && "$run_id" != "null" ]] || { echo "runs.sh: no run_id in response: $resp" >&2; exit 1; }
  save_role "$name"
  handle "$run_id" "$name"
}

cmd_status() { # run_id
  local resp status
  if ! resp=$(runs_get "/v1/runs/$1"); then
    echo unknown
    return
  fi
  status=$(jq -r '.status // empty' <<<"$resp")
  case "$status" in
    started) echo running ;;
    running|completed|failed|cancelled) echo "$status" ;;
    *) echo unknown ;;
  esac
}

cmd_output() { # run_id
  local resp
  resp=$(runs_get "/v1/runs/$1") || { echo "runs.sh: run $1 not found" >&2; exit 1; }
  jq -r '.output // .error // empty' <<<"$resp"
}

cmd_resume() { # session_id text|@file [--role worker|planner|orchestrator]
  local session_id="$1" arg="$2"
  shift 2
  local role_arg="${2:-}"
  if [[ "${1:-}" == "--role" ]]; then
    [[ -n "$role_arg" ]] || { echo "runs.sh: --role needs a value" >&2; exit 2; }
  elif [[ $# -gt 0 ]]; then
    echo "runs.sh: unexpected resume argument: $1" >&2; exit 2
  fi
  set_role "${role_arg:-$(load_role "$session_id")}"
  local input
  if [[ "$arg" == @* ]]; then
    local file="${arg#@}"
    [[ -r "$file" ]] || { echo "runs.sh: file not found: $file" >&2; exit 1; }
    input=$(cat "$file")
  else
    input="$arg"
  fi
  local body resp run_id
  body=$(with_model_fields "$(jq -n \
    --arg session_id "$session_id" \
    --argjson input "$(printf '%s' "$input" | json_escape)" \
    '{session_id:$session_id, input:$input}')")
  resp=$(runs_post "$body")
  run_id=$(jq -r '.run_id' <<<"$resp")
  [[ -n "$run_id" && "$run_id" != "null" ]] || { echo "runs.sh: no run_id in response: $resp" >&2; exit 1; }
  handle "$run_id" "$session_id"
}

case "${1:-}" in
  spawn)  [[ $# -ge 4 ]] || { echo "usage: runs.sh spawn <name> <skills-csv> <prompt-file> [--role worker|planner|orchestrator]" >&2; exit 2; }; cmd_spawn "${@:2}" ;;
  status) [[ $# -eq 2 ]] || { echo "usage: runs.sh status <run_id>" >&2; exit 2; };        cmd_status "$2" ;;
  output) [[ $# -eq 2 ]] || { echo "usage: runs.sh output <run_id>" >&2; exit 2; };        cmd_output "$2" ;;
  resume) [[ $# -ge 3 ]] || { echo "usage: runs.sh resume <session_id> <text|@file> [--role worker|planner|orchestrator]" >&2; exit 2; }; cmd_resume "${@:2}" ;;
  *) echo "usage: runs.sh {spawn <name> <skills-csv> <prompt-file> [--role R] | status <run_id> | output <run_id> | resume <session_id> <text|@file> [--role R]}" >&2; exit 2 ;;
esac