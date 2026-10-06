# CompAInion (Hermes-only) Implementation Plan

> For the implementer: work task by task, in order. TDD every code task (failing test -> run, see it fail -> minimal code -> run, see it pass -> commit). One commit per task, message given in each task. Do not add features not listed here (YAGNI). No dashboard UI in this plan (see "Out of scope").

## Goal

Ship a Go REST backend (`companiond`, OpenAPI-documented, SQLite) plus a set of Hermes-loadable skills (`/companion`, create-plan, execute-plan, execute-task, `harnesses/hermes.md`) so a Hermes session acting as "companion" can dispatch plans/tasks to detached Hermes workers/orchestrators and serve the user one interruption at a time.

## Current context / assumptions

- Repo `$COMPANION_HOME` (i.e. `<home-dir>/repos/arnaubennassar/compainion`) holds only `README.md` (12 bytes) and one commit. Greenfield. (Note the directory is spelled `compainion`; the product is "CompAInion"; Go module name: `github.com/arnaubennassar/compainion`. Confirm the real remote with `git remote -v`; if none, keep this name.)
- Toolchain on the box: Go 1.25.11, tmux, curl, jq, hermes (`~/.local/bin/hermes`). NO `sqlite3` CLI -> use a pure-Go driver (`modernc.org/sqlite`, no cgo) and verify DB state through the API or Go tests.
- Hermes facts verified locally (`hermes chat --help`):
  - `hermes chat -q "<text>" --oneshot -Q` answers and exits (non-interactive worker). `--query-file PATH` is safe for arbitrary text (use it for step payloads, never shell-interpolate).
  - `-s/--skills a,b` preloads skills; `-w/--worktree` isolates code-editing agents; `--format stream-json` emits JSONL; `--yolo` skips approvals (do NOT use by default); `--max-turns`, `--run-budget SECONDS` exist.
  - Interactive Hermes needs a real TTY -> detached agents run in tmux (`tmux new-session -d -s <name> 'hermes ...'`), input via `tmux send-keys`, output via `tmux capture-pane -p`.
  - Skills are loaded from `~/.hermes/skills/` and extra dirs listed in `skills.external_dirs` in `~/.hermes/config.yaml`. Config must be changed with `hermes config set`, never by hand-editing (Hermes invariant).
  - `~/.hermes/SOUL.md` is the identity prompt; the companion skill content is ingested there for the companion profile.
  - Hermes has an in-process `delegate_task` tool (children cannot ask the user, cannot clarify). Usable by the companion for read-only digest/inspection workers.
- Design decisions made here (the spec left them open):
  - HTTP: stdlib `net/http` `ServeMux` with method+path patterns (Go >= 1.22). No web framework.
  - IDs: ULIDs via `github.com/oklog/ulid/v2`.
  - OpenAPI: hand-written `api/openapi.yaml` (source of truth for the contract), embedded and served at `GET /openapi.yaml`; a test fails if a registered route is missing from the spec and vice versa. Spec validated with `github.com/getkin/kin-openapi/openapi3`.
  - Agents talk to the API through `scripts/capi` (bash + curl + jq) to avoid LLM-written curl mistakes. Not a Go CLI (YAGNI).
  - Bind `127.0.0.1:7777` by default (`COMPANIOND_ADDR` overrides; refuse non-loopback unless `COMPANIOND_ALLOW_NON_LOOPBACK=1`). No auth.
  - Times are RFC3339 UTC strings in JSON, stored as TEXT in SQLite.
  - JSON-valued columns (`handle`, `scope`, `acceptance_checks`, `evidence`, `payload`, `outcome`) stored as TEXT holding JSON.

## Architecture / proposed approach

`companiond` is a layered Go service: `internal/domain` (pure rules: state machines, DAG check, fingerprinting, no I/O) -> `internal/store` (SQLite repositories + migrations, transactions, optimistic concurrency) -> `internal/api` (handlers, RFC 7807 errors, idempotency keys, cursor pagination, `If-Match`) with `internal/events` (append-only event log + in-process fan-out used by long-poll, SSE, and the `internal/notify` subscription dispatcher for webhook/command hooks). The skills live in `skills/` and are pure markdown + `scripts/capi`; the Hermes harness doc teaches tmux-based detached spawning, liveness, steering and inspection, and wires a command-hook subscription that types a wake-up signal into the companion's tmux pane.

## Target layout

```
go.mod
Makefile
README.md
api/openapi.yaml
api/embed.go                       # //go:embed openapi.yaml
cmd/companiond/main.go
internal/config/config.go
internal/ids/ids.go
internal/domain/{errors.go,plan.go,step.go,task.go,interruption.go,finding.go,agent.go}
internal/store/{db.go,migrations/001_init.sql,workstreams.go,agents.go,plans.go,steps.go,tasks.go,interruptions.go,findings.go,events.go,subscriptions.go,idempotency.go}
internal/api/{server.go,problem.go,middleware.go,page.go,workstreams.go,agents.go,plans.go,steps.go,tasks.go,interruptions.go,findings.go,events.go,stream.go,subscriptions.go,spec_test.go}
internal/notify/dispatcher.go
internal/testutil/testutil.go      # in-memory server + client helpers
scripts/capi
scripts/hermes/{spawn.sh,alive.sh,state.sh,say.sh,peek.sh,wake.sh,install.sh}
skills/companion/SKILL.md
skills/companion/harnesses/hermes.md
skills/create-plan/SKILL.md
skills/execute-plan/SKILL.md
skills/execute-task/SKILL.md
e2e/e2e_test.go                    # build tag e2e, API-level scenarios 1-6
```

Conventions for ALL code tasks:
- Tests live next to code (`*_test.go`), table-driven, stdlib `testing` only plus `net/http/httptest`. No testify (YAGNI).
- Run `go vet ./... && go test ./...` before each commit. Expected: `ok` for each package, no output from vet.
- Error type: `domain.Error{Kind, Msg}` where Kind in `NotFound, Conflict, Invalid, Unprocessable, PreconditionFailed, PreconditionRequired`; `api/problem.go` maps Kind -> status `404, 409, 400, 422, 412, 428`.

---

## Phase 0 - Bootstrap

### Task 0.1 - Module, Makefile, gitignore

Files: `go.mod`, `Makefile`, `.gitignore`.

```bash
cd "$COMPANION_HOME"
go mod init github.com/arnaubennassar/compainion
go get modernc.org/sqlite@latest github.com/oklog/ulid/v2@latest github.com/getkin/kin-openapi@latest gopkg.in/yaml.v3@latest
```

`Makefile`:
```make
.PHONY: build test vet run e2e
build: ; go build -o bin/companiond ./cmd/companiond
vet:   ; go vet ./...
test:  ; go test ./...
e2e:   ; go test -tags e2e ./e2e/...
run:   ; go run ./cmd/companiond
```
`.gitignore`: `bin/`, `*.db`, `*.db-wal`, `*.db-shm`, `.hermes/plans/` is NOT ignored (plans are committed).

