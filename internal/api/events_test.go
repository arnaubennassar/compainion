package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arnaubennassar/compainion/internal/testutil"
)

type evDTO struct {
	ID      string          `json:"id"`
	AgentID string          `json:"agent_id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	At      string          `json:"at"`
}

func postEvent(t *testing.T, srv *httptest.Server, path string, body map[string]any) evDTO {
	t.Helper()
	res := testutil.Do(t, srv, "POST", path, body, nil)
	if res.Status != 201 {
		t.Fatalf("POST %s status = %d, want 201: %s", path, res.Status, res.Body)
	}
	var ev evDTO
	res.Unmarshal(t, &ev)
	return ev
}

func TestEventsPostList(t *testing.T) {
	srv := testutil.NewServer(t)

	ev1 := postEvent(t, srv, "/events", map[string]any{
		"type": "progress", "payload": map[string]any{"step": 1},
	})
	if ev1.ID == "" || ev1.At == "" || string(ev1.Payload) == "" {
		t.Fatalf("created event = %+v", ev1)
	}

	// POST missing type -> 400.
	if res := testutil.Do(t, srv, "POST", "/events", map[string]any{"payload": map[string]any{}}, nil); res.Status != 400 {
		t.Fatalf("POST /events without type status = %d, want 400", res.Status)
	}

	// POST non-object payload -> 400.
	if res := testutil.Do(t, srv, "POST", "/events", map[string]any{"type": "note", "payload": []any{1}}, nil); res.Status != 400 {
		t.Fatalf("POST /events array payload status = %d, want 400", res.Status)
	}

	ev2 := postEvent(t, srv, "/events", map[string]any{"type": "note"})

	// GET list ascending with next_cursor = last id.
	res := testutil.Do(t, srv, "GET", "/events", nil, nil)
	var page struct {
		Items      []evDTO `json:"items"`
		NextCursor string  `json:"next_cursor"`
	}
	if res.Status != 200 {
		t.Fatalf("GET /events status = %d", res.Status)
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 2 || page.Items[0].ID != ev1.ID || page.Items[1].ID != ev2.ID {
		t.Fatalf("GET /events = %+v", page.Items)
	}
	if page.NextCursor != ev2.ID {
		t.Fatalf("next_cursor = %q, want %q", page.NextCursor, ev2.ID)
	}

	// since is exclusive.
	res = testutil.Do(t, srv, "GET", "/events?since="+ev1.ID, nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != ev2.ID {
		t.Fatalf("GET /events?since = %+v", page.Items)
	}

	// limit pages.
	res = testutil.Do(t, srv, "GET", "/events?limit=1", nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.NextCursor != ev1.ID {
		t.Fatalf("GET /events?limit=1 = %+v cursor %q", page.Items, page.NextCursor)
	}
	// follow the cursor.
	res = testutil.Do(t, srv, "GET", "/events?limit=1&since="+page.NextCursor, nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != ev2.ID {
		t.Fatalf("GET /events cursor page = %+v", page.Items)
	}

	// type filter.
	res = testutil.Do(t, srv, "GET", "/events?type=note", nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != ev2.ID {
		t.Fatalf("GET /events?type=note = %+v", page.Items)
	}
}

func TestAgentEvents(t *testing.T) {
	srv := testutil.NewServer(t)
	// Agent must exist first (404 otherwise).
	if res := testutil.Do(t, srv, "GET", "/agents/nope/events", nil, nil); res.Status != 404 {
		t.Fatalf("GET events of missing agent status = %d, want 404", res.Status)
	}
	if res := testutil.Do(t, srv, "POST", "/agents/nope/events", map[string]any{"type": "steer"}, nil); res.Status != 404 {
		t.Fatalf("POST event to missing agent status = %d, want 404", res.Status)
	}

	testutil.Do(t, srv, "POST", "/agents", map[string]any{"id": "a1", "role": "worker"}, nil)
	ev := postEvent(t, srv, "/agents/a1/events", map[string]any{"type": "steer", "payload": map[string]any{"msg": "hi"}})
	if ev.AgentID != "a1" {
		t.Fatalf("agent event = %+v", ev)
	}

	// The event shows in the agent's list and the global log.
	var page struct {
		Items []evDTO `json:"items"`
	}
	res := testutil.Do(t, srv, "GET", "/agents/a1/events", nil, nil)
	if res.Status != 200 {
		t.Fatalf("GET /agents/a1/events status = %d", res.Status)
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != ev.ID {
		t.Fatalf("agent events = %+v", page.Items)
	}
}

func TestEventsLongPoll(t *testing.T) {
	srv := testutil.NewServer(t)

	// With no new events, wait=1 returns an empty page after ~1s.
	start := time.Now()
	res := testutil.Do(t, srv, "GET", "/events?wait=1&since=01JZZZZZZZZZZZZZZZZZZZZZZZ", nil, nil)
	elapsed := time.Since(start)
	if res.Status != 200 {
		t.Fatalf("long poll status = %d", res.Status)
	}
	if elapsed < time.Second {
		t.Fatalf("long poll returned after %v, want >= 1s", elapsed)
	}

	// A concurrent POST releases the poll in < 1s.
	done := make(chan bool, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		res := testutil.Do(t, srv, "POST", "/events", map[string]any{"type": "note"}, nil)
		var ev evDTO
		_ = json.Unmarshal(res.Body, &ev)
		_ = ev.ID
		done <- true
	}()
	start = time.Now()
	res = testutil.Do(t, srv, "GET", "/events?wait=5&since=01JZZZZZZZZZZZZZZZZZZZZZZZ", nil, nil)
	elapsed = time.Since(start)
	if res.Status != 200 {
		t.Fatalf("long poll status = %d", res.Status)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("long poll waited %v, want < 2s after publish", elapsed)
	}
	var page struct {
		Items []evDTO `json:"items"`
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 {
		t.Fatalf("long poll items = %+v", page.Items)
	}
}
