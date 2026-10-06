package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

const stepCols = `id, plan_id, kind, title, description, acceptance_criteria, scope, status,
	assignee_agent_id, outcome, attempt, added_by, suggested_executor, version, created_at, updated_at`

func scanStep(r interface{ Scan(...any) error }) (Step, error) {
	var s Step
	var assignee, outcome sql.NullString
	if err := r.Scan(&s.ID, &s.PlanID, &s.Kind, &s.Title, &s.Description, &s.AcceptanceCriteria, &s.Scope, &s.Status,
		&assignee, &outcome, &s.Attempt, &s.AddedBy, &s.SuggestedExecutor, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return Step{}, err
	}
	if assignee.Valid {
		v := assignee.String
		s.AssigneeAgentID = &v
	}
	if outcome.Valid {
		v := outcome.String
		s.Outcome = &v
	}
	return s, nil
}

// insertStepTx writes a step row; deps is the (already validated) dep id list.
func insertStepTx(tx *sql.Tx, s Step, deps []string) error {
	n := now()
	if _, err := tx.Exec(`INSERT INTO steps (id, plan_id, kind, title, description, acceptance_criteria, scope, status,
		assignee_agent_id, outcome, attempt, added_by, suggested_executor, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?, 1, ?, ?)`,
		s.ID, s.PlanID, s.Kind, s.Title, s.Description, s.AcceptanceCriteria, s.Scope, s.Status,
		s.AddedBy, s.SuggestedExecutor, n, n); err != nil {
		return fmt.Errorf("store: insert step: %w", err)
	}
	for _, dep := range deps {
		if _, err := tx.Exec(`INSERT INTO step_deps (step_id, depends_on_id) VALUES (?, ?)`, s.ID, dep); err != nil {
			return fmt.Errorf("store: insert dep: %w", err)
		}
	}
	return nil
}

