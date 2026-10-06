package store

import (
	"context"
	"testing"

	"github.com/arnaubennassar/compainion/internal/domain"
)

func seedAgent(t *testing.T, db *DB, id string) Agent {
	t.Helper()
	a, err := db.RegisterAgent(context.Background(), Agent{ID: id, Role: "worker", Harness: "hermes", Handle: `{"tmux":"x"}`})
	if err != nil {
		t.Fatalf("RegisterAgent = %v", err)
	}
	return a
}

func TestAgentRegisterIdempotent(t *testing.T) {
	db, _ := mustOpen(t)
	a := seedAgent(t, db, "01TESTAGENT000000000000000")
	a2, err := db.RegisterAgent(context.Background(), Agent{ID: a.ID, Role: "worker"})
	if err != nil || a2.ID != a.ID {
		t.Fatalf("re-register = %v, %q", err, a2.ID)
	}
	if a2.Handle != a.Handle || a2.CreatedAt != a.CreatedAt {
		t.Errorf("re-register must return the existing row unchanged, got %+v / %+v", a, a2)
	}
}

func TestAgentRegisterGeneratesID(t *testing.T) {
	db, _ := mustOpen(t)
	a, err := db.RegisterAgent(context.Background(), Agent{Role: "companion"})
	if err != nil || a.ID == "" {
		t.Fatalf("RegisterAgent = %v, %q", err, a.ID)
	}
	if a.Status != "starting" {
		t.Errorf("status = %q, want starting", a.Status)
	}
}

func TestAgentHeartbeat(t *testing.T) {
	db, _ := mustOpen(t)
	a := seedAgent(t, db, "01TESTAGENT000000000000000")
	if err := db.AgentHeartbeat(context.Background(), a.ID); err != nil {
		t.Fatalf("AgentHeartbeat = %v", err)
	}
	got, err := db.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("GetAgent = %v", err)
	}
	if got.LastHeartbeatAt == nil || *got.LastHeartbeatAt == "" {
		t.Error("heartbeat must set last_heartbeat_at")
	}
}

func TestAgentUpdateStatusStaleHeartbeatDoesNotChangeStatus(t *testing.T) {
	db, _ := mustOpen(t)
	a := seedAgent(t, db, "01TESTAGENT000000000000000")
	if err := db.AgentHeartbeat(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	// time passes, no more heartbeats: backend must NOT touch status
	got, err := db.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "starting" {
		t.Errorf("stale heartbeat must leave status untouched, got %q", got.Status)
	}
	// valid transition
	if err := db.UpdateAgentStatus(context.Background(), a.ID, "running"); err != nil {
		t.Fatalf("UpdateAgentStatus = %v", err)
	}
	// finished is terminal -> any further transition is Conflict
	if err := db.UpdateAgentStatus(context.Background(), a.ID, "finished"); err != nil {
		t.Fatalf("UpdateAgentStatus = %v", err)
	}
	if err := db.UpdateAgentStatus(context.Background(), a.ID, "running"); err == nil {
		t.Error("running from finished must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Conflict {
		t.Errorf("expected Conflict, got %v", err)
	}
}

func TestAgentListFilters(t *testing.T) {
	db, _ := mustOpen(t)
	parent := seedAgent(t, db, "01TESTPARENT000000000000000")
	child := seedAgent(t, db, "01TESTCHILD000000000000000")
	if _, err := db.Exec(`UPDATE agents SET parent_id = ? WHERE id = ?`, parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	ws, _ := db.CreateWorkstream(context.Background(), Workstream{Title: "w"})
	if _, err := db.Exec(`UPDATE agents SET workstream_id = ? WHERE id = ?`, ws.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListAgents(context.Background(), AgentFilter{Role: "worker"})
	if err != nil || len(got) != 2 {
		t.Fatalf("role filter = %v, %d", err, len(got))
	}
	got, err = db.ListAgents(context.Background(), AgentFilter{ParentID: parent.ID})
	if err != nil || len(got) != 1 || got[0].ID != child.ID {
		t.Fatalf("parent filter = %v, %+v", err, got)
	}
	got, err = db.ListAgents(context.Background(), AgentFilter{WorkstreamID: ws.ID})
	if err != nil || len(got) != 1 || got[0].ID != child.ID {
		t.Fatalf("workstream filter = %v, %+v", err, got)
	}
	got, err = db.ListAgents(context.Background(), AgentFilter{Status: "starting"})
	if err != nil || len(got) != 2 {
		t.Fatalf("status filter = %v, %d", err, len(got))
	}
	got, err = db.ListAgents(context.Background(), AgentFilter{})
	if err != nil || len(got) != 2 {
		t.Fatalf("no filter = %v, %d", err, len(got))
	}
}