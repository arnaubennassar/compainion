package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

const agentCols = `id, role, parent_id, harness, handle, status, workstream_id, last_heartbeat_at, created_at, updated_at`

func scanAgent(r interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var parent, ws sql.NullString
	var hb sql.NullString
	if err := r.Scan(&a.ID, &a.Role, &parent, &a.Harness, &a.Handle, &a.Status, &ws, &hb, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Agent{}, err
	}
	a.ParentID, a.WorkstreamID = parent.String, ws.String
	if hb.Valid {
		s := hb.String
		a.LastHeartbeatAt = &s
	}
	return a, nil
}

// RegisterAgent inserts an agent; when a.ID is supplied and already exists the
// existing row is returned unchanged (idempotent registration).
func (d *DB) RegisterAgent(ctx context.Context, a Agent) (Agent, error) {
	if a.ID != "" {
		if existing, err := d.GetAgent(ctx, a.ID); err == nil {
			return existing, nil
		} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
			return Agent{}, err
		}
	}
	if a.ID == "" {
		a.ID = ids.New()
	}
	if a.Role == "" {
		return Agent{}, domain.Errf(domain.Invalid, "role is required")
	}
	if a.Status == "" {
		a.Status = "starting"
	}
	n := now()
	_, err := d.ExecContext(ctx, `INSERT INTO agents (id, role, parent_id, harness, handle, status, workstream_id, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		a.ID, a.Role, nullStr(a.ParentID), a.Harness, a.Handle, a.Status, nullStr(a.WorkstreamID), n, n)
	if err != nil {
		return Agent{}, fmt.Errorf("store: register agent: %w", err)
	}
	return d.GetAgent(ctx, a.ID)
}

// GetAgent returns one agent or NotFound.
func (d *DB) GetAgent(ctx context.Context, id string) (Agent, error) {
	a, err := scanAgent(d.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Agent{}, domain.Errf(domain.NotFound, "agent %s not found", id)
	}
	return a, err
}

// AgentFilter narrows ListAgents.
type AgentFilter struct {
	Role         string
	Status       string
	ParentID     string
	WorkstreamID string
}

// ListAgents returns agents matching the filter in creation order.
func (d *DB) ListAgents(ctx context.Context, f AgentFilter) ([]Agent, error) {
	q := `SELECT ` + agentCols + ` FROM agents WHERE 1=1`
	var args []any
	if f.Role != "" {
		q += ` AND role = ?`
		args = append(args, f.Role)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.ParentID != "" {
		q += ` AND parent_id = ?`
		args = append(args, f.ParentID)
	}
	if f.WorkstreamID != "" {
		q += ` AND workstream_id = ?`
		args = append(args, f.WorkstreamID)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list agents: %w", err)
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AgentHeartbeat bumps last_heartbeat_at. It NEVER changes status: the backend
// does not derive liveness (agents self-report or the companion updates them).
func (d *DB) AgentHeartbeat(ctx context.Context, id string) error {
	res, err := d.ExecContext(ctx, `UPDATE agents SET last_heartbeat_at = ?, updated_at = ? WHERE id = ?`, now(), now(), id)
	if err != nil {
		return fmt.Errorf("store: agent heartbeat: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.NotFound, "agent %s not found", id)
	}
	return nil
}

// UpdateAgentStatus applies the agent status state machine (domain.AgentTransitions).
func (d *DB) UpdateAgentStatus(ctx context.Context, id, status string) error {
	a, err := d.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	if err := domain.AgentTransitions.Check(a.Status, status); err != nil {
		return err
	}
	res, err := d.ExecContext(ctx, `UPDATE agents SET status = ?, updated_at = ? WHERE id = ?`, status, now(), id)
	if err != nil {
		return fmt.Errorf("store: update agent status: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.NotFound, "agent %s not found", id)
	}
	return nil
}

// agentSetStatus sets status bypassing the state machine (companion-driven
// recovery paths); used internally by UpdateAgentPatch.
func (d *DB) agentSetStatus(ctx context.Context, id, status string) error {
	res, err := d.ExecContext(ctx, `UPDATE agents SET status = ?, updated_at = ? WHERE id = ?`, status, now(), id)
	if err != nil {
		return fmt.Errorf("store: set agent status: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.NotFound, "agent %s not found", id)
	}
	return nil
}

// UpdateAgent applies a PATCH-style update. When Status is set it goes through
// the state machine unless Force is set.
type AgentUpdate struct {
	Status *string
	Force  bool
}

// UpdateAgent applies the patch.
func (d *DB) UpdateAgent(ctx context.Context, id string, u AgentUpdate) (Agent, error) {
	if u.Status != nil {
		if u.Force {
			if err := d.agentSetStatus(ctx, id, strings.ToLower(*u.Status)); err != nil {
				return Agent{}, err
			}
		} else if err := d.UpdateAgentStatus(ctx, id, *u.Status); err != nil {
			return Agent{}, err
		}
	}
	return d.GetAgent(ctx, id)
}