Verify: `go mod tidy && go build ./... ` -> exits 0 (nothing to build yet is fine). Commit: `chore: bootstrap go module`.

### Task 0.2 - IDs and config

TDD. Test `internal/ids/ids_test.go`: `New()` returns 26-char string; two calls in sequence sort ascending (`a < b`) even in the same millisecond (use a monotonic entropy source behind a mutex).

`internal/ids/ids.go`:
```go
package ids

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

func New() string {
	mu.Lock()
	defer mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}
```
`internal/config/config.go`: `Config{Addr, DBPath string}`; `Load()` reads `COMPANIOND_ADDR` (default `127.0.0.1:7777`), `COMPANIOND_DB` (default `$XDG_DATA_HOME/companion/companion.db`, falling back to `~/.local/share/companion/companion.db`). `Validate()` returns error if host of Addr is not loopback (`127.0.0.1`, `::1`, `localhost`) unless `COMPANIOND_ALLOW_NON_LOOPBACK=1`. Test `config_test.go` covers: default loopback ok, `0.0.0.0:7777` rejected, allowed with the env var.

Verify: `go test ./internal/ids ./internal/config` -> `ok`. Commit: `feat: ids and config`.

---

## Phase 1 - Domain rules (pure, no I/O)

### Task 1.1 - Errors

`internal/domain/errors.go`:
```go
package domain

import "fmt"

type Kind int

const (
	NotFound Kind = iota + 1
	Conflict
	Invalid
	Unprocessable
	PreconditionFailed
	PreconditionRequired
)

type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func Errf(k Kind, f string, a ...any) *Error { return &Error{Kind: k, Msg: fmt.Sprintf(f, a...)} }
```
No test needed (trivial). Commit with 1.2.

### Task 1.2 - Generic state machine + step/plan/task/agent transitions

Test first, `internal/domain/step_test.go`. Required table (from -> allowed to):

Step:
- `pending` -> `ready`? NO: `ready` is derived, never stored (see 1.3). Stored statuses: `pending, in_progress, done, failed, blocked, interrupted, cancelled`; API presents `ready` computed.
- `pending` -> `in_progress, cancelled`
- `in_progress` -> `done, failed, blocked, interrupted`
- `blocked` -> `pending, cancelled` (orchestrator resolves and re-queues; `attempt` is bumped only on claim)
- `failed` -> `pending, cancelled` (retry)
- `interrupted` -> `pending, cancelled`
- `done`, `cancelled` terminal.

Plan: `draft -> approved, cancelled`; `approved -> running, cancelled, draft` (draft only via goal change, see 3.x); `running -> blocked, done, failed, cancelled`; `blocked -> running, failed, cancelled`; `done/failed/cancelled` terminal.

Task: same as Step minus nothing (`pending -> in_progress -> ...`).

Agent status: any -> any except nothing leaves `finished`/`failed` (terminal), `lost` -> `running|failed|finished` allowed (agent can come back).

Code (`internal/domain/transitions.go`):
```go
package domain

type Transitions map[string][]string

func (t Transitions) Check(from, to string) error {
	for _, a := range t[from] {
		if a == to {
			return nil
		}
	}
	return Errf(Conflict, "invalid transition %s -> %s", from, to)
}

var StepTransitions = Transitions{
	"pending":     {"in_progress", "cancelled"},
	"in_progress": {"done", "failed", "blocked", "interrupted"},
	"blocked":     {"pending", "cancelled"},
	"failed":      {"pending", "cancelled"},
	"interrupted": {"pending", "cancelled"},
}
var TaskTransitions = StepTransitions
var PlanTransitions = Transitions{
	"draft":    {"approved", "cancelled"},
	"approved": {"running", "cancelled", "draft"},
	"running":  {"blocked", "done", "failed", "cancelled"},
	"blocked":  {"running", "failed", "cancelled"},
}
```
Tests: every allowed pair passes; at least `done -> pending`, `pending -> done`, `cancelled -> pending`, `draft -> running` return `*Error` with `Kind == Conflict`. Verify: `go test ./internal/domain -run Transition` fails (undefined) -> implement -> passes. Commit: `feat(domain): state machines`.

### Task 1.3 - DAG validation and readiness

Test `internal/domain/dag_test.go`:
- adding edge A->B (A depends on B) where B already depends (transitively) on A -> `Conflict` ("cycle").
- self dependency -> `Conflict`.
- `Ready(steps, deps)` returns ids of steps with stored status `pending` whose deps are all `done`. A step with zero deps and `pending` is ready. `goal` step is included normally.

Code (`internal/domain/dag.go`):
```go
package domain

// deps: stepID -> ids it depends on.
func WouldCycle(deps map[string][]string, stepID, dependsOn string) bool {
	if stepID == dependsOn {
		return true
	}
	seen := map[string]bool{}
	var walk func(n string) bool
	walk = func(n string) bool { // can we reach stepID from n following depends-on edges?
		if n == stepID {
			return true
		}
		if seen[n] {
			return false
		}
		seen[n] = true
		for _, d := range deps[n] {
			if walk(d) {
				return true
			}
		}
		return false
	}
	return walk(dependsOn)
}

func Ready(status map[string]string, deps map[string][]string) []string {
	var out []string
	for id, st := range status {
		if st != "pending" {
			continue
		}
		ok := true
		for _, d := range deps[id] {
			if status[d] != "done" {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, id)
		}
	}
	sortStrings(out) // implement with sort.Strings (ULIDs => creation order)
	return out
}
```
Verify `go test ./internal/domain -run 'Cycle|Ready'`. Commit: `feat(domain): dag rules`.

### Task 1.4 - Scope narrowing

Test: `Narrower(planScope, stepScope)` true iff every path in step.read/write is covered by (prefix-equal or under) some plan.read/write entry respectively (write entries also satisfy read), and no step path falls under any plan.forbidden entry; step.forbidden must be a superset-or-equal (every plan.forbidden is present in step.forbidden OR step simply does not widen — implement: step.forbidden is unioned with plan.forbidden by the store, so only check read/write). Empty step scope is valid (inherits). Prefix match on path segments (`src/a` covers `src/a/b.go`, not `src/ab`).

Code in `internal/domain/scope.go`: `type Scope struct{ Read, Write, Forbidden []string }`; `func covered(p string, allowed []string) bool` using `p == a || strings.HasPrefix(p, strings.TrimSuffix(a,"/")+"/")`; `func Narrower(plan, step Scope) error` returns `Errf(Unprocessable, "step scope %q outside plan scope", p)`. Commit: `feat(domain): scope narrowing`.

### Task 1.5 - Finding fingerprint