// loadDeps returns step -> dep ids for a whole plan (plus, via allSteps, the
// goal edges), so DAG checks and readiness see the full graph.
func (d *DB) loadDeps(ctx context.Context, planID string) (map[string][]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT d.step_id, d.depends_on_id FROM step_deps d
		JOIN steps s ON s.id = d.step_id WHERE s.plan_id = ?`, planID)
	if err != nil {
		return nil, fmt.Errorf("store: load deps: %w", err)
	}
	defer rows.Close()
	deps := map[string][]string{}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		deps[a] = append(deps[a], b)
	}
	return deps, rows.Err()
}

// loadDepsTx is loadDeps inside a transaction.
func loadDepsTx(tx *sql.Tx, planID string) (map[string][]string, error) {
	rows, err := tx.Query(`SELECT d.step_id, d.depends_on_id FROM step_deps d
		JOIN steps s ON s.id = d.step_id WHERE s.plan_id = ?`, planID)
	if err != nil {
		return nil, fmt.Errorf("store: load deps: %w", err)
	}
	defer rows.Close()
	deps := map[string][]string{}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		deps[a] = append(deps[a], b)
	}
	return deps, rows.Err()
}

// CreateStep adds a step to a plan. The plan must be draft, approved or
// running; kind goal is reserved (the plan creates it). Scope must be narrower
// than the plan scope; deps must be same-plan steps and must not introduce a
// cycle. After insert, the goal's dependencies are recomputed to the current
// leaf steps (steps nothing depends on) so the DAG always ends in the goal.
func (d *DB) CreateStep(ctx context.Context, s Step) (Step, error) {
	if s.AddedBy == "" || (s.AddedBy != "planner" && s.AddedBy != "orchestrator" && s.AddedBy != "user") {
		return Step{}, domain.Errf(domain.Invalid, "added_by must be planner|orchestrator|user")
	}
	if s.Kind == "goal" {
		return Step{}, domain.Errf(domain.Invalid, "kind goal is created by the plan itself")
	}
	if s.Kind != "task" && s.Kind != "checkpoint" {
		return Step{}, domain.Errf(domain.Invalid, "kind must be task or checkpoint")
	}
	if s.Title == "" {
		return Step{}, domain.Errf(domain.Invalid, "title is required")
	}
	if s.Scope == "" {
		s.Scope = "{}"
	}
	p, err := d.GetPlan(ctx, s.PlanID)
	if err != nil {
		return Step{}, err
	}
	if p.Status != "draft" && p.Status != "approved" && p.Status != "running" {
		return Step{}, domain.Errf(domain.Conflict, "plan %s is %s, steps need draft|approved|running", p.ID, p.Status)
	}
	// scope narrowing
	if s.Scope != "{}" && s.Scope != "" {
		var ps, ss domain.Scope
		if err := json2(p.Scope, &ps); err != nil {
			return Step{}, fmt.Errorf("store: plan scope: %w", err)
		}
		if err := json2(s.Scope, &ss); err != nil {
			return Step{}, domain.Errf(domain.Invalid, "step scope must be a JSON scope object")
		}
		if err := domain.Narrower(ps, ss); err != nil {
			return Step{}, err
		}
	}
	// dep validation: same plan + no cycle
	deps := dedupe(s.Deps)
	if len(deps) > 0 {
		deps, err = d.validateDeps(ctx, p, s.ID, deps)
		if err != nil {
			return Step{}, err
		}
	}
	s.ID = ids.New()
	s.Status = "pending"
	s.Version = 1
	n := now()
	s.CreatedAt, s.UpdatedAt = n, n
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := insertStepTx(tx, s, deps); err != nil {
			return err
		}
		// recompute goal deps to current leaves so the DAG ends in the goal
		if err := recomputeGoalDepsTx(tx, p); err != nil {
			return err
		}
		_, err := AppendEventTx(tx, Event{PlanID: p.ID, StepID: s.ID, Type: "note",
			Payload: mustJSON(map[string]any{"kind": "step_created", "title": s.Title})})
		return err
	})
	if err != nil {
		return Step{}, err
	}
	return d.GetStep(ctx, s.ID)
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// validateDeps checks every dep exists on the same plan and that adding
// step->deps introduces no cycle.
func (d *DB) validateDeps(ctx context.Context, p Plan, stepID string, deps []string) ([]string, error) {
	for _, dep := range deps {
		var plan string
		if err := d.QueryRowContext(ctx, `SELECT plan_id FROM steps WHERE id = ?`, dep).Scan(&plan); err == sql.ErrNoRows {
			return nil, domain.Errf(domain.Invalid, "dep %s not found", dep)
		} else if err != nil {
			return nil, fmt.Errorf("store: validate deps: %w", err)
		} else if plan != p.ID {
			return nil, domain.Errf(domain.Invalid, "dep %s belongs to another plan", dep)
		}
	}
	existing, err := d.loadDeps(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	full := map[string][]string{}
	for k, v := range existing {
		full[k] = v
	}
	full[stepID] = deps
	for _, dep := range deps {
		if domain.WouldCycle(full, stepID, dep) {
			return nil, domain.Errf(domain.Conflict, "dependency %s would create a cycle", dep)
		}
	}
	return deps, nil
}

// recomputeGoalDepsTx resets the goal step's dependencies to the current leaf
// steps of the plan (steps that no other step depends on, excluding the goal
// itself). Leaf steps feed the goal.
func recomputeGoalDepsTx(tx *sql.Tx, p Plan) error {
	if p.GoalStepID == nil {
		return nil
	}
	goalID := *p.GoalStepID
	rows, err := tx.Query(`
		SELECT s.id FROM steps s
		WHERE s.plan_id = ? AND s.kind != 'goal' AND s.status != 'cancelled'
		AND NOT EXISTS (SELECT 1 FROM step_deps d WHERE d.depends_on_id = s.id)`, p.ID)
	if err != nil {
		return fmt.Errorf("store: leaf steps: %w", err)
	}
	defer rows.Close()
	var leaves []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		leaves = append(leaves, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM step_deps WHERE step_id = ?`, goalID); err != nil {
		return fmt.Errorf("store: reset goal deps: %w", err)
	}
	for _, l := range leaves {
		if _, err := tx.Exec(`INSERT INTO step_deps (step_id, depends_on_id) VALUES (?, ?)`, goalID, l); err != nil {
			return fmt.Errorf("store: goal dep: %w", err)
		}
	}
	return nil
}

