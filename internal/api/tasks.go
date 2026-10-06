package api

import (
	"encoding/json"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// registerTasks wires the /tasks* routes (see api/openapi.yaml).
func (s *Server) registerTasks() {
	s.handle("GET /tasks", s.listTasks)
	s.handle("POST /tasks", s.createTask)
	s.handle("GET /tasks/{id}", s.getTask)
	s.handle("PATCH /tasks/{id}", s.patchTask)
	s.handle("DELETE /tasks/{id}", s.deleteTask)
	s.handle("POST /tasks/{id}/claim", s.claimTask)
	s.handle("POST /tasks/{id}/finish", s.finishTask)
}

// taskInput is the POST /tasks body.
type taskInput struct {
	WorkstreamID       string         `json:"workstream_id"`
	RequestedBy        string         `json:"requested_by"`
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	AcceptanceCriteria string         `json:"acceptance_criteria"`
	Scope              map[string]any `json:"scope"`
	AddedBy            string         `json:"added_by"`
}

// taskPatch is the PATCH /tasks/{id} body (pointer fields = present).
type taskPatch struct {
	Title              *string        `json:"title"`
	Description        *string        `json:"description"`
	AcceptanceCriteria *string        `json:"acceptance_criteria"`
	Scope              map[string]any `json:"scope"`
}

func (in taskPatch) update() store.TaskUpdate {
	var scope *string
	if in.Scope != nil {
		b, _ := json.Marshal(in.Scope)
		s := string(b)
		scope = &s
	}
	return store.TaskUpdate{
		Title: in.Title, Description: in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria, Scope: scope,
	}
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	tasks, err := s.db.ListTasks(r.Context(), store.TaskFilter{
		WorkstreamID: r.URL.Query().Get("workstream_id"),
		Status:       r.URL.Query().Get("status"),
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	views := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		views = append(views, viewTask(t))
	}
	page, next := paginate(views, func(v taskView) string { return v.ID }, cursor, limit)
	writeJSON(w, http.StatusOK, Envelope{Items: page, NextCursor: next})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	in, err := decode[taskInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	if in.RequestedBy == "" {
		writeProblem(w, domain.Errf(domain.Invalid, "requested_by is required"))
		return
	}
	scope := "{}"
	if in.Scope != nil {
		b, _ := json.Marshal(in.Scope)
		scope = string(b)
	}
	t, err := s.db.CreateTask(r.Context(), store.Task{
		WorkstreamID: in.WorkstreamID, RequestedBy: in.RequestedBy,
		Title: in.Title, Description: in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria, Scope: scope, AddedBy: in.AddedBy,
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, t.Version)
	writeJSON(w, http.StatusCreated, viewTask(t))
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, t.Version)
	writeJSON(w, http.StatusOK, viewTask(t))
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	version, err := parseIfMatch(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	in, err := decode[taskPatch](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	t, err := s.db.UpdateTask(r.Context(), r.PathValue("id"), version, in.update())
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, t.Version)
	writeJSON(w, http.StatusOK, viewTask(t))
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteTask(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) claimTask(w http.ResponseWriter, r *http.Request) {
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
	t, err := s.db.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	if version == 0 {
		version = t.Version
	}
	t, err = s.db.ClaimTask(r.Context(), r.PathValue("id"), in.AgentID, version)
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, t.Version)
	writeJSON(w, http.StatusOK, viewTask(t))
}

func (s *Server) finishTask(w http.ResponseWriter, r *http.Request) {
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
	t, err := s.db.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	if version == 0 {
		version = t.Version
	}
	t, err = s.db.FinishTask(r.Context(), r.PathValue("id"), version, in.Status, in.Outcome.marshal())
	if err != nil {
		writeProblem(w, err)
		return
	}
	setETag(w, t.Version)
	writeJSON(w, http.StatusOK, viewTask(t))
}