Test `finding_test.go`: `Fingerprint("bug","internal/x.go:12","  Too MANY logs!! ")` equals `Fingerprint("bug","internal/x.go:99","too many logs")` ONLY if line numbers are stripped (yes: strip `:\d+` suffix from location). Lowercase, collapse non-alnum runs to a single space, trim. Format `category|location|title` then `sha1` hex first 16 chars. Different category -> different fingerprint.

Code: `internal/domain/finding.go`. Commit: `feat(domain): finding fingerprint`.

### Task 1.6 - Interruption selection and suggestion rule

Test `interruption_test.go`:
- `ValidateQuestions(qs)`: each question has >= 1 suggestion else `Unprocessable`; at most one `recommended`; positions unique (auto-assigned 0..n-1 if omitted); `answer_type` in enum.
- `Less(a,b Interruption) bool`: priority order `urgent<high<normal<low`; ties: `blocking` first; then older `created_at` (ULID compare).

Commit: `feat(domain): interruption rules`.

---

## Phase 2 - Storage

### Task 2.1 - DB open, migrations, schema

`internal/store/db.go`: `Open(path string) (*DB, error)` opens `modernc.org/sqlite` with DSN `file:<path>?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`; `:memory:` -> use `file:mem<ulid>?mode=memory&cache=shared`. Runs embedded migrations (`//go:embed migrations/*.sql`) tracked in table `schema_migrations(version)`. `db.SetMaxOpenConns(1)` for writes simplicity (SQLite) — document the choice in a comment.

`migrations/001_init.sql` (complete schema; all tables have `id TEXT PRIMARY KEY, created_at TEXT NOT NULL, updated_at TEXT NOT NULL`):

- `workstreams(title, status CHECK in (active,paused,done,archived), priority INT)`
- `agents(role CHECK, parent_id REFERENCES agents, harness, handle TEXT, status CHECK, workstream_id REFERENCES workstreams, last_heartbeat_at)`
- `plans(workstream_id, title, summary, goals, acceptance_criteria, acceptance_checks TEXT, scope TEXT, status CHECK, creator_agent_id, orchestrator_agent_id NULL, goal_step_id NULL, approved_by NULL, approved_at NULL, version INT NOT NULL DEFAULT 1)`
- `steps(plan_id, kind CHECK in (task,checkpoint,goal), title, description, acceptance_criteria, scope TEXT, status CHECK (stored statuses only), assignee_agent_id NULL, outcome TEXT NULL, attempt INT DEFAULT 0, added_by CHECK, suggested_executor TEXT DEFAULT '', version INT DEFAULT 1)`  (`suggested_executor` and checkpoint-flag: the plan spec lists a `checkpoint` flag and a suggested executor; `kind=checkpoint` IS the flag. Keep `suggested_executor` as a plain extra column.)
- `step_deps(step_id, depends_on_id, PRIMARY KEY(step_id, depends_on_id))`
- `tasks(workstream_id, requested_by, title, description, acceptance_criteria, scope, status, assignee_agent_id, outcome, attempt, added_by, version)`
- `interruptions(workstream_id, raised_by_agent_id, plan_id NULL, step_id NULL, task_id NULL, topic, kind CHECK, priority CHECK, digest, blocking INT, status CHECK, answered_at NULL)`
- `questions(interruption_id, position, text, answer_type CHECK, grp NULL, status CHECK)` (`group` is reserved SQL -> column `grp`, JSON name stays `group`)
- `suggestions(question_id, label, rationale, recommended INT)` + partial unique index `CREATE UNIQUE INDEX one_recommended ON suggestions(question_id) WHERE recommended=1;`
- `answers(question_id, author CHECK, mode CHECK, suggestion_id NULL, text NULL, final INT)`
- `findings(reported_by_agent_id, plan_id NULL, step_id NULL, category CHECK, severity CHECK, title, details, fingerprint, occurrences INT DEFAULT 1, evidence TEXT, status CHECK (new,surfaced,resolved), resolution NULL CHECK (issue_opened,step_added,ignored), resolution_ref NULL, interruption_id NULL)` + partial unique index `ON findings(fingerprint) WHERE status != 'resolved' OR resolution NOT IN ('issue_opened','ignored')`. NOTE: SQLite partial indexes allow this expression; the "resolved as step_added" finding can be re-reported as a new finding.
- `events(agent_id, plan_id, step_id, task_id, type CHECK, payload TEXT, at TEXT)`; `id` ULID gives the cursor order.
- `subscriptions(method CHECK in (webhook,command), target, types TEXT, filter TEXT, secret NULL, active INT)` + `UNIQUE(method, target)` (idempotent per target).
- `idempotency_keys(key, method, path, request_hash, status_code, response_body, PRIMARY KEY(key, method, path))`.

Test `db_test.go`: `Open(":memory:")` succeeds; second `Open` of same file path re-runs with no error (idempotent migrations); inserting a suggestion pair with `recommended=1` twice for one question fails with a constraint error. Verify `go test ./internal/store`. Commit: `feat(store): schema and migrations`.

### Task 2.2 - Repositories (one commit per file, each with a test file)

Each repo is a set of methods on `*DB` taking `ctx` and returning domain structs (define the structs with JSON tags matching the OpenAPI field names, in `internal/store/models.go`, to avoid a duplicate DTO layer — DRY). All writes that bump `version` use `UPDATE ... SET version=version+1 WHERE id=? AND version=?`; zero rows affected -> `PreconditionFailed` (412).

Order and the specific behaviour each test must pin:

