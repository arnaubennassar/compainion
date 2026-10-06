package api

import (
	"encoding/json"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/store"
)

// This file renders store models as the JSON shapes the OpenAPI spec
// promises: JSON-valued TEXT columns (scope, outcome, acceptance_checks,
// evidence) are decoded into real JSON values, and the derived `ready` flag
// on steps is preserved. Handlers use the views + paginate + setETag helpers.

// planView is the wire shape of a plan (acceptance_checks array, scope object).
type planView struct {
	ID                  string          `json:"id"`
	WorkstreamID        string          `json:"workstream_id"`
	Title               string          `json:"title"`
	Summary             string          `json:"summary"`
	Goals               string          `json:"goals"`
	AcceptanceCriteria  string          `json:"acceptance_criteria"`
	AcceptanceChecks    []string        `json:"acceptance_checks"`
	Scope               json.RawMessage `json:"scope"`
	Status              string          `json:"status"`
	CreatorAgentID      string          `json:"creator_agent_id,omitempty"`
	OrchestratorAgentID *string         `json:"orchestrator_agent_id,omitempty"`
	GoalStepID          *string         `json:"goal_step_id,omitempty"`
	ApprovedBy          *string         `json:"approved_by,omitempty"`
	ApprovedAt          *string         `json:"approved_at,omitempty"`
	Version             int             `json:"version"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

func viewPlan(p store.Plan) planView {
	checks := []string{}
	_ = json.Unmarshal([]byte(p.AcceptanceChecks), &checks)
	return planView{
		ID: p.ID, WorkstreamID: p.WorkstreamID, Title: p.Title, Summary: p.Summary,
		Goals: p.Goals, AcceptanceCriteria: p.AcceptanceCriteria,
		AcceptanceChecks: checks, Scope: rawJSON(p.Scope), Status: p.Status,
		CreatorAgentID: p.CreatorAgentID, OrchestratorAgentID: p.OrchestratorAgentID,
		GoalStepID: p.GoalStepID, ApprovedBy: p.ApprovedBy, ApprovedAt: p.ApprovedAt,
		Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// stepView is the wire shape of a step (scope object, outcome object, deps,
// derived ready flag).
type stepView struct {
	ID                 string          `json:"id"`
	PlanID             string          `json:"plan_id"`
	Kind               string          `json:"kind"`
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	AcceptanceCriteria string          `json:"acceptance_criteria"`
	Scope              json.RawMessage `json:"scope"`
	Status             string          `json:"status"`
	Ready              bool            `json:"ready"`
	AssigneeAgentID    *string         `json:"assignee_agent_id,omitempty"`
	Outcome            json.RawMessage `json:"outcome,omitempty"`
	Attempt            int             `json:"attempt"`
	AddedBy            string          `json:"added_by"`
	SuggestedExecutor  string          `json:"suggested_executor,omitempty"`
	Version            int             `json:"version"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	Deps               []string        `json:"deps,omitempty"`
}

func viewStep(s store.Step) stepView {
	var outcome json.RawMessage
	if s.Outcome != nil {
		outcome = rawJSON(*s.Outcome)
	}
	deps := s.Deps
	if deps == nil {
		deps = []string{}
	}
	return stepView{
		ID: s.ID, PlanID: s.PlanID, Kind: s.Kind, Title: s.Title,
		Description: s.Description, AcceptanceCriteria: s.AcceptanceCriteria,
		Scope: rawJSON(s.Scope), Status: s.Status, Ready: s.Ready,
		AssigneeAgentID: s.AssigneeAgentID, Outcome: outcome,
		Attempt: s.Attempt, AddedBy: s.AddedBy, SuggestedExecutor: s.SuggestedExecutor,
		Version: s.Version, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Deps: deps,
	}
}

// taskView is the wire shape of a task.
type taskView struct {
	ID                 string          `json:"id"`
	WorkstreamID       string          `json:"workstream_id"`
	RequestedBy        string          `json:"requested_by"`
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	AcceptanceCriteria string          `json:"acceptance_criteria"`
	Scope              json.RawMessage `json:"scope"`
	Status             string          `json:"status"`
	AssigneeAgentID    *string         `json:"assignee_agent_id,omitempty"`
	Outcome            json.RawMessage `json:"outcome,omitempty"`
	Attempt            int             `json:"attempt"`
	AddedBy            string          `json:"added_by"`
	Version            int             `json:"version"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

func viewTask(t store.Task) taskView {
	var outcome json.RawMessage
	if t.Outcome != nil {
		outcome = rawJSON(*t.Outcome)
	}
	return taskView{
		ID: t.ID, WorkstreamID: t.WorkstreamID, RequestedBy: t.RequestedBy,
		Title: t.Title, Description: t.Description, AcceptanceCriteria: t.AcceptanceCriteria,
		Scope: rawJSON(t.Scope), Status: t.Status, AssigneeAgentID: t.AssigneeAgentID,
		Outcome: outcome, Attempt: t.Attempt, AddedBy: t.AddedBy,
		Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// graphEdge is the wire shape of one dependency edge (step depends on).
type graphEdge struct {
	StepID      string `json:"step_id"`
	DependsOnID string `json:"depends_on_id"`
}

func rawJSON(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

// paginate splits items (ordered by ascending id) into one page after the
// opaque cursor, returning the page and the next cursor ("" on the last page).
func paginate[T any](items []T, id func(T) string, cursor string, limit int) ([]T, string) {
	start := 0
	if cursor != "" {
		for i, it := range items {
			if id(it) == cursor {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[start:end]
	if page == nil {
		page = make([]T, 0)
	}
	next := ""
	if end < len(items) && len(page) > 0 {
		next = id(page[len(page)-1])
	}
	return page, next
}

// setETag sets the ETag header for a resource version.
func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", etag(version))
}

// writeNoContent writes an empty 204 response.
func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
