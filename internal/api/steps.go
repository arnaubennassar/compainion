package api

import (
	"encoding/json"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// registerSteps wires the /steps* routes (see api/openapi.yaml).
func (s *Server) registerSteps() {
	s.handle("GET /steps/{id}", s.getStep)
	s.handle("PATCH /steps/{id}", s.patchStep)
	s.handle("DELETE /steps/{id}", s.deleteStep)
	s.handle("POST /steps/{id}/claim", s.claimStep)
	s.handle("POST /steps/{id}/finish", s.finishStep)
	s.handle("POST /steps/{id}/deps", s.addStepDep)
	s.handle("DELETE /steps/{id}/deps/{dep_id}", s.removeStepDep)
}

// stepInput is the POST /plans/{id}/steps body.
type stepInput struct {
	Kind               string         `json:"kind"`
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	AcceptanceCriteria string         `json:"acceptance_criteria"`
	Scope              map[string]any `json:"scope"`
	AddedBy            string         `json:"added_by"`
	SuggestedExecutor  string         `json:"suggested_executor"`
	Deps               []string       `json:"deps"`
	ApprovalRef        string         `json:"approval_ref"`
	DependsOnID        string         `json:"depends_on_id"`
}

// stepPatch is the PATCH /steps/{id} body (pointer fields = present).
type stepPatch struct {
	Title              *string        `json:"title"`
	Description        *string        `json:"description"`
	AcceptanceCriteria *string        `json:"acceptance_criteria"`
	Scope              map[string]any `json:"scope"`
	SuggestedExecutor  *string        `json:"suggested_executor"`
	ApprovalRef        string         `json:"approval_ref"`
}

func (in stepPatch) update() store.StepUpdate {
	var scope *string
	if in.Scope != nil {
		b, _ := json.Marshal(in.Scope)
		s := string(b)
		scope = &s
	}
	return store.StepUpdate{
		Title: in.Title, Description: in.Description, AcceptanceCriteria: in.AcceptanceCriteria,
		Scope: scope, SuggestedExecutor: in.SuggestedExecutor,
	}
}

// parseIfMatchOptional parses If-Match when present; missing header returns
// (0, nil). Used on claim/finish, where the spec does not require If-Match:
// the handler then uses the step's current version.
func parseIfMatchOptional(r *http.Request) (int, error) {
	if r.Header.Get("If-Match") == "" {
		return 0, nil
	}
	return parseIfMatch(r)
}

// createStep is POST /plans/{id}/steps.
func (s *Server) createStep(w http.ResponseWriter, r *http.Request) {
	in, err := decode[stepInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	scope := "{}"
	if in.Scope != nil {
		b, _ := json.Marshal(in.Scope)
		scope = string(b)
	}
	st, err := s.db.CreateStep(r.Context(), store.Step{
		PlanID: r.PathValue("id"), Kind: in.Kind, Title: in.Title,
		Description: in.Description, AcceptanceCriteria: in.AcceptanceCriteria,
		Scope: scope, AddedBy: in.AddedBy, SuggestedExecutor: in.SuggestedExecutor,
		Deps: in.Deps,
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, st.Version)
	writeJSON(w, http.StatusCreated, viewStep(st))
}

func (s *Server) getStep(w http.ResponseWriter, r *http.Request) {
	st, err := s.db.GetStep(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, st.Version)
	writeJSON(w, http.StatusOK, viewStep(st))
}

func (s *Server) patchStep(w http.ResponseWriter, r *http.Request) {
	version, err := parseIfMatch(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	in, err := decode[stepPatch](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	st, err := s.db.UpdateStep(r.Context(), r.PathValue("id"), version, in.update(), in.ApprovalRef)
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, st.Version)
	writeJSON(w, http.StatusOK, viewStep(st))
}

func (s *Server) deleteStep(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteStep(r.Context(), r.PathValue("id"), ""); err != nil {
		writeProblem(w, err)
		return
	}
	writeNoContent(w)
}

type claimInput struct {
	AgentID string `json:"agent_id"`
}

func (s *Server) claimStep(w http.ResponseWriter, r *http.Request) {
	in, err := decode[claimInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	if in.AgentID == "" {
		writeProblem(w, domain.Errf(domain.Invalid, "agent_id is required"))
		return
	}
	version, err := parseIfMatchOptional(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	st, err := s.db.GetStep(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	if version == 0 {
		version = st.Version
	}
	st, err = s.db.ClaimStep(r.Context(), r.PathValue("id"), in.AgentID, version)
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, st.Version)
	writeJSON(w, http.StatusOK, viewStep(st))
}

// finishInput is the POST .../finish body; outcome is stored as its JSON.
type finishInput struct {
	Status  string       `json:"status"`
	Outcome outcomeInput `json:"outcome"`
}

type outcomeInput struct {
	Result   string   `json:"result"`
	Evidence []string `json:"evidence"`
}

func (o outcomeInput) marshal() string {
	b, _ := json.Marshal(o)
	return string(b)
}

func (s *Server) finishStep(w http.ResponseWriter, r *http.Request) {
	in, err := decode[finishInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	version, err := parseIfMatchOptional(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	st, err := s.db.GetStep(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	if version == 0 {
		version = st.Version
	}
	st, err = s.db.FinishStep(r.Context(), r.PathValue("id"), version, in.Status, in.Outcome.marshal())
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, st.Version)
	writeJSON(w, http.StatusOK, viewStep(st))
}

type depInput struct {
	DependsOnID string `json:"depends_on_id"`
}

// addStepDep: POST /steps/{id}/deps — If-Match required (428 missing, 412
// stale per the spec responses).
func (s *Server) addStepDep(w http.ResponseWriter, r *http.Request) {
	version, err := parseIfMatch(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	in, err := decode[depInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	if err := s.db.AddDep(r.Context(), r.PathValue("id"), in.DependsOnID, version); err != nil {
		writeProblem(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) removeStepDep(w http.ResponseWriter, r *http.Request) {
	version, err := parseIfMatch(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	if err := s.db.RemoveDep(r.Context(), r.PathValue("id"), r.PathValue("dep_id"), version); err != nil {
		writeProblem(w, err)
		return
	}
	writeNoContent(w)
}