// GetStep returns one step with its deps filled or NotFound.
func (d *DB) GetStep(ctx context.Context, id string) (Step, error) {
	s, err := scanStep(d.QueryRowContext(ctx, `SELECT `+stepCols+` FROM steps WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Step{}, domain.Errf(domain.NotFound, "step %s not found", id)
	}
	if err != nil {
		return Step{}, err
	}
	if err := d.fillDepsAndReady(ctx, &s, nil); err != nil {
		return Step{}, err
	}
	return s, nil
}

// fillDepsAndReady loads deps and computes the derived ready flag. When a
// full graph is given (graph/next paths) it is reused instead of re-queried.
func (d *DB) fillDepsAndReady(ctx context.Context, s *Step, graph *depsGraph) error {
	var deps map[string][]string
	var err error
	if graph != nil {
		deps = graph.deps
	} else {
		deps, err = d.loadDeps(ctx, s.PlanID)
		if err != nil {
			return err
		}
	}
	s.Deps = deps[s.ID]
	d.applyReady(s, deps, graph)
	return nil
}

// depsGraph bundles the shared computation across steps of one plan.
type depsGraph struct {
	deps   map[string][]string
	status map[string]string
	ready  map[string]bool
}

func (d *DB) applyReady(s *Step, deps map[string][]string, g *depsGraph) {
	var ready bool
	if s.Status == "pending" {
		ok := true
		for _, dep := range deps[s.ID] {
			if st, has := d.stepStatus(s.PlanID, dep, g); !has || st != "done" {
				ok = false
				break
			}
		}
		ready = ok
	}
	s.Ready = ready
}

// stepStatus resolves a step status, using the graph cache when present.
func (d *DB) stepStatus(planID, stepID string, g *depsGraph) (string, bool) {
	if g != nil {
		st, ok := g.status[stepID]
		return st, ok
	}
	var st string
	if err := d.QueryRowContext(context.Background(), `SELECT status FROM steps WHERE id = ?`, stepID).Scan(&st); err != nil {
		return "", false
	}
	return st, true
}

// buildGraph loads statuses+deps for a plan once.
func (d *DB) buildGraph(ctx context.Context, planID string) (*depsGraph, error) {
	deps, err := d.loadDeps(ctx, planID)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT id, status FROM steps WHERE plan_id = ?`, planID)
	if err != nil {
		return nil, fmt.Errorf("store: statuses: %w", err)
	}
	defer rows.Close()
	status := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		status[id] = st
	}
	return &depsGraph{deps: deps, status: status}, rows.Err()
}

// StepFilter narrows ListSteps.
type StepFilter struct {
	PlanID string
	Status string
}

// ListSteps returns plan steps in creation order, deps filled and ready set.
func (d *DB) ListSteps(ctx context.Context, planID string) ([]Step, error) {
	return d.listSteps(ctx, StepFilter{PlanID: planID})
}

