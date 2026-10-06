package store

import (
	"context"
	"testing"

	"github.com/arnaubennassar/compainion/internal/ids"

	"github.com/arnaubennassar/compainion/internal/domain"
)

func seedPlanCtx(t *testing.T, db *DB) (Workstream, Agent) {
	t.Helper()
	ctx := context.Background()
	ws, err := db.CreateWorkstream(ctx, Workstream{Title: "ws"})
	if err != nil {
		t.Fatalf("CreateWorkstream = %v", err)
	}
	a := seedAgent(t, db, "01TESTPLANNER0000000000000")
	return ws, a
}

func samplePlan(ws Workstream, a Agent) Plan {
	return Plan{
		WorkstreamID:       ws.ID,
		Title:              "Ship it",
		Summary:            "s",
		Goals:              "make it work",
		AcceptanceCriteria: "tests green",
		AcceptanceChecks:   `["t1"]`,
		Scope:              `{"write":["src/"]}`,
		CreatorAgentID:     a.ID,
	}
}

func TestCreatePlanAutoGoal(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, a := seedPlanCtx(t, db)
	p, err := db.CreatePlan(ctx, samplePlan(ws, a))
	if err != nil {
		t.Fatalf("CreatePlan = %v", err)
	}
	if p.Status != "draft" || p.Version != 1 {
		t.Errorf("status/version = %s/%d, want draft/1", p.Status, p.Version)
	}
	if p.GoalStepID == nil || *p.GoalStepID == "" {
		t.Fatalf("goal_step_id not set: %+v", p)
	}
	g, err := db.GetStep(ctx, *p.GoalStepID)
	if err != nil {
		t.Fatalf("GetStep(goal) = %v", err)
	}
	if g.Kind != "goal" || g.PlanID != p.ID || g.Status != "pending" || g.AddedBy != "planner" {
		t.Errorf("goal step = %+v", g)
	}
	if len(g.Deps) != 0 {
		t.Errorf("goal must start with no deps, got %v", g.Deps)
	}
	// duplicate title etc fine; created event exists
	evs, _ := db.ListEvents(ctx, EventFilter{PlanID: p.ID})
	if len(evs) == 0 {
		t.Error("expected a lifecycle event on plan creation")
	}
}

func TestPlanGetList(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, a := seedPlanCtx(t, db)
	p1, err := db.CreatePlan(ctx, samplePlan(ws, a))
	if err != nil {
		t.Fatalf("CreatePlan = %v", err)
	}
	p2, err := db.CreatePlan(ctx, samplePlan(ws, a))
	if err != nil {
		t.Fatalf("CreatePlan = %v", err)
	}
	got, err := db.GetPlan(ctx, p1.ID)
	if err != nil || got.ID != p1.ID {
		t.Fatalf("GetPlan = %v, %v", got, err)
	}
	if _, err := db.GetPlan(ctx, "01NOPE"); err == nil {
		t.Error("GetPlan(unknown) must be NotFound")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("kind = %v", err)
	}
	ls, err := db.ListPlans(ctx, PlanFilter{WorkstreamID: ws.ID})
	if err != nil || len(ls) != 2 {
		t.Fatalf("ListPlans = %d, %v", len(ls), err)
	}
	if len(ls) > 1 && ls[0].ID > ls[1].ID {
		t.Error("ListPlans must be in creation order")
	}
	_ = p2
}

