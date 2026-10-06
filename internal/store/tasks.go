package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

const taskCols = `id, workstream_id, requested_by, title, description, acceptance_criteria, scope, status,
	assignee_agent_id, outcome, attempt, added_by, version, created_at, updated_at`

func scanTask(r interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var assignee, outcome sql.NullString
	if err := r.Scan(&t.ID, &t.WorkstreamID, &t.RequestedBy, &t.Title, &t.Description, &t.AcceptanceCriteria, &t.Scope, &t.Status,
		&assignee, &outcome, &t.Attempt, &t.AddedBy, &t.Version, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Task{}, err
	}
	if assignee.Valid {
		v := assignee.String
		t.AssigneeAgentID = &v
	}
	if outcome.Valid {
		v := outcome.String
		t.Outcome = &v
	}
	return t, nil
}

// CreateTask inserts a standalone task (same lifecycle as steps minus the
// plan/dependency machinery).
func (d *DB) CreateTask(ctx context.Context, t Task) (Task, error) {
	if t.WorkstreamID == "" {
		return Task{}, domain.Errf(domain.Invalid, "workstream_id is required")
	}
	if _, err := d.GetWorkstream(ctx, t.WorkstreamID); err != nil {
		return Task{}, err
	}
	if t.Title == "" {
		return Task{}, domain.Errf(domain.Invalid, "title is required")
	}
	if t.AddedBy == "" {
		return Task{}, domain.Errf(domain.Invalid, "added_by is required")
	}
	if t.Scope == "" {
		t.Scope = "{}"
	}
	t.ID = ids.New()
	t.Status = "pending"
	t.Version = 1
	n := now()
	t.CreatedAt, t.UpdatedAt = n, n
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO tasks (id, workstream_id, requested_by, title, description, acceptance_criteria, scope, status,
			assignee_agent_id, outcome, attempt, added_by, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', NULL, NULL, 0, ?, 1, ?, ?)`,
			t.ID, t.WorkstreamID, t.RequestedBy, t.Title, t.Description, t.AcceptanceCriteria, t.Scope,
			t.AddedBy, n, n); err != nil {
			return fmt.Errorf("store: create task: %w", err)
		}
		_, err := AppendEventTx(tx, Event{AgentID: t.RequestedBy, TaskID: t.ID, Type: "note",
			Payload: mustJSON(map[string]any{"kind": "task_created", "title": t.Title})})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return d.GetTask(ctx, t.ID)
}

// GetTask returns one task or NotFound.
func (d *DB) GetTask(ctx context.Context, id string) (Task, error) {
	t, err := scanTask(d.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Task{}, domain.Errf(domain.NotFound, "task %s not found", id)
	}
	return t, err
}

// TaskFilter narrows ListTasks.
type TaskFilter struct {
	WorkstreamID string
	Status       string
}

// ListTasks returns tasks matching the filter in creation order.
func (d *DB) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE 1=1`
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
		return nil, fmt.Errorf("store: list tasks: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClaimTask claims a pending task: pending -> in_progress, assignee set,
// attempt+1, version bumped, `started` event emitted (shared claimWorkTx).
func (d *DB) ClaimTask(ctx context.Context, id, assignee string, expectedVersion int) (Task, error) {
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		return claimWorkTx(tx, tasksTable, id, "", assignee, expectedVersion, nil)
	})
	if err != nil {
		return Task{}, err
	}
	return d.GetTask(ctx, id)
}

// FinishTask finishes an in_progress task with {status, outcome} using the
// shared finishWorkTx (done requires non-empty outcome.evidence).
func (d *DB) FinishTask(ctx context.Context, id string, expectedVersion int, status, outcome string) (Task, error) {
	var version int
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		version, err = finishWorkTx(tx, tasksTable, id, "", expectedVersion, status, outcome, nil)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	got, err := d.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	got.Version = version
	return got, nil
}

// CancelTask moves a pending task to cancelled (guarded on version).
func (d *DB) CancelTask(ctx context.Context, id string, expectedVersion int) (Task, error) {
	t, err := d.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	res, err := d.ExecContext(ctx, `UPDATE tasks SET status='cancelled', version=version+1, updated_at=? WHERE id=? AND version=? AND status='pending'`,
		now(), id, expectedVersion)
	if err != nil {
		return Task{}, fmt.Errorf("store: cancel task: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return Task{}, domain.Errf(domain.PreconditionFailed, "task %s version %d != expected %d", id, t.Version, expectedVersion)
	}
	return d.GetTask(ctx, id)
}

// RestartTask returns an interrupted/failed/blocked task to pending.
func (d *DB) RestartTask(ctx context.Context, id string) (Task, error) {
	t, err := d.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if err := domain.StepTransitions.Check(t.Status, "pending"); err != nil {
		return Task{}, err
	}
	if _, err := d.ExecContext(ctx, `UPDATE tasks SET status='pending', version=version+1, updated_at=? WHERE id=?`, now(), id); err != nil {
		return Task{}, fmt.Errorf("store: restart task: %w", err)
	}
	return d.GetTask(ctx, id)
}
