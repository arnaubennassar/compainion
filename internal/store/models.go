package store

// Shared row models for every entity in the data model. JSON tags match the
// OpenAPI field names; times are RFC3339 UTC strings. Later agents reuse
// these instead of defining DTOs.

import "database/sql/driver"

// Workstream is a top-level stream of related plans and tasks.
type Workstream struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Agent is a registered Hermes worker/orchestrator/companion.
type Agent struct {
	ID              string  `json:"id"`
	Role            string  `json:"role"`
	ParentID        string  `json:"parent_id,omitempty"`
	Harness         string  `json:"harness,omitempty"`
	Handle          string  `json:"handle,omitempty"` // TEXT holding JSON
	Status          string  `json:"status"`
	WorkstreamID    string  `json:"workstream_id,omitempty"`
	LastHeartbeatAt *string `json:"last_heartbeat_at,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// Plan groups ordered steps toward a goal.
type Plan struct {
	ID                  string  `json:"id"`
	WorkstreamID        string  `json:"workstream_id"`
	Title               string  `json:"title"`
	Summary             string  `json:"summary"`
	Goals               string  `json:"goals"`
	AcceptanceCriteria  string  `json:"acceptance_criteria"`
	AcceptanceChecks    string  `json:"acceptance_checks"` // TEXT holding JSON array
	Scope               string  `json:"scope"`             // TEXT holding JSON object
	Status              string  `json:"status"`
	CreatorAgentID      string  `json:"creator_agent_id,omitempty"`
	OrchestratorAgentID *string `json:"orchestrator_agent_id,omitempty"`
	GoalStepID          *string `json:"goal_step_id,omitempty"`
	ApprovedBy          *string `json:"approved_by,omitempty"`
	ApprovedAt          *string `json:"approved_at,omitempty"`
	Version             int     `json:"version"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

// Step is one unit of work inside a plan.
type Step struct {
	ID                 string  `json:"id"`
	PlanID             string  `json:"plan_id"`
	Kind               string  `json:"kind"` // task | checkpoint | goal
	Title              string  `json:"title"`
	Description        string  `json:"description"`
	AcceptanceCriteria string  `json:"acceptance_criteria"`
	Scope              string  `json:"scope"` // TEXT holding JSON object
	Status             string  `json:"status"`
	AssigneeAgentID    *string `json:"assignee_agent_id,omitempty"`
	Outcome            *string `json:"outcome,omitempty"` // TEXT holding JSON
	Attempt            int     `json:"attempt"`
	AddedBy            string  `json:"added_by"`
	SuggestedExecutor  string  `json:"suggested_executor,omitempty"`
	Version            int     `json:"version"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	// Deps is filled by the steps repository on read; not a column.
	Deps []string `json:"deps,omitempty"`
	// Ready is derived on read: pending and all deps done. Not a column.
	Ready bool `json:"ready,omitempty"`
}

// Task is a standalone unit of work (no plan).
type Task struct {
	ID                 string  `json:"id"`
	WorkstreamID       string  `json:"workstream_id"`
	RequestedBy        string  `json:"requested_by"`
	Title              string  `json:"title"`
	Description        string  `json:"description"`
	AcceptanceCriteria string  `json:"acceptance_criteria"`
	Scope              string  `json:"scope"` // TEXT holding JSON object
	Status             string  `json:"status"`
	AssigneeAgentID    *string `json:"assignee_agent_id,omitempty"`
	Outcome            *string `json:"outcome,omitempty"` // TEXT holding JSON
	Attempt            int     `json:"attempt"`
	AddedBy            string  `json:"added_by"`
	Version            int     `json:"version"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

// Interruption is one blocking ask surfaced to the user.
type Interruption struct {
	ID              string     `json:"id"`
	WorkstreamID    string     `json:"workstream_id,omitempty"`
	RaisedByAgentID string     `json:"raised_by_agent_id,omitempty"`
	PlanID          *string    `json:"plan_id,omitempty"`
	StepID          *string    `json:"step_id,omitempty"`
	TaskID          *string    `json:"task_id,omitempty"`
	Topic           string     `json:"topic"`
	Kind            string     `json:"kind"`
	Priority        string     `json:"priority"`
	Digest          string     `json:"digest"`
	Blocking        bool       `json:"blocking"`
	Status          string     `json:"status"`
	AnsweredAt      *string    `json:"answered_at,omitempty"`
	ExpiresAt       string     `json:"expires_at,omitempty"`
	DefaultAction   string     `json:"default_action,omitempty"`
	CreatedAt       string     `json:"created_at"`
	UpdatedAt       string     `json:"updated_at"`
	Questions       []Question `json:"questions,omitempty"`
	// Batch lists other open interruptions with the same topic (ids only).
	Batch []string `json:"batch,omitempty"`
}

// Question is one ask inside an interruption.
type Question struct {
	ID          string       `json:"id,omitempty"`
	Position    int          `json:"position"`
	Text        string       `json:"text"`
	AnswerType  string       `json:"answer_type"`
	Group       *string      `json:"group,omitempty"` // column grp
	Status      string       `json:"status,omitempty"`
	Suggestions []Suggestion `json:"suggestions,omitempty"`
	Answers     []Answer     `json:"answers,omitempty"`
}

// Suggestion is a canned option for a question.
type Suggestion struct {
	ID          string `json:"id,omitempty"`
	Label       string `json:"label"`
	Rationale   string `json:"rationale,omitempty"`
	Recommended bool   `json:"recommended"`
}

// Answer is one reply to a question.
type Answer struct {
	ID           string  `json:"id"`
	QuestionID   string  `json:"question_id"`
	Author       string  `json:"author"` // user | companion
	Mode         string  `json:"mode"`
	SuggestionID *string `json:"suggestion_id,omitempty"`
	Text         *string `json:"text,omitempty"`
	Final        bool    `json:"final"`
	CreatedAt    string  `json:"created_at"`
}

// Finding is a reported observation with dedupe fingerprint. Location is the
// file/symbol the finding came from; it participates in the fingerprint
// (domain.Fingerprint) with its trailing ":<line>" stripped.
type Finding struct {
	ID                string  `json:"id"`
	ReportedByAgentID string  `json:"reported_by_agent_id,omitempty"`
	PlanID            *string `json:"plan_id,omitempty"`
	StepID            *string `json:"step_id,omitempty"`
	Category          string  `json:"category"`
	Severity          string  `json:"severity"`
	Location          string  `json:"location,omitempty"`
	Title             string  `json:"title"`
	Details           string  `json:"details"`
	Fingerprint       string  `json:"fingerprint"`
	Occurrences       int     `json:"occurrences"`
	Evidence          string  `json:"evidence"` // TEXT holding JSON array
	Status            string  `json:"status"`
	Resolution        *string `json:"resolution,omitempty"`
	ResolutionRef     *string `json:"resolution_ref,omitempty"`
	InterruptionID    *string `json:"interruption_id,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

// Event is one entry of the append-only event log.
type Event struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id,omitempty"`
	PlanID    string `json:"plan_id,omitempty"`
	StepID    string `json:"step_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	Type      string `json:"type"`
	Payload   string `json:"payload"` // TEXT holding JSON object
	At        string `json:"at"`
	CreatedAt string `json:"-"`
	UpdatedAt string `json:"-"`
}

// Subscription registers a webhook or command hook.
type Subscription struct {
	ID        string  `json:"id"`
	Method    string  `json:"method"` // webhook | command
	Target    string  `json:"target"`
	Types     string  `json:"types"`  // TEXT holding JSON array
	Filter    string  `json:"filter"` // TEXT holding JSON object
	Secret    *string `json:"secret,omitempty"`
	Active    bool    `json:"active"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// IdempotencyRecord caches one POST response.
type IdempotencyRecord struct {
	Key          string
	Method       string
	Path         string
	RequestHash  string
	StatusCode   int
	ResponseBody string
}

// nullStr converts "" to SQL NULL for optional FK columns.
func nullStr(s string) driver.Value {
	if s == "" {
		return nil
	}
	return s
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
