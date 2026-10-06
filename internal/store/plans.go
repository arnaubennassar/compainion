package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

const planCols = `id, workstream_id, title, summary, goals, acceptance_criteria, acceptance_checks, scope, status,
	creator_agent_id, orchestrator_agent_id, goal_step_id, approved_by, approved_at, version, created_at, updated_at`

func scanPlan(r interface{ Scan(...any) error }) (Plan, error) {
	var p Plan
	var creator, orch, goal, approvedBy, approvedAt sql.NullString
	if err := r.Scan(&p.ID, &p.WorkstreamID, &p.Title, &p.Summary, &p.Goals, &p.AcceptanceCriteria, &p.AcceptanceChecks,
		&p.Scope, &p.Status, &creator, &orch, &goal, &approvedBy, &approvedAt, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Plan{}, err
	}
	p.CreatorAgentID = creator.String
	if orch.Valid {
		s := orch.String
		p.OrchestratorAgentID = &s
	}
	if goal.Valid {
		s := goal.String
		p.GoalStepID = &s
	}
	if approvedBy.Valid {
		s := approvedBy.String
		p.ApprovedBy = &s
	}
	if approvedAt.Valid {
		s := approvedAt.String
		p.ApprovedAt = &s
	}
	return p, nil
}

// CreatePlan inserts a draft plan with version 1 and auto-creates its goal
// step in the same transaction (the goal starts with no dependencies;
// dependencies accumulate as steps are created).
func (d *DB) CreatePlan(ctx context.Context, p Plan) (Plan, error) {
	if p.WorkstreamID == "" {
		return Plan{}, domain.Errf(domain.Invalid, "workstream_id is required")
	}
	if p.Title == "" {
		return Plan{}, domain.Errf(domain.Invalid, "title is required")
	}
	if _, err := d.GetWorkstream(ctx, p.WorkstreamID); err != nil {
		return Plan{}, err
	}
	if p.Status == "" {
		p.Status = "draft"
	}
	if p.Status != "draft" {
		return Plan{}, domain.Errf(domain.Invalid, "plans start as draft")
	}
	if p.AcceptanceChecks == "" {
		p.AcceptanceChecks = "[]"
	}
	if p.Scope == "" {
		p.Scope = "{}"
	}
	p.ID = ids.New()
	p.Version = 1
	n := now()
	p.CreatedAt, p.UpdatedAt = n, n
	// creator may be a plain user; only reference a real agent in the event.
	agent := p.CreatorAgentID
	if agent != "" {
		if _, err := d.GetAgent(ctx, agent); err != nil {
			agent = ""
		}
	}
	goal := Step{
		PlanID: p.ID, Kind: "goal", Title: "Goal",
		AcceptanceCriteria: p.AcceptanceCriteria, Scope: p.Scope,
		Status: "pending", AddedBy: "planner", Version: 1,
	}
	goal.ID = ids.New()
	p.GoalStepID = &goal.ID
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO plans (id, workstream_id, title, summary, goals, acceptance_criteria, acceptance_checks, scope, status,
			creator_agent_id, orchestrator_agent_id, goal_step_id, approved_by, approved_at, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL, NULL, 1, ?, ?)`,
			p.ID, p.WorkstreamID, p.Title, p.Summary, p.Goals, p.AcceptanceCriteria, p.AcceptanceChecks, p.Scope,
			p.Status, nullStr(p.CreatorAgentID), goal.ID, n, n)
		if err != nil {
			return fmt.Errorf("store: create plan: %w", err)
		}
		if err := insertStepTx(tx, goal, nil); err != nil {
			return err
		}
		_, err = AppendEventTx(tx, Event{AgentID: agent, PlanID: p.ID, Type: "note",
			Payload: mustJSON(map[string]any{"kind": "plan_created", "goal_step_id": goal.ID})})
		return err
	})
	if err != nil {
		return Plan{}, err
	}
	return p, nil
}

// GetPlan returns one plan or NotFound.
func (d *DB) GetPlan(ctx context.Context, id string) (Plan, error) {
	p, err := scanPlan(d.QueryRowContext(ctx, `SELECT `+planCols+` FROM plans WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Plan{}, domain.Errf(domain.NotFound, "plan %s not found", id)
	}
	return p, err
}

// PlanFilter narrows ListPlans.
type PlanFilter struct {
	WorkstreamID string
	Status       string
}

