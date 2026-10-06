package api_test

import (
	"testing"

	"github.com/arnaubennassar/compainion/internal/testutil"
)

func TestAgentRegisterListGetPatchHeartbeat(t *testing.T) {
	srv := testutil.NewServer(t)

	// The agent's workstream_id has an FK: create a real workstream first.
	res0 := testutil.Do(t, srv, "POST", "/workstreams", map[string]any{"title": "ws"}, nil)
	var ws struct {
		ID string `json:"id"`
	}
	res0.Unmarshal(t, &ws)

	// POST /agents happy path with handle object + id.
	res := testutil.Do(t, srv, "POST", "/agents", map[string]any{
		"id": "agent-1", "role": "worker", "harness": "hermes",
		"handle": map[string]any{"tmux": "sess-1"}, "workstream_id": ws.ID,
	}, nil)
	if res.Status != 201 {
		t.Fatalf("POST /agents status = %d, want 201: %s", res.Status, res.Body)
	}
	var a struct {
		ID, Role, Status string
		LastHeartbeatAt  *string `json:"last_heartbeat_at"`
	}
	res.Unmarshal(t, &a)
	if a.ID != "agent-1" || a.Role != "worker" || a.Status != "starting" {
		t.Fatalf("registered agent = %+v", a)
	}
	if a.LastHeartbeatAt != nil {
		t.Fatalf("new agent should have no heartbeat: %+v", a)
	}

	// Idempotent re-register: same id returns existing row (201/200 both
	// acceptable; the spec only documents 201).
	res = testutil.Do(t, srv, "POST", "/agents", map[string]any{
		"id": "agent-1", "role": "worker",
	}, nil)
	if res.Status >= 300 {
		t.Fatalf("re-register status = %d, body %s", res.Status, res.Body)
	}
	res.Unmarshal(t, &a)
	if a.ID != "agent-1" {
		t.Fatalf("re-register returned %+v", a)
	}

	// POST missing role -> 400.
	if res := testutil.Do(t, srv, "POST", "/agents", map[string]any{"harness": "x"}, nil); res.Status != 400 {
		t.Fatalf("POST /agents without role status = %d, want 400", res.Status)
	}

	// GET /agents with filters.
	testutil.Do(t, srv, "POST", "/agents", map[string]any{"id": "agent-2", "role": "companion"}, nil)
	res = testutil.Do(t, srv, "GET", "/agents?role=worker", nil, nil)
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != "agent-1" {
		t.Fatalf("GET /agents?role=worker = %+v", page.Items)
	}

	// GET /agents/{id}; 404 on unknown.
	res = testutil.Do(t, srv, "GET", "/agents/agent-1", nil, nil)
	if res.Status != 200 {
		t.Fatalf("GET agent status = %d", res.Status)
	}
	if res := testutil.Do(t, srv, "GET", "/agents/nope", nil, nil); res.Status != 404 {
		t.Fatalf("GET missing agent status = %d, want 404", res.Status)
	}

	// PATCH status through the state machine: starting -> running ok.
	res = testutil.Do(t, srv, "PATCH", "/agents/agent-1", map[string]any{"status": "running"}, nil)
	if res.Status != 200 {
		t.Fatalf("PATCH agent status = %d, want 200: %s", res.Status, res.Body)
	}
	res.Unmarshal(t, &a)
	if a.Status != "running" {
		t.Fatalf("patched agent = %+v", a)
	}

	// PATCH invalid transition running -> starting is allowed; use lost->idle
	// (invalid) instead: set lost first, then try idle.
	testutil.Do(t, srv, "PATCH", "/agents/agent-1", map[string]any{"status": "lost"}, nil)
	if res := testutil.Do(t, srv, "PATCH", "/agents/agent-1", map[string]any{"status": "idle"}, nil); res.Status != 409 {
		t.Fatalf("PATCH invalid transition status = %d, want 409: %s", res.Status, res.Body)
	}

	// PATCH unknown agent -> 404.
	if res := testutil.Do(t, srv, "PATCH", "/agents/nope", map[string]any{"status": "running"}, nil); res.Status != 404 {
		t.Fatalf("PATCH missing agent status = %d, want 404", res.Status)
	}

	// Heartbeat: 204 and sets last_heartbeat_at.
	if res := testutil.Do(t, srv, "POST", "/agents/agent-1/heartbeat", nil, nil); res.Status != 204 {
		t.Fatalf("heartbeat status = %d, want 204", res.Status)
	}
	res = testutil.Do(t, srv, "GET", "/agents/agent-1", nil, nil)
	res.Unmarshal(t, &a)
	if a.LastHeartbeatAt == nil {
		t.Logf("DEBUGBODY %s", res.Body)
		t.Fatalf("heartbeat did not set last_heartbeat_at: %+v", a)
	}
	if res := testutil.Do(t, srv, "POST", "/agents/nope/heartbeat", nil, nil); res.Status != 404 {
		t.Fatalf("heartbeat missing agent status = %d, want 404", res.Status)
	}
}