func (d *DB) listSteps(ctx context.Context, f StepFilter) ([]Step, error) {
	g, err := d.buildGraph(ctx, f.PlanID)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + stepCols + ` FROM steps WHERE 1=1`
	var args []any
	if f.PlanID != "" {
		q += ` AND plan_id = ?`
		args = append(args, f.PlanID)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY id`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list steps: %w", err)
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		s, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		s.Deps = g.deps[s.ID]
		if s.Status == "pending" {
			ok := true
			for _, dep := range s.Deps {
				if g.status[dep] != "done" && g.status[dep] != "cancelled" {
					ok = false
					break
				}
			}
			s.Ready = ok
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StepUpdate is a PATCH-style step change (pending steps only; goal needs an
// answered-interruption approval_ref).
type StepUpdate struct {
	Title              *string
	Description        *string
	AcceptanceCriteria *string
	Scope              *string
	SuggestedExecutor  *string
}

// UpdateStep applies the patch guarded on the expected version. Only pending
// steps are editable (else Conflict); goal steps require approval_ref (an
// answered interruption on the step's plan) or 428.
func (d *DB) UpdateStep(ctx context.Context, id string, expectedVersion int, u StepUpdate, approvalRef string) (Step, error) {
	s, err := d.GetStep(ctx, id)
	if err != nil {
		return Step{}, err
	}
	if s.Status != "pending" {
		return Step{}, domain.Errf(domain.Conflict, "step %s is %s, only pending steps are editable", id, s.Status)
	}
	if s.Kind == "goal" {
		if err := requirePlanApprovalRef(ctx, d, s.PlanID, approvalRef); err != nil {
			return Step{}, err
		}
	}
	res, err := d.ExecContext(ctx, `UPDATE steps SET
		title = COALESCE(?, title),
		description = COALESCE(?, description),
		acceptance_criteria = COALESCE(?, acceptance_criteria),
		scope = COALESCE(?, scope),
		suggested_executor = COALESCE(?, suggested_executor),
		version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`,
		u.Title, u.Description, u.AcceptanceCriteria, u.Scope, u.SuggestedExecutor, now(), id, expectedVersion)
	if err != nil {
		return Step{}, fmt.Errorf("store: update step: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return Step{}, domain.Errf(domain.PreconditionFailed, "step %s version %d != expected %d", id, s.Version, expectedVersion)
	}
	return d.GetStep(ctx, id)
}

// requirePlanApprovalRef enforces: approvalRef must be an answered interruption
// with plan_id = planID (direct SQL, per plan decision).
func requirePlanApprovalRef(ctx context.Context, d *DB, planID, approvalRef string) error {
	var one int
	err := d.QueryRowContext(ctx, `SELECT 1 FROM interruptions WHERE id = ? AND status = 'answered' AND plan_id = ?`, approvalRef, planID).Scan(&one)
	if err != nil {
		return domain.Errf(domain.PreconditionRequired,
			"goal change requires approval_ref of an answered interruption on plan %s", planID)
	}
	return nil
}

// AddDep adds a dependency edge. Same editing rule as UpdateStep (pending
// only); goal steps never gain deps this way (leaf recomputation owns the
// goal's edges); cycles and duplicate edges are Conflicts; a stale
// expectedVersion is a PreconditionFailed (412).
func (d *DB) AddDep(ctx context.Context, stepID, dependsOn string, expectedVersion int) error {
	return d.editDep(ctx, stepID, dependsOn, expectedVersion, true)
}

// RemoveDep deletes a dependency edge (same rules as AddDep).
func (d *DB) RemoveDep(ctx context.Context, stepID, dependsOn string, expectedVersion int) error {
	return d.editDep(ctx, stepID, dependsOn, expectedVersion, false)
}

func (d *DB) editDep(ctx context.Context, stepID, dependsOn string, expectedVersion int, add bool) error {
	s, err := d.GetStep(ctx, stepID)
	if err != nil {
		return err
	}
	if s.Version != expectedVersion {
		return domain.Errf(domain.PreconditionFailed, "step %s version %d != expected %d", stepID, s.Version, expectedVersion)
	}
	if s.Kind == "goal" {
		return domain.Errf(domain.Conflict, "goal dependencies are managed by the store (leaf feeding), not editable")
	}
	if s.Status != "pending" {
		return domain.Errf(domain.Conflict, "step %s is %s, only pending steps are editable", stepID, s.Status)
	}
	dep, err := d.GetStep(ctx, dependsOn)
	if err != nil {
		return domain.Errf(domain.Invalid, "depends_on %s not found", dependsOn)
	}
	if dep.PlanID != s.PlanID {
		return domain.Errf(domain.Invalid, "depends_on %s belongs to another plan", dependsOn)
	}
	if add {
		for _, existing := range s.Deps {
			if existing == dependsOn {
				return domain.Errf(domain.Conflict, "step %s already depends on %s", stepID, dependsOn)
			}
		}
		deps, err := d.loadDeps(ctx, s.PlanID)
		if err != nil {
			return err
		}
		full := map[string][]string{}
		for k, v := range deps {
			full[k] = v
		}
		full[stepID] = append(dedupe(full[stepID]), dependsOn)
		if domain.WouldCycle(full, stepID, dependsOn) {
			return domain.Errf(domain.Conflict, "dependency %s would create a cycle", dependsOn)
		}
		if _, err := d.ExecContext(ctx, `INSERT INTO step_deps (step_id, depends_on_id) VALUES (?, ?)`, stepID, dependsOn); err != nil {
			return fmt.Errorf("store: add dep: %w", err)
		}
		// the graph changed; recompute goal leaves
		p, err := d.GetPlan(ctx, s.PlanID)
		if err != nil {
			return err
		}
		return d.WithTx(ctx, func(tx *sql.Tx) error { return recomputeGoalDepsTx(tx, p) })
	}
	res, err := d.ExecContext(ctx, `DELETE FROM step_deps WHERE step_id = ? AND depends_on_id = ?`, stepID, dependsOn)
	if err != nil {
		return fmt.Errorf("store: remove dep: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.NotFound, "step %s does not depend on %s", stepID, dependsOn)
	}
	p, err := d.GetPlan(ctx, s.PlanID)
	if err != nil {
		return err
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error { return recomputeGoalDepsTx(tx, p) })
}

// DeleteStep removes a pending step. Goal steps are never deletable (409),
// even with an approval_ref. Deleting re-feeds the goal with the surviving
// leaves.
func (d *DB) DeleteStep(ctx context.Context, id, approvalRef string) error {
	s, err := d.GetStep(ctx, id)
	if err != nil {
		return err
	}
	if s.Kind == "goal" {
		return domain.Errf(domain.Conflict, "the goal step can never be deleted")
	}
	if s.Status != "pending" {
		return domain.Errf(domain.Conflict, "step %s is %s, only pending steps are deletable", id, s.Status)
	}
	p, err := d.GetPlan(ctx, s.PlanID)
	if err != nil {
		return err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM step_deps WHERE step_id = ? OR depends_on_id = ?`, id, id); err != nil {
			return fmt.Errorf("store: delete step deps: %w", err)
		}
		// events FK-reference the step; a pending step can only carry the
		// step_created note, which goes with it (no history is lost for
		// claimed work: those steps are not deletable).
		if _, err := tx.Exec(`DELETE FROM events WHERE step_id = ?`, id); err != nil {
			return fmt.Errorf("store: delete step events: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM steps WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete step: %w", err)
		}
		return recomputeGoalDepsTx(tx, p)
	})
	return err
}

