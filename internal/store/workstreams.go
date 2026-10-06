package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

const workstreamCols = `id, title, status, priority, created_at, updated_at`

func scanWorkstream(r interface{ Scan(...any) error }) (Workstream, error) {
	var w Workstream
	if err := r.Scan(&w.ID, &w.Title, &w.Status, &w.Priority, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Workstream{}, err
	}
	return w, nil
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// CreateWorkstream inserts a workstream (status defaults to active).
func (d *DB) CreateWorkstream(ctx context.Context, w Workstream) (Workstream, error) {
	if w.Title == "" {
		return Workstream{}, domain.Errf(domain.Invalid, "title is required")
	}
	if w.Status == "" {
		w.Status = "active"
	}
	w.ID = ids.New()
	n := now()
	if w.Priority == 0 {
		w.Priority = 2
	}
	w.CreatedAt, w.UpdatedAt = n, n
	_, err := d.ExecContext(ctx, `INSERT INTO workstreams (id, title, status, priority, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, w.ID, w.Title, w.Status, w.Priority, n, n)
	if err != nil {
		return Workstream{}, fmt.Errorf("store: create workstream: %w", err)
	}
	return w, nil
}

// GetWorkstream returns one workstream or NotFound.
func (d *DB) GetWorkstream(ctx context.Context, id string) (Workstream, error) {
	w, err := scanWorkstream(d.QueryRowContext(ctx,
		`SELECT `+workstreamCols+` FROM workstreams WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Workstream{}, domain.Errf(domain.NotFound, "workstream %s not found", id)
	}
	return w, err
}

// ListWorkstreams returns all workstreams in creation (ULID) order.
func (d *DB) ListWorkstreams(ctx context.Context) ([]Workstream, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+workstreamCols+` FROM workstreams ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list workstreams: %w", err)
	}
	defer rows.Close()
	var out []Workstream
	for rows.Next() {
		w, err := scanWorkstream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkstreamUpdate carries optional fields for PATCH-style updates.
type WorkstreamUpdate struct {
	Title    *string
	Status   *string
	Priority *int
}

// UpdateWorkstream applies a patch; NotFound when the id is unknown.
func (d *DB) UpdateWorkstream(ctx context.Context, id string, u WorkstreamUpdate) (Workstream, error) {
	n := now()
	res, err := d.ExecContext(ctx, `UPDATE workstreams SET
		title  = COALESCE(?, title),
		status = COALESCE(?, status),
		priority = COALESCE(?, priority),
		updated_at = ?
		WHERE id = ?`, u.Title, u.Status, u.Priority, n, id)
	if err != nil {
		return Workstream{}, fmt.Errorf("store: update workstream: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return Workstream{}, domain.Errf(domain.NotFound, "workstream %s not found", id)
	}
	return d.GetWorkstream(ctx, id)
}

// DeleteWorkstream removes a workstream; Conflict when plans or tasks still
// reference it.
func (d *DB) DeleteWorkstream(ctx context.Context, id string) error {
	var n int
	if err := d.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM plans WHERE workstream_id = ?) + (SELECT COUNT(*) FROM tasks WHERE workstream_id = ?)`, id, id).Scan(&n); err != nil {
		return fmt.Errorf("store: delete workstream: %w", err)
	}
	if n > 0 {
		return domain.Errf(domain.Conflict, "workstream %s still has %d plan(s)/task(s)", id, n)
	}
	res, err := d.ExecContext(ctx, `DELETE FROM workstreams WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete workstream: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.NotFound, "workstream %s not found", id)
	}
	return nil
}