// ListPlans returns plans matching the filter in creation order.
func (d *DB) ListPlans(ctx context.Context, f PlanFilter) ([]Plan, error) {
	q := `SELECT ` + planCols + ` FROM plans WHERE 1=1`
	var args []any
	if f.WorkstreamID != "" {
		q += ` AND workstream_id = ?`
		args = append(args, f.WorkstreamID)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY id`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list plans: %w", err)
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PlanUpdate is a PATCH-style plan change. Goals/AcceptanceCriteria/
// AcceptanceChecks count as "goal changes": on an approved-or-later plan they
// require ApprovalRef (an answered interruption for this plan). ApprovalRef
// also propagates the interruption's raiser/answer time into approved_*.
type PlanUpdate struct {
	Title              *string
	Summary            *string
	Goals              *string
	AcceptanceCriteria *string
	AcceptanceChecks   *string
	Scope              *string
}

func (u PlanUpdate) goalChange() bool {
	return u.Goals != nil || u.AcceptanceCriteria != nil || u.AcceptanceChecks != nil
}

// UpdatePlan applies the patch guarded on the expected version. A goal change
// on an approved+ plan without a valid approval_ref is PreconditionRequired.
func (d *DB) UpdatePlan(ctx context.Context, id string, expectedVersion int, u PlanUpdate, approvalRef string) (Plan, error) {
	p, err := d.GetPlan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if u.goalChange() && (p.Status == "approved" || p.Status == "running" || p.Status == "blocked" || p.Status == "done") {
		var by, at string
		err := d.QueryRowContext(ctx, `SELECT COALESCE(raised_by_agent_id, ''), answered_at FROM interruptions
			WHERE id = ? AND status = 'answered' AND plan_id = ?`, approvalRef, id).Scan(&by, &at)
		if err != nil {
			return Plan{}, domain.Errf(domain.PreconditionRequired,
				"goal change on %s plan requires approval_ref of an answered interruption on this plan", p.Status)
		}
		res, uerr := d.ExecContext(ctx, `UPDATE plans SET
					title = COALESCE(?, title),
					summary = COALESCE(?, summary),
					goals = COALESCE(?, goals),
					acceptance_criteria = COALESCE(?, acceptance_criteria),
					acceptance_checks = COALESCE(?, acceptance_checks),
					scope = COALESCE(?, scope),
					approved_by = ?, approved_at = ?,
					version = version + 1, updated_at = ?
					WHERE id = ? AND version = ?`,
			u.Title, u.Summary, u.Goals, u.AcceptanceCriteria, u.AcceptanceChecks, u.Scope,
			by, at, now(), id, expectedVersion)
		if uerr != nil {
			return Plan{}, fmt.Errorf("store: update plan: %w", uerr)
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return Plan{}, domain.Errf(domain.PreconditionFailed, "plan %s version %d != expected %d", id, p.Version, expectedVersion)
		}
	} else {
		res, uerr := d.ExecContext(ctx, `UPDATE plans SET
					title = COALESCE(?, title),
					summary = COALESCE(?, summary),
					goals = COALESCE(?, goals),
					acceptance_criteria = COALESCE(?, acceptance_criteria),
					acceptance_checks = COALESCE(?, acceptance_checks),
					scope = COALESCE(?, scope),
					version = version + 1, updated_at = ?
					WHERE id = ? AND version = ?`,
			u.Title, u.Summary, u.Goals, u.AcceptanceCriteria, u.AcceptanceChecks, u.Scope,
			now(), id, expectedVersion)
		if uerr != nil {
			return Plan{}, fmt.Errorf("store: update plan: %w", uerr)
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return Plan{}, domain.Errf(domain.PreconditionFailed, "plan %s version %d != expected %d", id, p.Version, expectedVersion)
		}
	}
	return d.GetPlan(ctx, id)
}

// DeletePlan removes a draft plan with all of its steps, deps and events.
// Only draft plans are deletable (else Conflict).
func (d *DB) DeletePlan(ctx context.Context, id string) error {
	p, err := d.GetPlan(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != "draft" {
		return domain.Errf(domain.Conflict, "plan %s is %s, only draft plans are deletable", id, p.Status)
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM step_deps WHERE step_id IN (SELECT id FROM steps WHERE plan_id = ?)
			OR depends_on_id IN (SELECT id FROM steps WHERE plan_id = ?)`, id, id); err != nil {
			return fmt.Errorf("store: delete plan deps: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM events WHERE plan_id = ?`, id); err != nil {
			return fmt.Errorf("store: delete plan events: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM steps WHERE plan_id = ?`, id); err != nil {
			return fmt.Errorf("store: delete plan steps: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM plans WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete plan: %w", err)
		}
		return nil
	})
}