// NextSteps returns the ready (claimable) steps of a plan.
func (d *DB) NextSteps(ctx context.Context, planID string) ([]Step, error) {
	if _, err := d.GetPlan(ctx, planID); err != nil {
		return nil, err
	}
	all, err := d.listSteps(ctx, StepFilter{PlanID: planID})
	if err != nil {
		return nil, err
	}
	var out []Step
	for _, s := range all {
		if s.Ready {
			out = append(out, s)
		}
	}
	return out, nil
}

// ClaimStep claims a pending+ready step: pending -> in_progress, assignee set,
// attempt+1, version bumped, `started` event emitted. A second claim (by
// anyone) is a Conflict; claiming a non-ready step is a Conflict.
func (d *DB) ClaimStep(ctx context.Context, id, assignee string, expectedVersion int) (Step, error) {
	s, err := d.GetStep(ctx, id)
	if err != nil {
		return Step{}, err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		return claimWorkTx(tx, stepsTable, id, s.PlanID, assignee, expectedVersion, func() error {
			return checkStepReady(tx, s)
		})
	})
	if err != nil {
		return Step{}, err
	}
	return d.GetStep(ctx, id)
}

// checkStepReady vetoes the claim when a dependency is not done (re-read
// inside the tx).
func checkStepReady(tx *sql.Tx, s Step) error {
	deps, err := loadDepsTx(tx, s.PlanID)
	if err != nil {
		return err
	}
	for _, dep := range deps[s.ID] {
		var st string
		if err := tx.QueryRow(`SELECT status FROM steps WHERE id = ?`, dep).Scan(&st); err != nil {
			return fmt.Errorf("store: read dep: %w", err)
		}
		if st != "done" && st != "cancelled" {
			return domain.Errf(domain.Conflict, "step %s is not ready: dep %s is %s", s.ID, dep, st)
		}
	}
	return nil
}

