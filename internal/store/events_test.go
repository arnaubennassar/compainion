package store

import (
	"context"
	"database/sql"
	"testing"
)

func seedEventCtx(t *testing.T, db *DB) Agent {
	t.Helper()
	return seedAgent(t, db, "01TESTAGENT000000000000000")
}

func TestAppendEventTxAndList(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	a := seedEventCtx(t, db)
	// append two events in one tx
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := AppendEventTx(tx, Event{AgentID: a.ID, Type: "started", Payload: `{"x":1}`}); err != nil {
			return err
		}
		if _, err := AppendEventTx(tx, Event{AgentID: a.ID, Type: "finished", Payload: `{}`}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("WithTx = %v", err)
	}
	got, err := db.ListEvents(ctx, EventFilter{AgentID: a.ID})
	if err != nil || len(got) != 2 {
		t.Fatalf("ListEvents = %v, %d", err, len(got))
	}
	if got[0].ID >= got[1].ID {
		t.Error("events must be returned in ascending ULID order")
	}
	if got[0].At == "" || got[0].ID == "" {
		t.Errorf("event missing id/at: %+v", got[0])
	}
	// type filter (multi)
	got, err = db.ListEvents(ctx, EventFilter{AgentID: a.ID, Types: []string{"finished"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("type filter = %v, %d", err, len(got))
	}
	// since cursor is exclusive
	got, err = db.ListEvents(ctx, EventFilter{AgentID: a.ID, Since: got[0].ID})
	if err != nil || len(got) != 0 {
		t.Fatalf("since filter = %v, %d", err, len(got))
	}
}

func TestListEventsLimit(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	a := seedEventCtx(t, db)
	for i := 0; i < 5; i++ {
		if _, err := db.AppendEvent(ctx, Event{AgentID: a.ID, Type: "note"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListEvents(ctx, EventFilter{AgentID: a.ID, Limit: 2})
	if err != nil || len(got) != 2 {
		t.Fatalf("limit = %v, %d", err, len(got))
	}
	// cap at 500
	got, err = db.ListEvents(ctx, EventFilter{AgentID: a.ID, Limit: 9999})
	if err != nil || len(got) != 5 {
		t.Fatalf("cap = %v, %d", err, len(got))
	}
}

func TestPlanEventFilter(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	a := seedEventCtx(t, db)
	ws, err := db.CreateWorkstream(ctx, Workstream{Title: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plans (id, workstream_id, title, status, created_at, updated_at)
		VALUES ('p1', ?, 't', 'draft', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AppendEvent(ctx, Event{AgentID: a.ID, PlanID: "p1", Type: "note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AppendEvent(ctx, Event{AgentID: a.ID, Type: "note"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListEvents(ctx, EventFilter{PlanID: "p1"})
	if err != nil || len(got) != 1 || got[0].PlanID != "p1" {
		t.Fatalf("plan filter = %v, %+v", err, got)
	}
}