1. `workstreams.go`: Create/Get/List/Update/Delete. Delete only when no plans/tasks (else Conflict).
2. `agents.go`: Register (idempotent on supplied `id` if given), Heartbeat (sets `last_heartbeat_at`), UpdateStatus (checks agent transitions), List(filter role/status/parent_id/workstream_id). Backend NEVER changes status by itself — test asserts a stale heartbeat does not alter status.
3. `plans.go`: Create (status `draft`, `version=1`, auto-creates the `goal` step and sets `goal_step_id` in one tx; the goal depends on nothing initially — dependencies are added as steps are created), Get, List, Update (needs expected version; if plan is `approved`/`running`/`done` and the patch changes `goals`, `acceptance_criteria`, `acceptance_checks`, `scope`, `title` or `summary`?? -> ONLY `goals`, `acceptance_criteria`, `acceptance_checks` count as "goal changes": require `approval_ref` (id of an `answered` interruption for the same plan) else `PreconditionRequired` 428; on success status -> `draft`? NO — keep status, reset `approved_by/approved_at` only if no approval_ref... Decision (simple and testable): a goal change on an approved+ plan REQUIRES an `approval_ref` pointing to an answered interruption with `plan_id` = this plan; with it the change applies and approved_* are set from that interruption; without it -> 428). Approve (`draft -> approved`, sets approved_by/at, requires `approved_by` body). Assign (sets `orchestrator_agent_id`; only when `approved`; agent must have role `orchestrator`, else 422). Graph (steps + edges + computed `ready`).
4. `steps.go`: Create (plan must be `draft` or `running`/`approved`; kind `goal` rejected on create — only the plan creates it; `added_by` required; scope checked with `Narrower` vs plan scope; deps validated same-plan + `WouldCycle`), Update (only while stored status `pending`; else Conflict; goal step needs `approval_ref` else 428; version via `If-Match`), AddDep/RemoveDep (same editing rule; the goal step can still gain dependencies ONLY via the store when a step is created by planner/orchestrator — a new step automatically becomes a dependency of goal if no other step depends on it, i.e. "leaf" steps feed the goal; this keeps "DAG ends in goal" true. Test: after creating A and B (B depends on A), goal deps == {B}), Delete (only `pending`, not goal; with approval_ref for goal -> still refused, goal can never be deleted: 409), Next (ready steps), Claim (`pending` & ready -> `in_progress`, set assignee, `attempt+1`; second claim by anyone -> 409; claim of a non-ready step -> 409), Finish (body `{status: done|failed|blocked|interrupted, outcome}`; outcome required and with `result`; `done` requires `outcome.evidence` non-empty else 422; completing the `goal` step sets plan `done` — but only if all other steps are `done`/`cancelled`, else 409).
5. `tasks.go`: same behaviours as steps minus plan/deps (reuse the shared claim/finish helper — DRY: `internal/store/work.go` with a small `workTable` struct{name string} parameterised helper used by both steps and tasks).
6. `interruptions.go`: Create (validates questions with `ValidateQuestions`, one tx inserting interruption+questions+suggestions; status `open`), Get (expanded with questions, suggestions, answers), List (filters status/workstream/topic), Next (single tx: select open by ordering `priority rank, blocking DESC, id ASC`, joined with workstream priority as final tiebreak before age? spec says ties: blocking first, then oldest -> implement exactly that, workstream priority is used ONLY inside a priority class before blocking? NO — keep to spec; do not use workstream priority until the user asks), marks `presented`, returns it. Also returns `batch`: other `open` interruptions with the same `topic` (ids only) so the companion can batch. Answer (`POST /interruptions/{id}/answers` body `{question_id, author, mode, suggestion_id?, text?, final}`; validation: mode `suggestion*` needs a suggestion belonging to that question; `free|pushback|needs_details` need `text` except `needs_details` where text optional; `author=companion` only with `pushback|needs_details`... (needs_details is a USER mode: the user asks for details; the companion's reply is a `pushback` or a `free` answer by author companion — keep enum as spec'd, enforce only: user may use suggestion/suggestion_with_comment/free/needs_details; companion may use pushback/free(=clarification text)); when `final=true`: question -> `answered`; when every question is `answered|skipped` -> interruption `answered`, `answered_at` set, and an `agent`-addressed `steer`-free event `interruption_answered` is NOT in the enum — emit event type `note` with payload `{kind:"interruption_answered", interruption_id}` targeted at `raised_by_agent_id`). Dismiss (`open|presented -> dismissed`). Re-queue: `presented` interruptions older than N minutes are NOT auto-reopened (companion owns that; expose `POST /interruptions/{id}/reopen` -> `open` so a cleared companion context can re-present; add to spec).
7. `findings.go`: Upsert per spec: compute fingerprint; if matching non-final finding exists -> `occurrences+1`, append evidence (JSON array merge), return it with `created=false` (HTTP 200 vs 201); if a `resolved` finding with resolution `issue_opened|ignored` has the fingerprint -> bump its `occurrences` only, return it (200), never re-surface; List with `query` (case-insensitive LIKE on title/details/fingerprint) and `status`; Resolve (`resolution` required; `issue_opened` requires `resolution_ref`; `step_added` requires `resolution_ref` = existing step id (validate)). Mark surfaced: `POST /findings/{id}/surface {interruption_id}` -> status `surfaced` (add to spec).
8. `events.go`: Append (ULID id, `at` now), List (filters agent_id, plan_id, step_id, task_id, type[], `since` cursor = event id exclusive, `limit` default 100 max 500, order ascending). Returns `next_cursor` = last id. Emitting side effects: store methods call `events.Append` for lifecycle (`started` on claim, `finished` on finish, etc.) in the same tx.
9. `subscriptions.go`: Upsert by (method,target) (idempotent), List, Delete, SetActive.
10. `idempotency.go`: `Lookup(key,method,path)` / `Save`. Same key + different `request_hash` -> 422.

Each repo test uses `store.Open(":memory:")`, builds the minimal parent rows with helper constructors in `internal/testutil`. Run `go test ./internal/store -run <Name>` red/green per file. Commit per file: `feat(store): <name> repository`.

---

## Phase 3 - HTTP layer

### Task 3.1 - Server skeleton, problem+json, middleware

Test (`internal/api/server_test.go`) with `httptest.NewServer(api.New(db).Handler())`:
- `GET /healthz` -> 200 `{"status":"ok"}`.
- Unknown id -> 404 with `Content-Type: application/problem+json` and body having `type,title,status,detail`.
- Missing `If-Match` on PATCH of a plan -> 428; stale `If-Match: "1"` -> 412.
- `POST` twice with same `Idempotency-Key` and same body -> second response is identical and `Idempotent-Replayed: true` header; same key different body -> 422.

Implement: `problem.go` (`writeProblem(w, err)` using the Kind->status mapping; unknown errors -> 500 with generic detail and logged), `middleware.go` (logging, panic recovery -> 500 problem, idempotency wrapper that buffers response for `POST` when header present), `page.go` (`?limit` default 50 max 200, `?cursor` opaque = last id; response envelope `{items:[...], next_cursor:"..."}`), ETag = `"<version>"` on plans/steps/tasks responses.

Commit: `feat(api): server skeleton`.

### Task 3.2 - Resource handlers (one commit each, handler test = happy path + one failure per rule)

Routes (exactly these; each registered in `server.go` with Go 1.22 patterns, e.g. `mux.HandleFunc("POST /steps/{id}/claim", s.claimStep)`):

