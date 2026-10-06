package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/arnaubennassar/compainion/internal/ids"
)

// AppendEventTx appends an event inside the caller's transaction so lifecycle
// events are atomic with the state change that caused them. Exported so later
// repositories (plans, steps, tasks, interruptions) reuse it. Sets the ULID id
// and `at` timestamp when empty.
func AppendEventTx(tx *sql.Tx, ev Event) (Event, error) {
	if ev.ID == "" {
		ev.ID = ids.New()
	}
	if ev.At == "" {
		ev.At = now()
	}
	if ev.Payload == "" {
		ev.Payload = "{}"
	}
	n := now()
	_, err := tx.Exec(`INSERT INTO events (id, agent_id, plan_id, step_id, task_id, type, payload, at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, nullStr(ev.AgentID), nullStr(ev.PlanID), nullStr(ev.StepID), nullStr(ev.TaskID),
		ev.Type, ev.Payload, ev.At, n, n)
	if err != nil {
		return Event{}, fmt.Errorf("store: append event: %w", err)
	}
	return ev, nil
}

// AppendEvent appends an event on its own transaction.
func (d *DB) AppendEvent(ctx context.Context, ev Event) (Event, error) {
	var out Event
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = AppendEventTx(tx, ev)
		return err
	})
	return out, err
}

// EventFilter narrows ListEvents; Since is an exclusive event-id cursor.
type EventFilter struct {
	AgentID string
	PlanID  string
	StepID  string
	TaskID  string
	Types   []string
	Since   string
	Limit   int
}

// ListEvents returns events in ascending ULID order (cursor order). Limit
// defaults to 100 and is capped at 500.
func (d *DB) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	q := `SELECT id, agent_id, plan_id, step_id, task_id, type, payload, at FROM events WHERE 1=1`
	var args []any
	if f.AgentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, f.AgentID)
	}
	if f.PlanID != "" {
		q += ` AND plan_id = ?`
		args = append(args, f.PlanID)
	}
	if f.StepID != "" {
		q += ` AND step_id = ?`
		args = append(args, f.StepID)
	}
	if f.TaskID != "" {
		q += ` AND task_id = ?`
		args = append(args, f.TaskID)
	}
	if len(f.Types) > 0 {
		q += ` AND type IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.Types)), ",") + `)`
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.Since != "" {
		q += ` AND id > ?`
		args = append(args, f.Since)
	}
	q += ` ORDER BY id`
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q += fmt.Sprintf(` LIMIT %d`, limit)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var agent, plan, step, task sql.NullString
		if err := rows.Scan(&ev.ID, &agent, &plan, &step, &task, &ev.Type, &ev.Payload, &ev.At); err != nil {
			return nil, err
		}
		ev.AgentID, ev.PlanID, ev.StepID, ev.TaskID = agent.String, plan.String, step.String, task.String
		out = append(out, ev)
	}
	return out, rows.Err()
}

// NextEventCursor returns the id of the last event in a listing (the opaque
// cursor handed back to API clients).
func NextEventCursor(evs []Event) string {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].ID
}