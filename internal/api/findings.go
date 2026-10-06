package api

import (
	"encoding/json"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/store"
)

// registerFindings wires the finding routes. Handlers contain no business
// rules: parse, call the store, map errors via writeProblem.
func (s *Server) registerFindings() {
	s.handle("GET /findings", s.listFindings)
	s.handle("POST /findings", s.createFinding)
	s.handle("GET /findings/{id}", s.getFinding)
	s.handle("POST /findings/{id}/resolve", s.resolveFinding)
	s.handle("POST /findings/{id}/surface", s.surfaceFinding)
}

// findingOut is the wire shape for a finding: evidence as a JSON array and
// the created flag (true on 201, false on dedupe).
type findingOut struct {
	store.Finding
	Evidence json.RawMessage `json:"evidence"`
	Created  bool            `json:"created"`
}

func toFindingOut(f store.Finding, created bool) findingOut {
	out := findingOut{Finding: f, Evidence: json.RawMessage(f.Evidence), Created: created}
	if len(out.Evidence) == 0 {
		out.Evidence = json.RawMessage("[]")
	}
	return out
}

// listFindings handles GET /findings with query/status filters.
func (s *Server) listFindings(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	q := r.URL.Query()
	items, err := s.db.ListFindings(r.Context(), store.FindingFilter{
		Query:  q.Get("query"),
		Status: q.Get("status"),
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	page, next := pageFindings(items, limit, cursor)
	out := make([]findingOut, len(page))
	for i, f := range page {
		out[i] = toFindingOut(f, false)
	}
	writeJSON(w, http.StatusOK, Envelope{Items: out, NextCursor: next})
}

// pageByID applies cursor (exclusive, ids are ULIDs so byte order works) and
// limit to an id-ordered slice; next is the id of the last returned item when
// more remain, "" otherwise.
func pagedBy[T any](items []T, limit int, cursor string, id func(T) string) ([]T, string) {
	start := 0
	if cursor != "" {
		for i, it := range items {
			if id(it) > cursor {
				start = i
				break
			}
			start = i + 1
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[start:end]
	if end < len(items) && len(page) > 0 {
		return page, id(page[len(page)-1])
	}
	return page, ""
}

// pageFindings pages the id-ordered finding list, returning the next cursor.
func pageFindings(items []store.Finding, limit int, cursor string) ([]store.Finding, string) {
	page, next := pagedBy(items, limit, cursor, func(f store.Finding) string { return f.ID })
	return page, next
}

// findingInput is the create body; evidence arrives as a JSON array and is
// stored as its serialised form.
type findingInput struct {
	ReportedByAgentID string            `json:"reported_by_agent_id"`
	PlanID            string            `json:"plan_id"`
	StepID            string            `json:"step_id"`
	Category          string            `json:"category"`
	Severity          string            `json:"severity"`
	Location          string            `json:"location"`
	Title             string            `json:"title"`
	Details           string            `json:"details"`
	Evidence          []json.RawMessage `json:"evidence"`
}

// createFinding handles POST /findings (201 on create, 200 on dedupe).
func (s *Server) createFinding(w http.ResponseWriter, r *http.Request) {
	in, err := decode[findingInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	f := store.Finding{
		ReportedByAgentID: in.ReportedByAgentID,
		Category:          in.Category,
		Severity:          in.Severity,
		Location:          in.Location,
		Title:             in.Title,
		Details:           in.Details,
	}
	if in.PlanID != "" {
		f.PlanID = &in.PlanID
	}
	if in.StepID != "" {
		f.StepID = &in.StepID
	}
	if len(in.Evidence) > 0 {
		b, err := json.Marshal(in.Evidence)
		if err != nil {
			writeProblem(w, err)
			return
		}
		f.Evidence = string(b)
	}
	out, created, err := s.db.UpsertFinding(r.Context(), f)
	if err != nil {
		writeProblem(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toFindingOut(out, created))
}

// getFinding handles GET /findings/{id}.
func (s *Server) getFinding(w http.ResponseWriter, r *http.Request) {
	f, err := s.db.GetFinding(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFindingOut(f, false))
}

// resolveFinding handles POST /findings/{id}/resolve.
func (s *Server) resolveFinding(w http.ResponseWriter, r *http.Request) {
	in, err := decode[struct {
		Resolution    string `json:"resolution"`
		ResolutionRef string `json:"resolution_ref"`
	}](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	f, err := s.db.ResolveFinding(r.Context(), r.PathValue("id"), in.Resolution, in.ResolutionRef)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFindingOut(f, false))
}

// surfaceFinding handles POST /findings/{id}/surface.
func (s *Server) surfaceFinding(w http.ResponseWriter, r *http.Request) {
	in, err := decode[struct {
		InterruptionID string `json:"interruption_id"`
	}](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	f, err := s.db.MarkFindingSurfaced(r.Context(), r.PathValue("id"), in.InterruptionID)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFindingOut(f, false))
}
