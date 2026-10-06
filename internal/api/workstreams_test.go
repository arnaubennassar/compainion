package api_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/arnaubennassar/compainion/internal/api"
	"github.com/arnaubennassar/compainion/internal/store"
	"github.com/arnaubennassar/compainion/internal/testutil"
)

func TestWorkstreamCRUD(t *testing.T) {
	srv := testutil.NewServer(t)

	// POST /workstreams happy path.
	res := testutil.Do(t, srv, "POST", "/workstreams", map[string]any{
		"title": "ws one", "priority": 1,
	}, nil)
	if res.Status != 201 {
		t.Fatalf("POST /workstreams status = %d, want 201: %s", res.Status, res.Body)
	}
	var ws store.Workstream
	res.Unmarshal(t, &ws)
	if ws.ID == "" || ws.Title != "ws one" || ws.Status != "active" || ws.Priority != 1 {
		t.Fatalf("created workstream = %+v", ws)
	}
	if ws.CreatedAt == "" || ws.UpdatedAt == "" {
		t.Fatalf("created workstream missing timestamps: %+v", ws)
	}

	// POST missing title -> 400 problem.
	res = testutil.Do(t, srv, "POST", "/workstreams", map[string]any{"priority": 1}, nil)
	if res.Status != 400 {
		t.Fatalf("POST /workstreams without title status = %d, want 400", res.Status)
	}

	// POST unknown field -> 400 (strict decode).
	res = testutil.Do(t, srv, "POST", "/workstreams", map[string]any{"title": "t", "bogus": 1}, nil)
	if res.Status != 400 {
		t.Fatalf("POST /workstreams unknown field status = %d, want 400", res.Status)
	}

	// GET list envelope.
	res = testutil.Do(t, srv, "GET", "/workstreams", nil, nil)
	if res.Status != 200 {
		t.Fatalf("GET /workstreams status = %d, want 200", res.Status)
	}
	var page struct {
		Items      []store.Workstream `json:"items"`
		NextCursor string             `json:"next_cursor"`
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != ws.ID {
		t.Fatalf("GET /workstreams items = %+v", page.Items)
	}

	// GET one.
	res = testutil.Do(t, srv, "GET", "/workstreams/"+ws.ID, nil, nil)
	if res.Status != 200 {
		t.Fatalf("GET workstream status = %d, want 200", res.Status)
	}
	res.Unmarshal(t, &ws)
	if ws.Title != "ws one" {
		t.Fatalf("GET workstream = %+v", ws)
	}

	// GET unknown -> 404.
	if res := testutil.Do(t, srv, "GET", "/workstreams/nope", nil, nil); res.Status != 404 {
		t.Fatalf("GET missing workstream status = %d, want 404", res.Status)
	}

	// PATCH happy path.
	res = testutil.Do(t, srv, "PATCH", "/workstreams/"+ws.ID, map[string]any{
		"title": "renamed", "status": "paused",
	}, nil)
	if res.Status != 200 {
		t.Fatalf("PATCH workstream status = %d, want 200: %s", res.Status, res.Body)
	}
	res.Unmarshal(t, &ws)
	if ws.Title != "renamed" || ws.Status != "paused" {
		t.Fatalf("PATCHed workstream = %+v", ws)
	}

	// PATCH unknown -> 404.
	res = testutil.Do(t, srv, "PATCH", "/workstreams/nope", map[string]any{"title": "x"}, nil)
	if res.Status != 404 {
		t.Fatalf("PATCH missing workstream status = %d, want 404", res.Status)
	}

	// DELETE happy -> 204; then 404.
	if res := testutil.Do(t, srv, "DELETE", "/workstreams/"+ws.ID, nil, nil); res.Status != 204 {
		t.Fatalf("DELETE workstream status = %d, want 204", res.Status)
	}
	if res := testutil.Do(t, srv, "DELETE", "/workstreams/"+ws.ID, nil, nil); res.Status != 404 {
		t.Fatalf("DELETE missing workstream status = %d, want 404", res.Status)
	}
}

func TestWorkstreamDeleteConflict(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	srv := httptest.NewServer(api.New(db).Handler())
	defer srv.Close()

	ws, err := db.CreateWorkstream(context.Background(), store.Workstream{Title: "busy"})
	if err != nil {
		t.Fatalf("create workstream: %v", err)
	}
	if _, err := db.CreatePlan(context.Background(), store.Plan{WorkstreamID: ws.ID, Title: "p", Goals: "g"}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if res := testutil.Do(t, srv, "DELETE", "/workstreams/"+ws.ID, nil, nil); res.Status != 409 {
		t.Fatalf("DELETE referenced workstream status = %d, want 409: %s", res.Status, res.Body)
	}
}
