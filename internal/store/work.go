package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arnaubennassar/compainion/internal/domain"
)

// workTable parameterises the shared claim/finish machinery over the two
// work-item tables (steps and tasks) so the state machine, attempt bumping,
// version guarding and lifecycle events are written once (DRY).
type workTable struct {
	name   string // "steps" or "tasks"
	hasDep bool   // steps carry a deps table; tasks do not
}

var (
	stepsTable = workTable{name: "steps", hasDep: true}
	tasksTable = workTable{name: "tasks"}
)

// validateOutcome checks a finish outcome payload: it must be a JSON object
// carrying a non-empty "result"; finishing "done" additionally requires a
// non-empty "evidence".
func validateOutcome(status, outcome string) error {
	var o map[string]any
	if err := json.Unmarshal([]byte(outcome), &o); err != nil || o == nil {
		return domain.Errf(domain.Unprocessable, "outcome must be a JSON object with a result")
	}
	res, _ := o["result"].(string)
	if strings.TrimSpace(res) == "" {
		return domain.Errf(domain.Unprocessable, "outcome.result is required")
	}
	if status == "done" {
		switch e := o["evidence"].(type) {
		case nil:
			return domain.Errf(domain.Unprocessable, "finishing done requires non-empty outcome.evidence")
		case []any:
			if len(e) == 0 {
				return domain.Errf(domain.Unprocessable, "finishing done requires non-empty outcome.evidence")
			}
		case string:
			if strings.TrimSpace(e) == "" {
				return domain.Errf(domain.Unprocessable, "finishing done requires non-empty outcome.evidence")
			}
		default:
			return domain.Errf(domain.Unprocessable, "finishing done requires non-empty outcome.evidence")
		}
	}
	return nil
}

// claimWorkTx claims a pending + ready work item inside tx: pending ->
// in_progress, assignee set, attempt bumped, version guarded, `started` event
// emitted. readyFn may veto the claim (steps are only ready when their deps
// are all done; tasks have no deps). planID/agentID only feed the event row.
func claimWorkTx(tx *sql.Tx, t workTable, id, planID, assignee string, expectedVersion int, readyFn func() error) error {
	var status string
	var attempt, version int
	if err := tx.QueryRow(`SELECT status, attempt, version FROM `+t.name+` WHERE id = ?`, id).
		Scan(&status, &attempt, &version); err == sql.ErrNoRows {
		return domain.Errf(domain.NotFound, "%s %s not found", t.name, id)
	} else if err != nil {
		return fmt.Errorf("store: claim %s: %w", t.name, err)
	}
	if version != expectedVersion {
		return domain.Errf(domain.PreconditionFailed, "%s %s version %d != expected %d", t.name, id, version, expectedVersion)
	}
	if status != "pending" {
		return domain.Errf(domain.Conflict, "%s %s is %s, only pending work can be claimed", t.name, id, status)
	}
	if assignee != "" {
		var one string
		if err := tx.QueryRow(`SELECT id FROM agents WHERE id = ?`, assignee).Scan(&one); err == sql.ErrNoRows {
			return domain.Errf(domain.Invalid, "agent %s is not registered", assignee)
		} else if err != nil {
			return fmt.Errorf("store: claim %s: %w", t.name, err)
		}
	}
	if readyFn != nil {
		if err := readyFn(); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`UPDATE `+t.name+` SET status='in_progress', assignee_agent_id=?, attempt=attempt+1, version=version+1, updated_at=? WHERE id=? AND version=?`,
		nullStr(assignee), now(), id, expectedVersion)
	if err != nil {
		return fmt.Errorf("store: claim %s: %w", t.name, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.PreconditionFailed, "%s %s version %d != expected %d", t.name, id, version, expectedVersion)
	}
	var stepID, taskID string
	if t.hasDep {
		stepID = id
	} else {
		taskID = id
	}
	_, err = AppendEventTx(tx, Event{AgentID: assignee, PlanID: planID, StepID: stepID, TaskID: taskID, Type: "started",
		Payload: mustJSON(map[string]any{"assignee": assignee, "attempt": attempt + 1})})
	return err
}

// finishWorkTx moves an in_progress work item to done/failed/blocked/
// interrupted, guarding on version, validating the outcome and emitting a
// `finished` event. onComplete runs after a successful update (steps use it to
// complete the plan when the goal step finishes). Returns the new version.
func finishWorkTx(tx *sql.Tx, t workTable, id, planID string, expectedVersion int, status, outcome string, onComplete func(*sql.Tx) error) (int, error) {
	if status != "done" && status != "failed" && status != "blocked" && status != "interrupted" {
		return 0, domain.Errf(domain.Invalid, "finish status must be done|failed|blocked|interrupted")
	}
	if err := validateOutcome(status, outcome); err != nil {
		return 0, err
	}
	var cur string
	var version int
	if err := tx.QueryRow(`SELECT status, version FROM `+t.name+` WHERE id = ?`, id).Scan(&cur, &version); err == sql.ErrNoRows {
		return 0, domain.Errf(domain.NotFound, "%s %s not found", t.name, id)
	} else if err != nil {
		return 0, fmt.Errorf("store: finish %s: %w", t.name, err)
	}
	if version != expectedVersion {
		return 0, domain.Errf(domain.PreconditionFailed, "%s %s version %d != expected %d", t.name, id, version, expectedVersion)
	}
	if err := domain.StepTransitions.Check(cur, status); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE `+t.name+` SET status=?, outcome=?, version=version+1, updated_at=? WHERE id=? AND version=?`,
		status, outcome, now(), id, expectedVersion)
	if err != nil {
		return 0, fmt.Errorf("store: finish %s: %w", t.name, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return 0, domain.Errf(domain.PreconditionFailed, "%s %s version %d != expected %d", t.name, id, version, expectedVersion)
	}
	var agent string
	_ = tx.QueryRow(`SELECT COALESCE(assignee_agent_id,'') FROM `+t.name+` WHERE id = ?`, id).Scan(&agent)
	var stepID, taskID string
	if t.hasDep {
		stepID = id
	} else {
		taskID = id
	}
	if _, err := AppendEventTx(tx, Event{AgentID: agent, PlanID: planID, StepID: stepID, TaskID: taskID, Type: "finished",
		Payload: mustJSON(map[string]any{"status": status})}); err != nil {
		return 0, err
	}
	if onComplete != nil {
		if err := onComplete(tx); err != nil {
			return 0, err
		}
	}
	return version + 1, nil
}

// mustJSON marshals v (empty object on error) for JSON-valued TEXT columns.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