```
GET/POST        /workstreams            GET/PATCH/DELETE /workstreams/{id}
POST            /agents                 GET /agents       GET/PATCH /agents/{id}
POST            /agents/{id}/heartbeat
GET/POST        /agents/{id}/events
GET/POST        /plans                  GET/PATCH/DELETE /plans/{id}
POST            /plans/{id}/approve     POST /plans/{id}/assign     GET /plans/{id}/graph
GET/POST        /plans/{id}/steps       GET /plans/{id}/steps/next
GET/PATCH/DELETE /steps/{id}            POST /steps/{id}/claim      POST /steps/{id}/finish
POST/DELETE     /steps/{id}/deps  (body {depends_on_id}; DELETE /steps/{id}/deps/{dep_id})
GET/POST        /tasks                  GET/PATCH/DELETE /tasks/{id}
POST            /tasks/{id}/claim       POST /tasks/{id}/finish
GET/POST        /interruptions          GET/DELETE /interruptions/{id}
GET             /interruptions/next     POST /interruptions/{id}/answers
POST            /interruptions/{id}/dismiss    POST /interruptions/{id}/reopen
GET/POST        /findings               GET /findings/{id}
POST            /findings/{id}/resolve  POST /findings/{id}/surface
GET/POST        /events                 GET /stream
GET/POST        /subscriptions          GET/DELETE /subscriptions/{id}
GET             /openapi.yaml           GET /healthz
```
Handler pattern (use for all, DRY): generic helpers `decode[T any](r) (T, error)` (strict: `DisallowUnknownFields`, 1 MiB cap) and `writeJSON(w, status, v)`. Handlers contain no business rules; they parse, call the store, map errors.

Specific behaviours to test:
- `GET /interruptions/next`: 204 when none; otherwise 200 with the interruption and status `presented`; calling again does NOT return the same one (it is `presented`, not `open`).
- `POST /findings`: 201 on create, 200 on dedupe (body includes `occurrences`).
- `GET /plans/{id}/steps/next`: only `pending` steps whose deps are all `done`; response field `ready:true` set on each; steps `pending` but blocked by deps are listed by `GET .../steps` with `status:"pending"` and `ready:false`, and the API shows `status:"ready"` ONLY via the `ready` boolean (the stored `status` stays `pending`) — decision: expose `status` as stored plus derived `ready bool` and document that the spec's `ready` status is this derived flag.
- Every `POST` that creates accepts `Idempotency-Key`.
Commits: `feat(api): workstreams+agents`, `feat(api): plans+steps+tasks`, `feat(api): interruptions`, `feat(api): findings+events`.

### Task 3.3 - Long polling and SSE

Test `events_wait_test.go`: `GET /events?since=<last>&wait=2` blocks, then a concurrent `POST /events` makes it return that event in < 1s; with no event returns `200 {items:[]}` after the `wait` timeout (use `wait=1` in test and assert 1s <= elapsed < 2s). Same `?wait=` on `GET /interruptions/next` (returns 204 on timeout).

Implement `internal/events/bus.go`: `Bus` with `Publish(ev)` and `Subscribe() (<-chan Event, cancel func())`, non-blocking send into buffered chan (drop-on-full is acceptable because consumers reconcile from the log; document it). The store emits to the bus after commit.

SSE `GET /stream?types=a,b&workstream_id=...`: `text/event-stream`, each message `id: <event id>\nevent: <type>\ndata: {"type":..,"id":..}\n\n` (signal only). Honour `Last-Event-ID` by first replaying `events` with id > header. Test: connect with `http.Client`, post an event, read one `data:` line within 1s; reconnect with `Last-Event-ID` returns the missed event. Heartbeat comment `: ping` every 15s.

Commit: `feat(api): long poll and sse`.

### Task 3.4 - Subscriptions dispatcher (webhook + command hooks)

Test `internal/notify/dispatcher_test.go`:
- webhook: register `POST /subscriptions {method:"webhook", target: <httptest url>, types:["needs_input"]}`; publishing a `needs_input` event results in exactly one POST with body `{"type":"needs_input","id":"<eventid>"}` and header `X-Companion-Signature` = hex HMAC-SHA256(body, secret) when secret set; non-matching type -> no call; 500 response -> retried up to 3 times with backoff 100ms/200ms (inject clock/sleeper for the test).
- command: `{method:"command", target:"touch <tmpfile>"}` runs via `sh -c` with env `COMPANION_EVENT_TYPE`, `COMPANION_EVENT_ID`; test asserts the file exists within 1s. Commands run with a 10s timeout, never inherit request bodies; the payload NEVER enters the command line (signal only through env) to prevent injection.
- Idempotent registration: posting the same (method,target) twice returns the same subscription id.
- Also emit synthetic event types for the companion's wake-up: `interruption_created` (published when an interruption is created), `agent_waiting` (agent status set to `waiting`), `agent_lost`. Add these to the `events.type` CHECK enum? The spec enum is `started, progress, needs_input, finished, steer, error, note`; to stay inside it, emit them as `type=note` with `payload.kind` set to `interruption_created|agent_waiting|agent_lost`, and let subscriptions filter on `payload.kind` through the `filter` JSON field (`{"kind":["interruption_created"]}`). Test covers filter matching.

Commit: `feat(notify): webhook and command subscriptions`.

### Task 3.5 - OpenAPI spec + route/spec parity test

Write `api/openapi.yaml` (OpenAPI 3.0.3) describing every route above, all schemas (copy field lists from the data model in the spec document verbatim), `components/responses/Problem` (RFC 7807), parameters `IfMatch`, `IdempotencyKey`, `Cursor`, `Limit`. `api/embed.go`: `//go:embed openapi.yaml; var Spec []byte`. Serve at `GET /openapi.yaml`.

Test `internal/api/spec_test.go`:
1. `openapi3.NewLoader().LoadFromData(api.Spec)` then `doc.Validate(ctx)` -> nil.
2. Walk `server.Routes()` (expose a `[]string` of "METHOD /path/{id}" patterns recorded at registration) and assert each exists in `doc.Paths` with that method, and each documented operation has a registered route. Failure lists the diff.

Verify: `go test ./internal/api -run Spec` red (spec missing) -> write spec -> green. Commit: `docs(api): openapi spec with parity test`.

### Task 3.6 - main.go

`cmd/companiond/main.go`: load config, validate, open DB, build server, start dispatcher, `http.Server{ReadHeaderTimeout: 5s}`, graceful shutdown on SIGINT/SIGTERM (5s). Log JSON via `log/slog`.

Verify manually:
```bash
COMPANIOND_DB=$(mktemp -d)/c.db go run ./cmd/companiond &   # logs: "listening" addr=127.0.0.1:7777
curl -s localhost:7777/healthz                      # {"status":"ok"}
curl -s localhost:7777/openapi.yaml | head -3       # openapi: 3.0.3
curl -s -X POST localhost:7777/workstreams -d '{"title":"t","priority":1}' -H 'content-type: application/json' | jq .id
kill %1
COMPANIOND_ADDR=0.0.0.0:7777 go run ./cmd/companiond   # exits non-zero with "non-loopback"
```
Commit: `feat: companiond entrypoint`.

---

## Phase 4 - API-level scenario tests (acceptance for the backend)

### Task 4.1 - e2e scenarios 1-6 as Go tests

