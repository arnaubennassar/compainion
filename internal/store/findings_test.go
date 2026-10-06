package store

import (
	"context"
	"testing"

	"github.com/arnaubennassar/compainion/internal/domain"
)

func seedFindingAgent(t *testing.T, db *DB) string {
	t.Helper()
	a := seedAgent(t, db, "01TESTFINDER000000000000000")
	return a.ID
}

// seedPlanWithStep inserts plan + step rows directly (plans/steps repos belong
// to a concurrent task) so step_added resolution has a real step to reference.
func seedPlanWithStep(t *testing.T, db *DB, wsID string) (planID, stepID string) {
	t.Helper()
	planID = "01TESTPLAN0000000000000000000"
	stepID = "01TESTSTEP0000000000000000000"
	if _, err := db.Exec(`INSERT INTO plans (id, workstream_id, title, status, created_at, updated_at)
		VALUES (?, ?, 'p', 'draft', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, planID, wsID); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO steps (id, plan_id, kind, title, status, added_by, created_at, updated_at)
		VALUES (?, ?, 'task', 's', 'pending', 'planner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, stepID, planID); err != nil {
		t.Fatalf("seed step: %v", err)
	}
	return planID, stepID
}

func baseFinding(agentID string) Finding {
	return Finding{
		ReportedByAgentID: agentID,
		Category:          "bug",
		Severity:          "high",
		Location:          "internal/x.go:12",
		Title:             "Too many logs",
		Details:           "noisy logs",
		Evidence:          `["run A"]`,
	}
}

func TestFindingUpsertCreatesAndDedupes(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	agentID := seedFindingAgent(t, db)

	f, created, err := db.UpsertFinding(ctx, baseFinding(agentID))
	if err != nil || !created {
		t.Fatalf("UpsertFinding = %v, created=%v; want created", err, created)
	}
	if f.Status != "new" || f.Occurrences != 1 {
		t.Errorf("new finding = %+v", f)
	}
	if f.Fingerprint == "" || len(f.Fingerprint) != 16 {
		t.Errorf("fingerprint = %q", f.Fingerprint)
	}

	// same fingerprint (different line number in location) -> bump, merge evidence
	dup := baseFinding(agentID)
	dup.Location = "internal/x.go:99"
	dup.Details = "still noisy"
	dup.Evidence = `["run B"]`
	got, created, err := db.UpsertFinding(ctx, dup)
	if err != nil {
		t.Fatalf("UpsertFinding dup = %v", err)
	}
	if created {
		t.Error("second report must dedupe, created=false")
	}
	if got.ID != f.ID {
		t.Errorf("dedupe must return the existing row, got %s want %s", got.ID, f.ID)
	}
	if got.Occurrences != 2 {
		t.Errorf("occurrences = %d, want 2", got.Occurrences)
	}
	if got.Details != "noisy logs" {
		t.Errorf("dedupe must not overwrite details, got %q", got.Details)
	}
	if got.Evidence != `["run A","run B"]` {
		t.Errorf("evidence = %s, want merged array", got.Evidence)
	}
	// same evidence string is not duplicated
	dup.Evidence = `["run B"]`
	got, created, err = db.UpsertFinding(ctx, dup)
	if err != nil || created || got.Occurrences != 3 {
		t.Fatalf("third report = %v, %v, occ=%d", err, created, got.Occurrences)
	}
	if got.Evidence != `["run A","run B"]` {
		t.Errorf("duplicate evidence must not repeat, got %s", got.Evidence)
	}
}

func TestFindingUpsertValidation(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	agentID := seedFindingAgent(t, db)
	f := baseFinding(agentID)
	f.Category = ""
	if _, _, err := db.UpsertFinding(ctx, f); err == nil {
		t.Error("missing category must be rejected")
	}
	f = baseFinding(agentID)
	f.Title = ""
	if _, _, err := db.UpsertFinding(ctx, f); err == nil {
		t.Error("missing title must be rejected")
	}
	f = baseFinding(agentID)
	f.Severity = "critical"
	if _, _, err := db.UpsertFinding(ctx, f); err == nil {
		t.Error("unknown severity must be rejected")
	}
}

func TestFindingResolvedNotResurfaced(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, err := db.CreateWorkstream(ctx, Workstream{Title: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	agentID := seedFindingAgent(t, db)
	seedPlanWithStep(t, db, ws.ID)
	planID := "01TESTPLAN0000000000000000000"
	base := baseFinding(agentID)
	base.PlanID = &planID
	f, _, err := db.UpsertFinding(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveFinding(ctx, f.ID, "ignored", ""); err != nil {
		t.Fatalf("ResolveFinding ignored = %v", err)
	}
	// same fingerprint again: bump occurrences, never re-surface
	got, created, err := db.UpsertFinding(ctx, baseFinding(agentID))
	if err != nil || created {
		t.Fatalf("UpsertFinding after resolve = %v, created=%v; want not created", err, created)
	}
	if got.ID != f.ID || got.Status != "resolved" {
		t.Errorf("resolved finding must stay resolved: %+v", got)
	}
	if got.Occurrences != 2 {
		t.Errorf("occurrences = %d, want 2", got.Occurrences)
	}
	if got.Resolution == nil || *got.Resolution != "ignored" {
		t.Errorf("resolution = %v", got.Resolution)
	}
	// not listed as new/surfaced
	open, err := db.ListFindings(ctx, FindingFilter{Status: "new"})
	if err != nil || len(open) != 0 {
		t.Errorf("ListFindings new = %v, %d", err, len(open))
	}
	resolved, err := db.ListFindings(ctx, FindingFilter{Status: "resolved"})
	if err != nil || len(resolved) != 1 {
		t.Fatalf("ListFindings resolved = %v, %d", err, len(resolved))
	}
	if resolved[0].PlanID == nil || *resolved[0].PlanID != planID {
		t.Errorf("plan_id = %v", resolved[0].PlanID)
	}
}

func TestFindingResolvedStepAddedCanBeReReported(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, err := db.CreateWorkstream(ctx, Workstream{Title: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	agentID := seedFindingAgent(t, db)
	_, stepID := seedPlanWithStep(t, db, ws.ID)

	f, _, err := db.UpsertFinding(ctx, baseFinding(agentID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveFinding(ctx, f.ID, "step_added", stepID); err != nil {
		t.Fatalf("ResolveFinding step_added = %v", err)
	}
	// re-report: becomes a NEW finding
	got, created, err := db.UpsertFinding(ctx, baseFinding(agentID))
	if err != nil {
		t.Fatalf("re-report = %v", err)
	}
	if !created {
		t.Error("step_added resolution allows re-report as new finding")
	}
	if got.ID == f.ID || got.Status != "new" || got.Occurrences != 1 {
		t.Errorf("re-reported finding = %+v", got)
	}
}

func TestFindingResolveValidation(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	ws, err := db.CreateWorkstream(ctx, Workstream{Title: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	agentID := seedFindingAgent(t, db)
	_, stepID := seedPlanWithStep(t, db, ws.ID)
	f, _, err := db.UpsertFinding(ctx, baseFinding(agentID))
	if err != nil {
		t.Fatal(err)
	}

	// resolution required
	if _, err := db.ResolveFinding(ctx, f.ID, "", ""); err == nil {
		t.Error("empty resolution must be rejected")
	}
	if _, err := db.ResolveFinding(ctx, f.ID, "whatever", ""); err == nil {
		t.Error("unknown resolution must be rejected")
	}
	// issue_opened needs resolution_ref
	if _, err := db.ResolveFinding(ctx, f.ID, "issue_opened", ""); err == nil {
		t.Error("issue_opened without resolution_ref must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
		t.Errorf("expected Unprocessable, got %v", err)
	}
	// step_added needs an existing step id
	if _, err := db.ResolveFinding(ctx, f.ID, "step_added", "01NOSUCHSTEP000000000000000"); err == nil {
		t.Error("step_added with unknown step must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
	// happy paths
	if _, err := db.ResolveFinding(ctx, f.ID, "step_added", stepID); err != nil {
		t.Fatalf("ResolveFinding step_added = %v", err)
	}
	got, err := db.GetFinding(ctx, f.ID)
	if err != nil || got.Status != "resolved" || got.Resolution == nil || *got.Resolution != "step_added" || got.ResolutionRef == nil || *got.ResolutionRef != stepID {
		t.Fatalf("resolved finding = %v, %+v", err, got)
	}
	// already resolved -> Conflict
	if _, err := db.ResolveFinding(ctx, f.ID, "ignored", ""); err == nil {
		t.Error("re-resolve must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Conflict {
		t.Errorf("expected Conflict, got %v", err)
	}
	// unknown finding
	if _, err := db.ResolveFinding(ctx, "01NOSUCHFINDING000000000000", "ignored", ""); err == nil {
		t.Error("resolve unknown must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestFindingListQueryFilter(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	agentID := seedFindingAgent(t, db)
	if _, _, err := db.UpsertFinding(ctx, baseFinding(agentID)); err != nil {
		t.Fatal(err)
	}
	other := baseFinding(agentID)
	other.Title = "unrelated thing"
	other.Location = "internal/y.go"
	other.Details = "different matter"
	if _, _, err := db.UpsertFinding(ctx, other); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListFindings(ctx, FindingFilter{Query: "LOGS"}) // case-insensitive
	if err != nil || len(got) != 1 || got[0].Title != "Too many logs" {
		t.Fatalf("query filter = %v, %+v", err, got)
	}
	got, err = db.ListFindings(ctx, FindingFilter{Query: "unrelated"})
	if err != nil || len(got) != 1 {
		t.Fatalf("query filter 2 = %v, %d", err, len(got))
	}
	// fingerprint matches too
	f1, _ := db.ListFindings(ctx, FindingFilter{})
	got, err = db.ListFindings(ctx, FindingFilter{Query: f1[0].Fingerprint})
	if err != nil || len(got) != 1 || got[0].Fingerprint != f1[0].Fingerprint {
		t.Fatalf("fingerprint query = %v, %+v", err, got)
	}
}

func TestFindingMarkSurfaced(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)
	f, _, err := db.UpsertFinding(ctx, baseFinding(agentID))
	if err != nil {
		t.Fatal(err)
	}
	it, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "findings", Kind: "finding",
		Questions: []Question{mkQuestion("q", "choice", mkSuggestion("open issue", true))},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.MarkFindingSurfaced(ctx, f.ID, it.ID)
	if err != nil {
		t.Fatalf("MarkFindingSurfaced = %v", err)
	}
	if got.Status != "surfaced" || got.InterruptionID == nil || *got.InterruptionID != it.ID {
		t.Errorf("surfaced finding = %+v", got)
	}
	// unknown interruption -> NotFound
	if _, err := db.MarkFindingSurfaced(ctx, f.ID, "01NOSUCHINTERRUP00000000000"); err == nil {
		t.Error("surface with unknown interruption must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
	// resolved cannot be surfaced
	if _, err := db.ResolveFinding(ctx, f.ID, "ignored", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFindingSurfaced(ctx, f.ID, it.ID); err == nil {
		t.Error("surfacing a resolved finding must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Conflict {
		t.Errorf("expected Conflict, got %v", err)
	}
	// unknown finding
	if _, err := db.MarkFindingSurfaced(ctx, "01NOSUCHFINDING000000000000", it.ID); err == nil {
		t.Error("surface unknown must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}
