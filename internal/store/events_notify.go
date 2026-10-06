package store

import (
	"context"
	"database/sql"
)

// Streaming/notification helpers over the events table. Both pollers (SSE in
// internal/api and the dispatcher in internal/notify) use these to walk the
// append-only log.

// LatestEventID returns the id of the newest event, or "" when the log is
// empty. Used as the starting cursor for "start from now".
func LatestEventID(ctx context.Context, d *DB) (string, error) {
	var id string
	err := d.QueryRowContext(ctx, `SELECT id FROM events ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// EventWorkstreamID resolves the workstream an event belongs to: events carry
// agent/plan/step/task ids, and the workstream comes from the agent's
// workstream_id, the plan's or task's workstream_id, or (for steps) the
// owning plan's. Empty when the event has no resolvable workstream.
func EventWorkstreamID(ctx context.Context, d *DB, ev Event) (string, error) {
	var ws string
	err := d.QueryRowContext(ctx, `
		SELECT COALESCE(a.workstream_id, p.workstream_id, t.workstream_id, sp.workstream_id, '')
		FROM events e
		LEFT JOIN agents a  ON a.id  = e.agent_id
		LEFT JOIN plans  p  ON p.id  = e.plan_id
		LEFT JOIN tasks  t  ON t.id  = e.task_id
		LEFT JOIN steps  s  ON s.id  = e.step_id
		LEFT JOIN plans  sp ON sp.id = s.plan_id
		WHERE e.id = ?`, ev.ID).Scan(&ws)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return ws, err
}