// approvePlanTx is the tx body of ApprovePlan (no events beyond the note).
func (d *DB) ApprovePlan(ctx context.Context, id, approvedBy string) (Plan, error) {
	if approvedBy == "" {
		return Plan{}, domain.Errf(domain.Invalid, "approved_by is required")
	}
	p, err := d.GetPlan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if p.Status != "draft" {
		return Plan{}, domain.Errf(domain.Conflict, "plan must be draft to be approved (status %s)", p.Status)
	}
	if err := domain.PlanTransitions.Check(p.Status, "approved"); err != nil {
		return Plan{}, err
	}
	n := now()
	res, uerr := d.ExecContext(ctx, `UPDATE plans SET status='approved', approved_by=?, approved_at=?, version=version+1, updated_at=? WHERE id=? AND version=?`,
		approvedBy, n, n, id, p.Version)
	if uerr != nil {
		return Plan{}, fmt.Errorf("store: approve plan: %w", uerr)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return Plan{}, domain.Errf(domain.PreconditionFailed, "plan %s version %d != expected %d", id, p.Version, p.Version)
	}
	if _, err := d.AppendEvent(ctx, Event{PlanID: id, Type: "note", Payload: mustJSON(map[string]any{"kind": "plan_approved", "approved_by": approvedBy})}); err != nil {
		return Plan{}, err
	}
	return d.GetPlan(ctx, id)
}

// AssignPlan sets the orchestrator; only on an approved plan and only for an
// agent with role orchestrator.
func (d *DB) AssignPlan(ctx context.Context, planID, agentID string) (Plan, error) {
	p, err := d.GetPlan(ctx, planID)
	if err != nil {
		return Plan{}, err
	}
	if p.Status != "approved" {
		return Plan{}, domain.Errf(domain.Conflict, "plan must be approved to assign an orchestrator (status %s)", p.Status)
	}
	a, err := d.GetAgent(ctx, agentID)
	if err != nil {
		return Plan{}, err
	}
	if a.Role != "orchestrator" {
		return Plan{}, domain.Errf(domain.Unprocessable, "agent %s has role %s, want orchestrator", agentID, a.Role)
	}
	res, uerr := d.ExecContext(ctx, `UPDATE plans SET orchestrator_agent_id=?, version=version+1, updated_at=? WHERE id=? AND version=?`,
		agentID, now(), planID, p.Version)
	if uerr != nil {
		return Plan{}, fmt.Errorf("store: assign plan: %w", uerr)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return Plan{}, domain.Errf(domain.PreconditionFailed, "plan %s version %d != expected %d", planID, p.Version, p.Version)
	}
	if _, err := d.AppendEvent(ctx, Event{PlanID: planID, AgentID: agentID, Type: "note", Payload: mustJSON(map[string]any{"kind": "plan_assigned"})}); err != nil {
		return Plan{}, err
	}
	return d.GetPlan(ctx, planID)
}

// PlanGraph is a plan with its steps, dependency edges and readiness.
type PlanGraph struct {
	Plan  Plan        `json:"plan"`
	Steps []Step      `json:"steps"`
	Edges [][2]string `json:"edges"`
}

// PlanGraph returns the plan, its steps (deps filled, ready computed) and the
// dependency edges (step depends on first).
func (d *DB) PlanGraph(ctx context.Context, planID string) (PlanGraph, error) {
	p, err := d.GetPlan(ctx, planID)
	if err != nil {
		return PlanGraph{}, err
	}
	steps, err := d.ListSteps(ctx, planID)
	if err != nil {
		return PlanGraph{}, err
	}
	deps := map[string][]string{}
	status := map[string]string{}
	var edges [][2]string
	for _, s := range steps {
		status[s.ID] = s.Status
		for _, dep := range s.Deps {
			deps[s.ID] = append(deps[s.ID], dep)
			edges = append(edges, [2]string{s.ID, dep})
		}
	}
	readySet := map[string]bool{}
	for _, id := range domain.Ready(status, deps) {
		readySet[id] = true
	}
	for i := range steps {
		steps[i].Ready = readySet[steps[i].ID]
	}
	return PlanGraph{Plan: p, Steps: steps, Edges: edges}, nil
}