// FinishStep finishes an in_progress step with {status, outcome}. done
// requires non-empty outcome.evidence. Finishing the goal step completes the
// plan — but only when every other step is done/cancelled, else Conflict.
func (d *DB) FinishStep(ctx context.Context, id string, expectedVersion int, status, outcome string) (Step, error) {
	s, err := d.GetStep(ctx, id)
	if err != nil {
		return Step{}, err
	}
	var onComplete func(*sql.Tx) error
	if s.Kind == "goal" {
		onComplete = func(tx *sql.Tx) error { return completePlanTx(tx, s.PlanID) }
	}
	var version int
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		version, err = finishWorkTx(tx, stepsTable, id, s.PlanID, expectedVersion, status, outcome, onComplete)
		return err
	})
	if err != nil {
		return Step{}, err
	}
	got, err := d.GetStep(ctx, id)
	if err != nil {
		return Step{}, err
	}
	got.Version = version
	return got, nil
}

// completePlanTx sets the plan done when all of its steps are done or
// cancelled; otherwise Conflict.
func completePlanTx(tx *sql.Tx, planID string) error {
	var total, ok int
	if err := tx.QueryRow(`SELECT COUNT(*),
			SUM(CASE WHEN status IN ('done','cancelled') THEN 1 ELSE 0 END)
			FROM steps WHERE plan_id = ?`, planID).Scan(&total, &ok); err != nil {
		return fmt.Errorf("store: complete plan: %w", err)
	}
	if ok != total {
		return domain.Errf(domain.Conflict, "cannot finish plan: %d of %d steps not done/cancelled", total-ok, total)
	}
	if _, err := tx.Exec(`UPDATE plans SET status='done', version=version+1, updated_at=? WHERE id=?`, now(), planID); err != nil {
		return fmt.Errorf("store: complete plan: %w", err)
	}
	_, err := AppendEventTx(tx, Event{PlanID: planID, Type: "finished", Payload: mustJSON(map[string]any{"kind": "plan_done"})})
	return err
}

// CancelStep moves a pending step to cancelled (guarded on version).
func (d *DB) CancelStep(ctx context.Context, id string, expectedVersion int) (Step, error) {
	s, err := d.GetStep(ctx, id)
	if err != nil {
		return Step{}, err
	}
	res, err := d.ExecContext(ctx, `UPDATE steps SET status='cancelled', version=version+1, updated_at=? WHERE id=? AND version=? AND status='pending'`,
		now(), id, expectedVersion)
	if err != nil {
		return Step{}, fmt.Errorf("store: cancel step: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return Step{}, domain.Errf(domain.PreconditionFailed, "step %s version %d != expected %d", id, s.Version, expectedVersion)
	}
	p, err := d.GetPlan(ctx, s.PlanID)
	if err != nil {
		return Step{}, err
	}
	if err := d.WithTx(ctx, func(tx *sql.Tx) error { return recomputeGoalDepsTx(tx, p) }); err != nil {
		return Step{}, err
	}
	return d.GetStep(ctx, id)
}

// RestartStep returns an interrupted/failed/blocked step to pending so a
// cleared executor can retry (transitions: interrupted|failed|blocked ->
// pending).
func (d *DB) RestartStep(ctx context.Context, id string) (Step, error) {
	s, err := d.GetStep(ctx, id)
	if err != nil {
		return Step{}, err
	}
	if err := domain.StepTransitions.Check(s.Status, "pending"); err != nil {
		return Step{}, err
	}
	if _, err := d.ExecContext(ctx, `UPDATE steps SET status='pending', version=version+1, updated_at=? WHERE id=?`, now(), id); err != nil {
		return Step{}, fmt.Errorf("store: restart step: %w", err)
	}
	return d.GetStep(ctx, id)
}

// json2 is a small alias so scope parsing reads naturally in this file.
func json2(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}