func TestPlanUpdateVersionAndApprovalRef(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, a := seedPlanCtx(t, db)
	p, _ := db.CreatePlan(ctx, samplePlan(ws, a))

	// draft: goal change without approval_ref is fine
	np, err := db.UpdatePlan(ctx, p.ID, 1, PlanUpdate{Goals: strp("new goal")}, "")
	if err != nil {
		t.Fatalf("UpdatePlan draft = %v", err)
	}
	if np.Version != 2 || np.Goals != "new goal" {
		t.Errorf("UpdatePlan = v%d goals=%q", np.Version, np.Goals)
	}
	// version mismatch -> PreconditionFailed
	if _, err := db.UpdatePlan(ctx, p.ID, 1, PlanUpdate{Title: strp("x")}, ""); err == nil {
		t.Fatal("stale version must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.PreconditionFailed {
		t.Errorf("kind = %v", err)
	}

	// approve, then a goal change without approval_ref -> 428
	if _, err := db.ApprovePlan(ctx, p.ID, "user"); err != nil {
		t.Fatalf("ApprovePlan = %v", err)
	}
	if _, err := db.UpdatePlan(ctx, p.ID, 3, PlanUpdate{Goals: strp("change")}, ""); err == nil {
		t.Fatal("goal change on approved plan without approval_ref must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.PreconditionRequired {
		t.Errorf("kind = %v", err)
	}
	// non-goal change on approved plan needs no approval_ref
	if _, err := db.UpdatePlan(ctx, p.ID, 3, PlanUpdate{Title: strp("renamed")}, ""); err != nil {
		t.Fatalf("title change on approved = %v", err)
	}
}

func TestPlanUpdateWithApprovalRef(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, a := seedPlanCtx(t, db)
	p, _ := db.CreatePlan(ctx, samplePlan(ws, a))
	if _, err := db.ApprovePlan(ctx, p.ID, "user"); err != nil {
		t.Fatalf("ApprovePlan = %v", err)
	}
	// unrelated answered interruption -> 428
	irr := seedInterruption(t, db, &ws, &a, nil)
	answerInterruption(t, db, irr, "user")
	if _, err := db.UpdatePlan(ctx, p.ID, 2, PlanUpdate{Goals: strp("x")}, irr.ID); err == nil {
		t.Fatal("approval_ref from another plan must fail")
	}
	// answered interruption for this plan -> applies, approved_* set from it
	own := seedInterruption(t, db, &ws, &a, &p)
	answerInterruption(t, db, own, "user")
	np, err := db.UpdatePlan(ctx, p.ID, 2, PlanUpdate{Goals: strp("approved goal")}, own.ID)
	if err != nil {
		t.Fatalf("UpdatePlan with approval_ref = %v", err)
	}
	if np.Goals != "approved goal" {
		t.Errorf("goals = %q", np.Goals)
	}
	if np.ApprovedBy == nil || *np.ApprovedBy != a.ID {
		t.Errorf("approved_by must come from the interruption raiser: %+v", np.ApprovedBy)
	}
	if np.ApprovedAt == nil || *np.ApprovedAt == "" {
		t.Error("approved_at must be set from the interruption")
	}
	// unanswered interruption -> 428
	open := seedInterruption(t, db, &ws, &a, &p)
	if _, err := db.UpdatePlan(ctx, p.ID, 3, PlanUpdate{Goals: strp("y")}, open.ID); err == nil {
		t.Fatal("unanswered interruption must not authorize goal change")
	}
}

func TestPlanApproveAndAssign(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, a := seedPlanCtx(t, db)
	p, _ := db.CreatePlan(ctx, samplePlan(ws, a))
	// approve requires approved_by
	if _, err := db.ApprovePlan(ctx, p.ID, ""); err == nil {
		t.Fatal("ApprovePlan without approved_by must fail")
	}
	ap, err := db.ApprovePlan(ctx, p.ID, "user")
	if err != nil {
		t.Fatalf("ApprovePlan = %v", err)
	}
	if ap.Status != "approved" || ap.ApprovedBy == nil || *ap.ApprovedBy != "user" || ap.ApprovedAt == nil {
		t.Errorf("ApprovePlan = %+v", ap)
	}
	// double approve -> Conflict (draft -> approved only)
	if _, err := db.ApprovePlan(ctx, p.ID, "user"); err == nil {
		t.Error("second approve must conflict")
	}
	// assign requires approved status
	ws2, _ := db.CreateWorkstream(ctx, Workstream{Title: "w2"})
	p2, _ := db.CreatePlan(ctx, samplePlan(ws2, a))
	if _, err := db.AssignPlan(ctx, p2.ID, a.ID); err == nil {
		t.Error("assign on draft must fail")
	}
	// agent must have orchestrator role
	worker := seedAgent(t, db, "01TESTWORKER0000000000000")
	if _, err := db.AssignPlan(ctx, p.ID, worker.ID); err == nil {
		t.Error("assign non-orchestrator must fail 422")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
		t.Errorf("kind = %v", err)
	}
	orch, _ := db.RegisterAgent(ctx, Agent{Role: "orchestrator"})
	got, err := db.AssignPlan(ctx, p.ID, orch.ID)
	if err != nil {
		t.Fatalf("AssignPlan = %v", err)
	}
	if got.OrchestratorAgentID == nil || *got.OrchestratorAgentID != orch.ID {
		t.Errorf("AssignPlan = %+v", got)
	}
}

func TestPlanGraph(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, a := seedPlanCtx(t, db)
	p, _ := db.CreatePlan(ctx, samplePlan(ws, a))
	sa, err := db.CreateStep(ctx, Step{PlanID: p.ID, Kind: "task", Title: "A", AddedBy: "planner"})
	if err != nil {
		t.Fatalf("CreateStep A = %v", err)
	}
	sb, err := db.CreateStep(ctx, Step{PlanID: p.ID, Kind: "task", Title: "B", AddedBy: "planner", Deps: []string{sa.ID}})
	if err != nil {
		t.Fatalf("CreateStep B = %v", err)
	}
	g, err := db.PlanGraph(ctx, p.ID)
	if err != nil {
		t.Fatalf("PlanGraph = %v", err)
	}
	if len(g.Steps) != 3 {
		t.Fatalf("graph steps = %d, want 3", len(g.Steps))
	}
	goal := g.Plan.GoalStepID
	var goalDeps []string
	for _, s := range g.Steps {
		if s.ID == *goal {
			goalDeps = s.Deps
		}
		if s.ID == sa.ID {
			if !s.Ready {
				t.Error("A must be ready")
			}
		}
		if s.ID == sb.ID && s.Ready {
			t.Error("B must not be ready (depends on pending A)")
		}
	}
	if len(goalDeps) != 1 || goalDeps[0] != sb.ID {
		t.Errorf("leaf steps feed the goal: goal deps = %v, want [%s]", goalDeps, sb.ID)
	}
	if len(g.Edges) < 2 {
		t.Errorf("edges = %v", g.Edges)
	}
}

// ---- helpers shared with steps/tasks tests ----

func strp(s string) *string { return &s }

// seedInterruption inserts one open interruption with a single free question
// via raw SQL (decoupled from the interruptions repository's validation rules,
// which this file's tests must not depend on).
func seedInterruption(t *testing.T, db *DB, ws *Workstream, by *Agent, plan *Plan) Interruption {
	t.Helper()
	ctx := context.Background()
	id := ids.New()
	if plan != nil {
		if _, err := db.ExecContext(ctx, `INSERT INTO interruptions (id, workstream_id, raised_by_agent_id, plan_id, topic, kind, priority, digest, blocking, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'clarification', 'normal', 'd', 1, 'open', ?, ?)`,
			id, ws.ID, by.ID, plan.ID, "t-"+id, now(), now()); err != nil {
			t.Fatalf("seed interruption: %v", err)
		}
	} else {
		if _, err := db.ExecContext(ctx, `INSERT INTO interruptions (id, workstream_id, raised_by_agent_id, topic, kind, priority, digest, blocking, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'clarification', 'normal', 'd', 1, 'open', ?, ?)`,
			id, ws.ID, by.ID, "t-"+id, now(), now()); err != nil {
			t.Fatalf("seed interruption: %v", err)
		}
	}
	qid := id + "Q1"
	if _, err := db.ExecContext(ctx, `INSERT INTO questions (id, interruption_id, position, text, answer_type, status, created_at, updated_at)
		VALUES (?, ?, 1, 'ok?', 'free_text', 'open', ?, ?)`, qid, id, now(), now()); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	return Interruption{ID: id, Questions: []Question{{ID: qid}}}
}

func planIDPtr(p *Plan) *string {
	if p == nil {
		s := "none"
		return &s
	}
	return &p.ID
}

// answerInterruption answers its only question finally via raw SQL.
func answerInterruption(t *testing.T, db *DB, irr Interruption, by string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO answers (id, question_id, author, mode, text, final, created_at, updated_at)
		VALUES (?, ?, 'user', 'free', 'ok', 1, ?, ?)`, "01TSTANSWER"+irr.ID[len(irr.ID)-8:], irr.Questions[0].ID, now(), now()); err != nil {
		t.Fatalf("seed answer: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE questions SET status='answered', updated_at=? WHERE id=?`, now(), irr.Questions[0].ID); err != nil {
		t.Fatalf("answer question: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE interruptions SET status='answered', answered_at=?, updated_at=? WHERE id=?`, now(), now(), irr.ID); err != nil {
		t.Fatalf("answer interruption: %v", err)
	}
}

// execForTest runs a raw statement (test scaffolding only).
func (d *DB) execForTest(ctx context.Context, q string, args ...any) error {
	_, err := d.ExecContext(ctx, q, args...)
	return err
}

// seedWorker registers a generic worker agent for claim tests.
func seedWorker(t *testing.T, db *DB) Agent {
	t.Helper()
	return seedAgent(t, db, ids.New())
}
