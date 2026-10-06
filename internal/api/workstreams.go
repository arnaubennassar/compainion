package api

import (
	"net/http"

	"github.com/arnaubennassar/compainion/internal/store"
)

// registerWorkstreams wires the workstream resource routes.
func (s *Server) registerWorkstreams() {
	s.handle("GET /workstreams", s.listWorkstreams)
	s.handle("POST /workstreams", s.createWorkstream)
	s.handle("GET /workstreams/{id}", s.getWorkstream)
	s.handle("PATCH /workstreams/{id}", s.updateWorkstream)
	s.handle("DELETE /workstreams/{id}", s.deleteWorkstream)
}

// workstreamInput is the create/patch body (WorkstreamInput in the spec).
type workstreamInput struct {
	Title    *string `json:"title"`
	Status   *string `json:"status"`
	Priority *int    `json:"priority"`
}

func (s *Server) createWorkstream(w http.ResponseWriter, r *http.Request) {
	in, err := decode[workstreamInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	var title string
	if in.Title != nil {
		title = *in.Title
	}
	ws, err := s.db.CreateWorkstream(r.Context(), store.Workstream{
		Title: title, Status: ptrOrEmpty(in.Status), Priority: ptrOrInt(in.Priority),
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

func (s *Server) listWorkstreams(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	wss, err := s.db.ListWorkstreams(r.Context())
	if err != nil {
		writeProblem(w, err)
		return
	}
	wss = paginateAfter(wss, cursor, limit, func(w store.Workstream) string { return w.ID })
	writeJSON(w, http.StatusOK, Envelope{Items: wss, NextCursor: cursorOf(wss, func(w store.Workstream) string { return w.ID })})
}

// lastStr returns the id of the last item (the next cursor).
func cursorOf[E any](items []E, id func(E) string) string {
	if len(items) == 0 {
		return ""
	}
	return id(items[len(items)-1])
}

func (s *Server) getWorkstream(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.GetWorkstream(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) updateWorkstream(w http.ResponseWriter, r *http.Request) {
	in, err := decode[workstreamInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	ws, err := s.db.UpdateWorkstream(r.Context(), r.PathValue("id"), store.WorkstreamUpdate{
		Title: in.Title, Status: in.Status, Priority: in.Priority,
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) deleteWorkstream(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteWorkstream(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ptrOrEmpty dereferences p ("" when nil).
func ptrOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ptrOrInt dereferences p (0 when nil, so store defaults apply).
func ptrOrInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// pageByID applies cursor pagination over id-ordered items: keep items with
// id > cursor, at most limit of them.
func paginateAfter[E any](items []E, cursor string, limit int, id func(E) string) []E {
	out := make([]E, 0, len(items))
	for _, it := range items {
		if cursor != "" && id(it) <= cursor {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, it)
	}
	return out
}