`e2e/e2e_test.go` (`//go:build e2e`), boots an in-process server on `:memory:` and drives it only via HTTP using `internal/testutil` client helpers. One test per spec flow:
1. Simple task: create workstream, agent (worker), task, claim, events started/finished, finish with evidence -> task `done`.
2. Plan then execute: create plan with 3 steps (A, B depends A, verify depends B) -> goal deps == {verify}; approval interruption answered -> `POST /plans/{id}/approve`, assign orchestrator; `steps/next` returns only A; finish A -> next returns B; finishing verify then goal -> plan `done`. Interruption with 3 questions where Q3 gets `needs_details` then companion `free` answer then user final: interruption closes only after all three are final.
3. Step fails and plan changes: finish step `failed`; edit a `done` step -> 409; add new step with deps, rewire a `pending` step's deps -> OK; PATCH goal without approval_ref -> 428, with an answered interruption id -> 200.
4. Finding dedupe: post same finding twice with different line numbers -> second returns 200, `occurrences==2`; resolve `ignored`; post again -> still `resolved`, `occurrences==3`, no new row (`GET /findings?status=new` empty).
5. Status/long-poll: `GET /events?since=...&wait=2` returns upon append.
6. Auto-steer record: agent status `waiting`; `POST /agents/{id}/events {type:"steer"}` recorded and visible via `GET /agents/{id}/events`.
Plus lost-agent: update agent status `lost` while holding an in_progress step; step status remains `in_progress` (backend never mutates).

Verify: `make e2e` -> `ok .../e2e`. Commit: `test(e2e): spec flows 1-6`.

---

## Phase 5 - Agent-facing tooling and Hermes skills

### Task 5.1 - `scripts/capi`

Bash, `set -euo pipefail`. Usage: `capi METHOD PATH [JSON|@file]`. Reads `COMPANIOND_URL` (default `http://127.0.0.1:7777`). Adds `Content-Type`, an `Idempotency-Key` (`${CAPI_KEY:-$(date +%s%N)-$RANDOM}`) on POST, and `If-Match` from `CAPI_IF_MATCH` if set. Prints body; on HTTP >= 400 prints the problem JSON to stderr and exits 22. Include `capi --help`.

```bash
#!/usr/bin/env bash
set -euo pipefail
BASE="${COMPANIOND_URL:-http://127.0.0.1:7777}"
[ "${1:-}" = "--help" ] && { echo "usage: capi METHOD PATH [JSON|@file]"; exit 0; }
m="$1"; p="$2"; body="${3:-}"
args=(-sS -X "$m" "$BASE$p" -H 'Content-Type: application/json' -o /tmp/capi.$$ -w '%{http_code}')
[ "$m" = POST ] && args+=(-H "Idempotency-Key: ${CAPI_KEY:-$(date +%s%N)-$RANDOM}")
[ -n "${CAPI_IF_MATCH:-}" ] && args+=(-H "If-Match: \"$CAPI_IF_MATCH\"")
[ -n "$body" ] && args+=(--data-binary "$body")
code=$(curl "${args[@]}")
if [ "$code" -ge 400 ]; then cat /tmp/capi.$$ >&2; rm -f /tmp/capi.$$; exit 22; fi
cat /tmp/capi.$$; rm -f /tmp/capi.$$
```
Verify against the running daemon: `scripts/capi GET /healthz` -> `{"status":"ok"}`; `scripts/capi GET /nope; echo $?` -> problem JSON on stderr, `22`. Commit: `feat: capi helper`.

### Task 5.2 - Hermes harness scripts (`scripts/hermes/*.sh`)

All take an agent session name (`cmp-<agentid-lowercase-first-10>`), all `set -euo pipefail`.

- `spawn.sh <name> <workdir> <skills-csv> <prompt-file> [--worktree]`: `tmux new-session -d -s "$name" -c "$workdir" "hermes -s '$skills' ${WT:+-w}"` then waits until the pane shows the Hermes prompt (poll `capture-pane` up to 30s for a non-empty last line, else exit 1), then loads the prompt via tmux buffer to avoid quoting: `tmux load-buffer -b "$name" "$prompt_file" && tmux paste-buffer -b "$name" -t "$name" && tmux send-keys -t "$name" Enter`. Prints `{"harness":"hermes","tmux_session":"<name>","workdir":"<dir>"}` (this JSON becomes the agent `handle`).
- `alive.sh <name>`: exit 0 if `tmux has-session -t "$name"`, else 1.
- `state.sh <name>`: prints one of `working|waiting|idle|gone` by capturing the last 15 lines. `gone` if no session. `waiting` if the tail matches approval/confirm patterns (`[y/N]`, `(y/n)`, `Approve`, `Do you want`, `Press Enter`) ; `working` if the pane output changed between two captures 2s apart; else `idle`. Patterns live in a variable at the top of the script and are documented as heuristics to be tuned against real Hermes (see risks).
- `say.sh <name> <text-or-@file>`: pastes through a tmux buffer then Enter (never `send-keys` raw text with shell metacharacters).
- `peek.sh <name> [lines=60]`: `tmux capture-pane -t "$name" -p -S -$lines`. Read only.
- `wake.sh`: invoked by the command-hook subscription; reads `COMPANION_EVENT_ID`, debounces (skip if invoked < 3s ago via a lock file in `$XDG_RUNTIME_DIR` or `/tmp`), checks companion tmux session `${COMPANION_TMUX:-companion}` state with `state.sh`; only when `idle` (not `working`) pastes `[companion-signal] reconcile: GET /interruptions/next` + Enter; if `working`/`waiting` do nothing (the companion polls at the end of each loop anyway).
- `install.sh`: (1) `hermes config set skills.external_dirs "[<repo>/skills]"` — FIRST run `hermes config set --help` / docs to confirm list syntax, and if lists are not supported print the exact manual instruction instead of editing config.yaml by hand; (2) registers wake subscription: `capi POST /subscriptions '{"method":"command","target":"<repo>/scripts/hermes/wake.sh","types":["note","needs_input"],"filter":{"kind":["interruption_created","agent_waiting","agent_lost"]}}'`; (3) prints how to start the companion: `tmux new-session -s companion 'hermes -s companion'`.

Test with a bats-free smoke script `scripts/hermes/smoke.sh` using a fake agent: `tmux new-session -d -s cmp-smoke 'cat'`. Assert: `alive.sh cmp-smoke` exit 0; `say.sh cmp-smoke "hello world"`; `peek.sh cmp-smoke | grep -q 'hello world'`; `state.sh cmp-smoke` prints `idle` or `working`; then `tmux kill-session -t cmp-smoke`; `alive.sh cmp-smoke` exit 1 and `state.sh` prints `gone`. Also run it with text containing `"; $(rm -rf x) '` and assert it is echoed literally and no `x` is touched.

Verify: `bash scripts/hermes/smoke.sh` -> `smoke: OK`. Commit: `feat(hermes): tmux harness scripts`.

