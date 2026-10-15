# Feature Y across A and B

**Summary:** Add feature Y to repo-a and expose it through the repo-b API.

**Goals:** Ship endpoint `POST /y` in repo-b backed by `repo-a/src/y`, covered by tests, with a PR open on each.

**Acceptance criteria:** `go test ./...` green in both repos; `curl -X POST /y` returns 200 on staging.

## Scope / non-goals

- **writes:** `repo-a/src/y/`, `repo-b/api/`
- **branch:** `companion/T-015`
- Off limits: everything else.

## Steps

### s1 — Scaffold module
- **agent:** worker-low
- **writes:** `repo-a/src/y/`
- Create the package skeleton and an exported `Y()` entry point with a stub test.

### s2 — Add API endpoint
- **depends on:** s1
- **agent:** worker-medium
- **writes:** `repo-b/api/`
- Wire `POST /y` to `Y()`, add handler tests and request validation.

### s3 — CLI wiring
- **depends on:** s1
- **agent:** worker-low
- **writes:** `repo-a/src/cli/`
- Expose feature Y through the existing CLI subcommand.

### s4 — End-to-end verification
- **depends on:** s2, s3
- **agent:** worker-high
- **checkpoint:** true
- Run the plan's acceptance criteria end to end and record evidence in the report.
