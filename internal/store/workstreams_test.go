package store

import (
	"context"
	"testing"

	"github.com/arnaubennassar/compainion/internal/domain"
)

func TestWorkstreamCRUD(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	w, err := db.CreateWorkstream(ctx, Workstream{Title: "test", Priority: 1})
	if err != nil {
		t.Fatalf("CreateWorkstream = %v", err)
	}
	if w.ID == "" || w.Status != "active" || w.Priority != 1 {
		t.Errorf("created = %+v", w)
	}
	got, err := db.GetWorkstream(ctx, w.ID)
	if err != nil || got.Title != "test" {
		t.Fatalf("GetWorkstream = %v, %+v", err, got)
	}
	if got.CreatedAt != got.UpdatedAt {
		t.Errorf("created_at %q != updated_at %q", got.CreatedAt, got.UpdatedAt)
	}

	title, status, prio := "renamed", "paused", 3
	up, err := db.UpdateWorkstream(ctx, w.ID, WorkstreamUpdate{Title: &title, Status: &status, Priority: &prio})
	if err != nil {
		t.Fatalf("UpdateWorkstream = %v", err)
	}
	if up.Title != "renamed" || up.Status != "paused" || up.Priority != 3 {
		t.Errorf("updated = %+v", up)
	}

	list, err := db.ListWorkstreams(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListWorkstreams = %v, %d", err, len(list))
	}

	if err := db.DeleteWorkstream(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkstream = %v", err)
	}
	if _, err := db.GetWorkstream(ctx, w.ID); err == nil {
		t.Error("GetWorkstream after delete must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestWorkstreamDeleteBlockedByPlans(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	w, err := db.CreateWorkstream(ctx, Workstream{Title: "ws"})
	if err != nil {
		t.Fatalf("CreateWorkstream = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, workstream_id, requested_by, title, status, added_by, created_at, updated_at)
		VALUES ('t1', ?, 'a', 'x', 'pending', 'user', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, w.ID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := db.DeleteWorkstream(ctx, w.ID); err == nil {
		t.Fatal("delete with tasks must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Conflict {
		t.Errorf("expected Conflict, got %v", err)
	}
}