### Task 5.3 - Skills (markdown). Write each with Hermes frontmatter (`name`, `description` starting "Use when ...", `version`, `metadata.hermes.tags`)

All skills: short imperative steps, always use `scripts/capi` (path resolved from `$COMPANION_HOME`, set by `install.sh`; skills tell the agent to `export COMPANION_HOME=<repo>` if unset), never local copies of plans.

1. `skills/companion/SKILL.md` — implements the main loop verbatim from the spec: 1. `capi GET /interruptions/next` (204 -> "nothing pending"); 2. present (digest first, one question at a time by `position`, same `group` together, batch same-`topic` ids from `batch`, always show suggestions with the recommended one first, offer 4 modes); handle `needs_details` by delegating a worker (never answer inline) and re-present; push back when answer looks inconsistent/incomplete; 3. POST answer then loop; steer unblocked agent via `harnesses/hermes.md` if its handle shows it is not polling; 4. context may be cleared after closing. Also includes: startup (register self as agent `role=companion`, harness `hermes`; create-or-select workstream), the dispatch rules table (task vs plan), "never do effective work" rule with the Hermes exception text, status-question procedure (spawn worker with the `execute-task` skill whose task is "digest plan X", relay result), surfacing procedure (every loop turn: `GET /findings?status=new`; at a natural break between interruptions batch them into one `kind=finding` interruption with suggestions open-issue(recommended for low/medium)/add-step/ignore; after answer call `/findings/{id}/resolve`; for "open issue" spawn a worker to create it and use its URL as `resolution_ref`; check `GET /findings?query=` for near dupes first), auto-steer rule (only continue/yes/proceed; record `POST /agents/{id}/events {type:"steer",payload:{text,reason}}`), liveness sweep (each loop turn and on `agent_lost` signal: for each agent `running|waiting` run `alive.sh`; gone -> `PATCH /agents/{id} {"status":"lost"}`; if it holds a step/task create interruption with suggestions respawn-and-retry / mark failed / cancel).
2. `skills/companion/harnesses/hermes.md` — answers the four required questions: spawn (`spawn.sh`, register agent with handle JSON first, `status=starting`), alive/idle/waiting (`alive.sh`, `state.sh`; heartbeat from API is secondary), send message (`say.sh`), read output (`peek.sh`; for inspection prefer `delegate_task` or a `hermes chat -q --oneshot -Q -s execute-task --query-file` digest worker); wake-up method = command hook (`wake.sh`) with polling fallback each loop and `GET /interruptions/next?wait=30` via a background terminal as an optional alternative; role-to-command table: worker = tmux `hermes -s execute-task -w` (use `-w` when code is edited), orchestrator = tmux `hermes -s execute-plan`, create-plan worker = tmux `hermes -s create-plan`; read-only digest worker = `delegate_task` or `--oneshot`; how to ingest the companion skill as `SOUL.md` for a dedicated profile (`hermes profile` — verify the exact command with `hermes profile --help` before documenting; cite the output) ; Hermes specifics: `delegate_task` children cannot spawn further or ask the user.
3. `skills/create-plan/SKILL.md` — process from spec; plan body template; rules (final verification step, steps small, scope inherits, `kind=checkpoint` flag); read-only constraint; posting order: create plan (draft) -> create steps with deps -> verify graph via `GET /plans/{id}/graph` -> approval interruption (suggestions approve(recommended) / approve with comments / request changes); questions batched in ONE interruption before drafting when answers change the plan shape; events `started/finished`.
4. `skills/execute-plan/SKILL.md` — loop steps 1-6 from spec; evaluate against acceptance criteria not claims; step edit rules (only pending; add steps; never goal without answered interruption `approval_ref`); spawn worker per step with prompt file containing the step payload and ids; emit events; findings are posted not fixed.
5. `skills/execute-task/SKILL.md` — worker contract from spec; event reporting commands with exact `capi` examples; finding reporting (query first via `GET /findings?query=`); outcome format `{result:"success|failed|interrupted", evidence:[...], notes}`; blocked decision -> interruption with suggestions, set `needs_input` event, agent status `waiting`; no further workers.

Each skill must include a "Quick reference" with the exact `capi` calls it needs (copy the JSON bodies from the OpenAPI examples). Add `examples` to the OpenAPI for: create interruption with 2 questions, finish step, create finding.

