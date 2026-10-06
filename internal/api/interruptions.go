package api

import (
	"net/http"

	"github.com/arnaubennassar/compainion/internal/store"
)

// registerInterruptions wires the interruption routes. Handlers contain no
// business rules: parse, call the store, map errors via writeProblem.
func (s *Server) registerInterruptions() {
	s.handle("GET /interruptions", s.listInterruptions)
	s.handle("POST /interruptions", s.createInterruption)
	s.handle("GET /interruptions/next", s.nextInterruption)
	s.handle("GET /interruptions/{id}", s.getInterruption)
	s.handle("DELETE /interruptions/{id}", s.deleteInterruption)
	s.handle("POST /interruptions/{id}/answers", s.answerInterruption)
	s.handle("POST /interruptions/{id}/dismiss", s.dismissInterruption)
	s.handle("POST /interruptions/{id}/reopen", s.reopenInterruption)
}

// listInterruptions handles GET /interruptions with status/workstream_id/topic
// filters and cursor pagination.
func (s *Server) listInterruptions(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	q := r.URL.Query()
	items, err := s.db.ListInterruptions(r.Context(), store.InterruptionFilter{
		Status:       q.Get("status"),
		WorkstreamID: q.Get("workstream_id"),
		Topic:        q.Get("topic"),
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	page, next := pagedBy(items, limit, cursor, func(it store.Interruption) string { return it.ID })
	writeJSON(w, http.StatusOK, Envelope{Items: page, NextCursor: next})
}

// createInterruption handles POST /interruptions.
func (s *Server) createInterruption(w http.ResponseWriter, r *http.Request) {
	in, err := decode[store.Interruption](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	it, err := s.db.CreateInterruption(r.Context(), in)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

// nextInterruption handles GET /interruptions/next. With ?wait=<seconds> it
// long-polls until an interruption becomes available or the wait elapses
// (204). The store marks the picked interruption presented and returns the
// same-topic batch ids.
func (s *Server) nextInterruption(w http.ResponseWriter, r *http.Request) {
	var picked store.Interruption
	err := waitUntil(r.Context(), parseWait(r.URL.Query().Get("wait")), func() (bool, error) {
		it, err := s.db.NextInterruption(r.Context())
		if err != nil {
			return false, err
		}
		if it.ID != "" {
			picked = it
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	if picked.ID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, picked)
}

// getInterruption handles GET /interruptions/{id} (expanded).
func (s *Server) getInterruption(w http.ResponseWriter, r *http.Request) {
	it, err := s.db.GetInterruption(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// deleteInterruption handles DELETE /interruptions/{id}.
func (s *Server) deleteInterruption(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteInterruption(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answerInterruption handles POST /interruptions/{id}/answers; the updated
// interruption is returned.
func (s *Server) answerInterruption(w http.ResponseWriter, r *http.Request) {
	in, err := decode[store.Answer](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	it, err := s.db.AnswerInterruption(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// dismissInterruption handles POST /interruptions/{id}/dismiss.
func (s *Server) dismissInterruption(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DismissInterruption(r.Context(), id); err != nil {
		writeProblem(w, err)
		return
	}
	it, err := s.db.GetInterruption(r.Context(), id)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// reopenInterruption handles POST /interruptions/{id}/reopen.
func (s *Server) reopenInterruption(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.ReopenInterruption(r.Context(), id); err != nil {
		writeProblem(w, err)
		return
	}
	it, err := s.db.GetInterruption(r.Context(), id)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}
