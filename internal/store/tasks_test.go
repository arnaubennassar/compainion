package store

import (
	"context"
	"testing"

	"github.com/arnaubennassar/compainion/internal/domain"
)

func seedTaskCtx(t *testing.T, db *DB) (Workstream, Agent) {
	t.Helper()
	return seedPlanCtx(t, db)
}

func sampleTask(ws Workstream, by Agent) Task {
	return Task{WorkstreamID: ws.ID, RequestedBy: by.ID, Title: "do a thing", AddedBy: "planner",
		AcceptanceCriteria: "ac", Scope: `{"read":["docs/"]}`}
}

func TestCreateTask(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, by := seedTaskCtx(t, db)
	tk, err := db.CreateTask(ctx, sampleTask(ws, by))
	if err != nil {
		t.Fatalf("CreateTask = %v", err)
	}
	if tk.Status != "pending" || tk.Version != 1 || tk.ID == "" || tk.CreatedAt == "" {
		t.Errorf("CreateTask = %+v", tk)
	}
	// unknown workstream
	bad := sampleTask(ws, by)
	bad.WorkstreamID = "01NOPE"
	if _, err := db.CreateTask(ctx, bad); err == nil {
		t.Error("unknown workstream must fail")
	}
	// missing title
	bad2 := sampleTask(ws, by)
	bad2.Title = ""
	if _, err := db.CreateTask(ctx, bad2); err == nil {
		t.Error("title required")
	}
	// Get / List / unknown
	got, err := db.GetTask(ctx, tk.ID)
	if err != nil || got.ID != tk.ID {
		t.Fatalf("GetTask = %+v, %v", got, err)
	}
	if _, err := db.GetTask(ctx, "01NOPE"); err == nil {
		t.Error("unknown task must be NotFound")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("kind = %v", err)
	}
	ls, err := db.ListTasks(ctx, TaskFilter{WorkstreamID: ws.ID})
	if err != nil || len(ls) != 1 {
		t.Fatalf("ListTasks = %d, %v", len(ls), err)
	}
}

func TestClaimFinishTask(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, by := seedTaskCtx(t, db)
	tk, _ := db.CreateTask(ctx, sampleTask(ws, by))
	worker := seedWorker(t, db)

	got, err := db.ClaimTask(ctx, tk.ID, worker.ID, 1)
	if err != nil {
		t.Fatalf("ClaimTask = %v", err)
	}
	if got.Status != "in_progress" || got.Attempt != 1 || got.AssigneeAgentID == nil || *got.AssigneeAgentID != worker.ID || got.Version != 2 {
		t.Errorf("claimed = %+v", got)
	}
	// second claim -> 409
	if _, err := db.ClaimTask(ctx, tk.ID, worker.ID, got.Version); err == nil {
		t.Error("second claim must 409")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Conflict {
		t.Errorf("kind = %v", err)
	}
	// started event
	evs, _ := db.ListEvents(ctx, EventFilter{TaskID: tk.ID, Types: []string{"started"}})
	if len(evs) == 0 {
		t.Error("claim must emit started event")
	}
	// outcome validation
	if _, err := db.FinishTask(ctx, tk.ID, got.Version, "done", `{}`); err == nil {
		t.Error("finish without outcome result must 422")
	}
	if _, err := db.FinishTask(ctx, tk.ID, got.Version, "done", `{"result":"ok"}`); err == nil {
		t.Error("done without evidence must 422")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
		t.Errorf("kind = %v", err)
	}
	// stale version -> PreconditionFailed
	if _, err := db.FinishTask(ctx, tk.ID, 1, "done", `{"result":"ok","evidence":["e"]}`); err == nil {
		t.Fatal("stale version finish must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.PreconditionFailed {
		t.Errorf("kind = %v", err)
	}
	// blocked finish works
	if fb, err := db.FinishTask(ctx, tk.ID, got.Version, "blocked", `{"result":"waiting"}`); err != nil || fb.Status != "blocked" {
		t.Fatalf("FinishTask blocked = %+v, %v", fb, err)
	}
	evs, _ = db.ListEvents(ctx, EventFilter{TaskID: tk.ID, Types: []string{"finished"}})
	if len(evs) == 0 {
		t.Error("finish must emit finished event")
	}
}

func TestTaskLifecycleHappyPath(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, by := seedTaskCtx(t, db)
	tk, _ := db.CreateTask(ctx, sampleTask(ws, by))
	worker := seedWorker(t, db)
	if _, err := db.ClaimTask(ctx, tk.ID, worker.ID, 1); err != nil {
		t.Fatalf("ClaimTask = %v", err)
	}
	if _, err := db.FinishTask(ctx, tk.ID, 2, "done", `{"result":"ok","evidence":["e"]}`); err != nil {
		t.Fatalf("FinishTask = %v", err)
	}
	got, _ := db.GetTask(ctx, tk.ID)
	if got.Status != "done" || got.Version != 3 || got.Attempt != 1 {
		t.Errorf("task = %+v", got)
	}
}