Verification (docs can't be unit tested, so make them checkable):
- `scripts/check-skills.sh`: for each `skills/*/SKILL.md` assert frontmatter has `name:` and `description:` and that every `capi <METHOD> <PATH>` occurrence in all skill files matches a route in `api/openapi.yaml` (normalise `{...}`/`$ID` segments). Expected output `skills: OK (<n> capi calls checked)`.
- `hermes -s companion` is loadable: after `install.sh`, `hermes chat -q "list the steps of the companion main loop" --oneshot -Q -s companion` must mention `/interruptions/next` (manual check, record the output in the PR).
Commit per skill, then `test: check-skills` with the script.

---

## Phase 6 - Live acceptance on Hermes (manual, record outputs)

### Task 6.1 - Smoke the real thing

1. `make build && COMPANIOND_DB=/tmp/cmp.db bin/companiond &`; `bash scripts/hermes/install.sh`.
2. `tmux new-session -d -s companion 'hermes -s companion'`; attach and ask: "bump nothing, just create a file /tmp/cmp-hello.txt containing hi". Expected: a task row (`capi GET /tasks` shows 1 task), a worker agent row with handle tmux session, worker finishes, `GET /tasks/{id}` status `done` with `outcome.evidence`, companion replies with a two-line digest and the file exists.
3. Ask for a plan ("add a README section describing the API") -> a create-plan worker raises an interruption; companion presents one question at a time with suggestions; approve; an orchestrator is spawned and the plan reaches `done`.
4. Kill a worker's tmux session mid-run -> within one loop companion marks agent `lost` and raises the respawn/fail/cancel interruption.
5. Write findings of what did not work against the heuristics in `state.sh` as edits to `harnesses/hermes.md`.

Record outcomes in `docs/live-acceptance.md`. Commit: `docs: live acceptance on hermes`.

### Task 6.2 - README

`README.md`: what it is (3 lines), quick start (`make build`, run, `scripts/hermes/install.sh`, start companion), layout, link to `api/openapi.yaml`, security note (localhost, no auth), the harness doc pointer for adding other harnesses. Commit: `docs: readme`.

---

## Definition of done (acceptance criteria for the whole plan)

- `go vet ./... && go test ./... && go test -tags e2e ./e2e/...` all pass.
- Spec/route parity test passes; `GET /openapi.yaml` served.
- `bash scripts/hermes/smoke.sh` -> `smoke: OK`; `bash scripts/check-skills.sh` -> `skills: OK ...`.
- Task 6.1 steps 2-4 performed with real Hermes and recorded.
- Backend rules enforced by API and covered by tests: acyclic DAG, ready derivation, pending-only edits, goal protected (428 without approval ref), transition 409s, suggestion required 422, finding dedupe, idempotency, `If-Match` 412/428, loopback-only bind.

## Out of scope (do not build)

Dashboard UI, other harnesses (Claude Code, Codex, OpenClaw), Postgres, auth, markdown mirrors of plans, a Go CLI client, workstream-priority-aware interruption ordering, automatic agent-lost detection in the backend.

## Risks, tradeoffs, open questions

- Detecting `waiting` in Hermes via tmux screen scraping is heuristic; may misfire. Mitigations: the worker is told to self-report `needs_input` events and set status `waiting`; scraping is only the fallback. Tune patterns in Task 6.1.
- Waking the companion by typing into its pane can corrupt a half-typed user message. `wake.sh` only fires when the pane is `idle` and sends a clearly tagged line; the main loop also polls, so a skipped wake is harmless. Open: whether to instead use long polling via a background terminal with `notify`.
- Spec says `findings` unique "among non-ignored-or-resolved"; implemented as: dedupe against open ones and against resolved `issue_opened|ignored`; `step_added` resolutions may be re-reported as new. Confirm with the author.
- Goal-change rule simplified: goal edits on approved+ plans require an `approval_ref` to an answered interruption on that plan; there is no automatic reset to `draft`. Confirm.
- `ready` is a derived flag rather than a stored status (spec lists it as a status). Confirm acceptable to API consumers.
- Added endpoints not in the draft spec: `/interruptions/{id}/reopen`, `/findings/{id}/surface`, `/steps/{id}/deps`; added `suggested_executor` column. Update the spec doc if kept.
- Hermes profile/SOUL ingestion command not verified; Task 5.3 requires checking `hermes profile --help` and `hermes config set --help` before documenting.
- `modernc.org/sqlite` with a single connection is simple but serializes writes; fine for localhost scale, revisit with Postgres.
- The repo directory is spelled `compainion` while the product is CompAInion; module path may need adjusting to the real remote.

---

# Amendment 1 (supersedes Task 5.2, the harness part of 5.3, and Phase 6): Hermes gateway + Telegram, no tmux

Decision (user): the companion is operated through Telegram via the Hermes gateway. tmux scripts (`scripts/hermes/*`, commit 5899b70) are dropped in the final harness; delete them in Task A5. Phases 0-4 (Go backend) are unchanged. Backend stays harness-agnostic: it only needs webhook subscriptions (already in Task 3.4).

Facts checked locally: gateway is running but `Platforms: none configured`; webhook platform not enabled; no TELEGRAM_/API_SERVER_/WEBHOOK_ vars in `~/.hermes/.env`; `hermes profile create` and `hermes gateway setup/status` exist. Secrets (Telegram bot token, API key, HMAC secret) are added by the USER only; agents must never write or print them.

Design:
- Companion = a dedicated Hermes profile `companion` (own SOUL.md = companion skill, own gateway, Telegram platform). The user chats with it on Telegram. Its state lives in the API, so context resets are fine.
- Wake-up: companiond webhook subscription -> Hermes webhook route `companion-wake` (`deliver: telegram`, `mirror_to_session: true`, prompt = fixed text "reconcile: present the next interruption"). Notification is a signal only; the companion calls `GET /interruptions/next` itself. When the user replies in Telegram, the companion finds what is being answered via `GET /interruptions?status=presented` (API is source of truth, not the chat history).
- Workers/orchestrators: headless runs through the gateway API server `POST /v1/runs` (skill body + step payload in `instructions`/`input`, `session_id` = agent id), status via `GET /v1/runs/{id}`, output from the run. Handle JSON = `{harness:"hermes", run_id, session_id}`. "Waiting" = run finished while the agent has an open blocking interruption; steering/answering = a new run on the same `session_id`. `lost` = run failed/unknown after a gateway restart.
- Inspection/digest workers: same Runs API with the read-only `execute-task` skill (or `delegate_task` inside the companion).

Tasks (A = amendment):
- A0 (USER, manual, prerequisites): `hermes profile create companion`; `hermes -p companion gateway setup` -> Telegram bot token + allowed user id, enable webhook platform and API server (API_SERVER_KEY, WEBHOOK_SECRET into that profile's `.env`); start `hermes -p companion gateway run`. Verify: `hermes -p companion status` shows Telegram connected; `curl localhost:<webhook-port>/health`.
- A1 SPIKE (read the real behaviour before writing docs; record outputs in `docs/hermes-gateway-spike.md`): (a) `POST /v1/runs` with instructions containing a skill and an input that runs `curl localhost:7777/healthz`; poll status; (b) second run with same `session_id` continues context (ask "what was the previous result?"); if not, document the `previous_response_id`/`conversation_history` alternative; (c) can a run set cwd / load a named skill? (d) which signature header/format does the webhook route expect and does companiond's HMAC header (`X-Companion-Signature`, hex HMAC-SHA256) need changing to match; (e) `mirror_to_session` really lands the message in the Telegram chat session.
- A2 companiond webhook compat: adapt `internal/notify` signing/headers to whatever A1(d) found (test first).
- A3 `scripts/hermes/runs.sh` (curl+jq): `spawn <name> <skills> <prompt-file>`, `status <run_id>`, `resume <session_id> <text|@file>`, `output <run_id>`; reads `HERMES_API_URL`, `HERMES_API_KEY` from env (never logged). Tested against a Python stub of the Runs API in `$TMPDIR`, then live in A1 conditions.
- A4 `scripts/hermes/install.sh` rewrite: prints (does not do) the profile/gateway steps from A0; registers the companiond subscription `{method:"webhook", target:"http://127.0.0.1:<port>/webhooks/companion-wake", types:["note"], filter:{kind:["interruption_created","agent_waiting","agent_lost"]}, secret_env:"WEBHOOK_SECRET"}` and the Hermes route via `hermes webhook subscribe companion-wake --deliver telegram --mirror-to-session ...`.
- A5 delete tmux scripts and `smoke.sh`; rewrite `skills/companion/harnesses/hermes.md` for Runs API + Telegram (answer the four required questions: spawn, alive/idle/waiting, send message, read output) and Telegram presentation rules (short messages, numbered suggestions, one question at a time, accept "1", "1 + comment", free text, "details").
- A6 Live acceptance on Telegram (replaces Task 6.1): same scenarios (simple task, plan with approval, lost worker) driven from the phone; record in `docs/live-acceptance.md`.

Phase 5.3 skills (companion/create-plan/execute-plan/execute-task) are still written as planned, except every mention of tmux/`state.sh`/`say.sh`/`peek.sh` is replaced by `runs.sh`.
