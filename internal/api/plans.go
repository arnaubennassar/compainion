package api

import (
	"encoding/json"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// registerPlans wires the /plans* routes (see api/openapi.yaml for the
// contract).
func (s *Server) registerPlans() {
	s.handle("GET /plans", s.listPlans)
	s.handle("POST /plans", s.createPlan)
	s.handle("GET /plans/{id}", s.getPlan)
	s.handle("PATCH /plans/{id}", s.patchPlan)
	s.handle("DELETE /plans/{id}", s.deletePlan)
	s.handle("POST /plans/{id}/approve", s.approvePlan)
	s.handle("POST /plans/{id}/assign", s.assignPlan)
	s.handle("GET /plans/{id}/graph", s.planGraph)
	s.handle("GET /plans/{id}/steps", s.listPlanSteps)
	s.handle("POST /plans/{id}/steps", s.createStep)
	s.handle("GET /plans/{id}/steps/next", s.nextPlanSteps)
}

// planInput is the POST /plans body.
type planInput struct {
	WorkstreamID       string         `json:"workstream_id"`
	Title              string         `json:"title"`
	Summary            string         `json:"summary"`
	Goals              string         `json:"goals"`
	AcceptanceCriteria string         `json:"acceptance_criteria"`
	AcceptanceChecks   []string       `json:"acceptance_checks"`
	Scope              map[string]any `json:"scope"`
	CreatorAgentID     string         `json:"creator_agent_id"`
}

// planPatch is the PATCH /plans/{id} body (pointer fields = "field present").
type planPatch struct {
	Title              *string        `json:"title"`
	Summary            *string        `json:"summary"`
	Goals              *string        `json:"goals"`
	AcceptanceCriteria *string        `json:"acceptance_criteria"`
	AcceptanceChecks   []string       `json:"acceptance_checks"`
	Scope              map[string]any `json:"scope"`
	CreatorAgentID     string         `json:"creator_agent_id"`
	ApprovalRef        string         `json:"approval_ref"`
}

func (in planPatch) update() store.PlanUpdate {
	var checks *string
	if in.AcceptanceChecks != nil {
		b, _ := json.Marshal(in.AcceptanceChecks)
		s := string(b)
		checks = &s
	}
	var scope *string
	if in.Scope != nil {
		b, _ := json.Marshal(in.Scope)
		s := string(b)
		scope = &s
	}
	return store.PlanUpdate{
		Title: in.Title, Summary: in.Summary, Goals: in.Goals,
		AcceptanceCriteria: in.AcceptanceCriteria, AcceptanceChecks: checks, Scope: scope,
	}
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	plans, err := s.db.ListPlans(r.Context(), store.PlanFilter{
		WorkstreamID: r.URL.Query().Get("workstream_id"),
		Status:       r.URL.Query().Get("status"),
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	views := make([]planView, 0, len(plans))
	for _, p := range plans {
		views = append(views, viewPlan(p))
	}
	page, next := paginate(views, func(v planView) string { return v.ID }, cursor, limit)
	writeJSON(w, http.StatusOK, Envelope{Items: page, NextCursor: next})
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	in, err := decode[planInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	if in.Goals == "" {
		writeProblem(w, domain.Errf(domain.Invalid, "goals is required"))
		return
	}
	checks := in.AcceptanceChecks
	if checks == nil {
		checks = []string{}
	}
	scope := in.Scope
	if scope == nil {
		scope = map[string]any{}
	}
	cb, _ := json.Marshal(checks)
	sb, _ := json.Marshal(scope)
	p, err := s.db.CreatePlan(r.Context(), store.Plan{
		WorkstreamID: in.WorkstreamID, Title: in.Title, Summary: in.Summary,
		Goals: in.Goals, AcceptanceCriteria: in.AcceptanceCriteria,
		AcceptanceChecks: string(cb), Scope: string(sb), CreatorAgentID: in.CreatorAgentID,
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, p.Version)
	writeJSON(w, http.StatusCreated, viewPlan(p))
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	p, err := s.db.GetPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, p.Version)
	writeJSON(w, http.StatusOK, viewPlan(p))
}

func (s *Server) patchPlan(w http.ResponseWriter, r *http.Request) {
	version, err := parseIfMatch(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	in, err := decode[planPatch](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	p, err := s.db.UpdatePlan(r.Context(), r.PathValue("id"), version, in.update(), in.ApprovalRef)
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, p.Version)
	writeJSON(w, http.StatusOK, viewPlan(p))
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeletePlan(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	writeNoContent(w)
}

type approveInput struct {
	ApprovedBy string `json:"approved_by"`
}

func (s *Server) approvePlan(w http.ResponseWriter, r *http.Request) {
	in, err := decode[approveInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	p, err := s.db.ApprovePlan(r.Context(), r.PathValue("id"), in.ApprovedBy)
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, p.Version)
	writeJSON(w, http.StatusOK, viewPlan(p))
}

type assignInput struct {
	AgentID string `json:"agent_id"`
}

func (s *Server) assignPlan(w http.ResponseWriter, r *http.Request) {
	in, err := decode[assignInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	p, err := s.db.AssignPlan(r.Context(), r.PathValue("id"), in.AgentID)
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, p.Version)
	writeJSON(w, http.StatusOK, viewPlan(p))
}

func (s *Server) planGraph(w http.ResponseWriter, r *http.Request) {
	g, err := s.db.PlanGraph(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	steps := make([]stepView, 0, len(g.Steps))
	for _, st := range g.Steps {
		steps = append(steps, viewStep(st))
	}
	edges := make([]graphEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, graphEdge{StepID: e[0], DependsOnID: e[1]})
	}
	writeJSON(w, http.StatusOK, struct {
		Steps []stepView  `json:"steps"`
		Edges []graphEdge `json:"edges"`
	}{Steps: steps, Edges: edges})
}

func (s *Server) listPlanSteps(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	steps, err := s.db.ListSteps(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	views := make([]stepView, 0, len(steps))
	for _, st := range steps {
		views = append(views, viewStep(st))
	}
	page, next := paginate(views, func(v stepView) string { return v.ID }, cursor, limit)
	writeJSON(w, http.StatusOK, Envelope{Items: page, NextCursor: next})
}

func (s *Server) nextPlanSteps(w http.ResponseWriter, r *http.Request) {
	steps, err := s.db.NextSteps(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	views := make([]stepView, 0, len(steps))
	for _, st := range steps {
		views = append(views, viewStep(st))
	}
	writeJSON(w, http.StatusOK, Envelope{Items: views, NextCursor: ""})
}
