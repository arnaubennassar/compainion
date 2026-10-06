package api

import (
	"encoding/json"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// registerAgents wires the agent resource routes.
func (s *Server) registerAgents() {
	s.handle("GET /agents", s.listAgents)
	s.handle("POST /agents", s.registerAgent)
	s.handle("GET /agents/{id}", s.getAgent)
	s.handle("PATCH /agents/{id}", s.patchAgent)
	s.handle("POST /agents/{id}/heartbeat", s.agentHeartbeat)
}

// agentDTO is the wire form of an agent: handle is a JSON object on the wire
// and TEXT-holding-JSON in the store.
type agentDTO struct {
	ID              string          `json:"id"`
	Role            string          `json:"role"`
	ParentID        string          `json:"parent_id,omitempty"`
	Harness         string          `json:"harness,omitempty"`
	Handle          json.RawMessage `json:"handle,omitempty"`
	Status          string          `json:"status"`
	WorkstreamID    string          `json:"workstream_id,omitempty"`
	LastHeartbeatAt *string         `json:"last_heartbeat_at,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

func agentFromStore(a store.Agent) agentDTO {
	var handle json.RawMessage
	if a.Handle != "" {
		handle = json.RawMessage(a.Handle)
	}
	return agentDTO{
		ID: a.ID, Role: a.Role, ParentID: a.ParentID, Harness: a.Harness,
		Handle: handle, Status: a.Status, WorkstreamID: a.WorkstreamID,
		LastHeartbeatAt: a.LastHeartbeatAt, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

// agentInput mirrors AgentInput in the spec.
type agentInput struct {
	ID           *string         `json:"id"`
	Role         *string         `json:"role"`
	ParentID     *string         `json:"parent_id"`
	Harness      *string         `json:"harness"`
	Handle       json.RawMessage `json:"handle"`
	Status       *string         `json:"status"`
	WorkstreamID *string         `json:"workstream_id"`
}

func (s *Server) registerAgent(w http.ResponseWriter, r *http.Request) {
	in, err := decode[agentInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	var a store.Agent
	if in.ID != nil {
		a.ID = *in.ID
	}
	if in.Role != nil {
		a.Role = *in.Role
	}
	if in.ParentID != nil {
		a.ParentID = *in.ParentID
	}
	if in.Harness != nil {
		a.Harness = *in.Harness
	}
	if len(in.Handle) > 0 {
		if err := validateJSONObject(in.Handle); err != nil {
			writeProblem(w, err)
			return
		}
		a.Handle = string(in.Handle)
	}
	if in.Status != nil {
		a.Status = *in.Status
	}
	if in.WorkstreamID != nil {
		a.WorkstreamID = *in.WorkstreamID
	}
	out, err := s.db.RegisterAgent(r.Context(), a)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, agentFromStore(out))
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	q := r.URL.Query()
	agents, err := s.db.ListAgents(r.Context(), store.AgentFilter{
		Role:         q.Get("role"),
		Status:       q.Get("status"),
		ParentID:     q.Get("parent_id"),
		WorkstreamID: q.Get("workstream_id"),
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	agents = paginateAfter(agents, cursor, limit, func(a store.Agent) string { return a.ID })
	writeJSON(w, http.StatusOK, Envelope{Items: agents, NextCursor: cursorOf(agents, func(a store.Agent) string { return a.ID })})
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.db.GetAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agentFromStore(a))
}

func (s *Server) patchAgent(w http.ResponseWriter, r *http.Request) {
	in, err := decode[agentInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	u := store.AgentUpdate{Status: in.Status}
	a, err := s.db.UpdateAgent(r.Context(), r.PathValue("id"), u)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agentFromStore(a))
}

func (s *Server) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	if err := s.db.AgentHeartbeat(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateJSONObject reports Invalid when raw is not a JSON object.
func validateJSONObject(raw []byte) error {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return domain.Errf(domain.Invalid, "value must be a JSON object")
	}
	return nil
}
