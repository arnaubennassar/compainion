package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// registerEvents wires the event log routes (global and per-agent).
func (s *Server) registerEvents() {
	s.handle("GET /events", s.listEvents)
	s.handle("POST /events", s.appendEvent)
	s.handle("GET /agents/{id}/events", s.listAgentEvents)
	s.handle("POST /agents/{id}/events", s.appendAgentEvent)
}

// eventDTO is the wire form of an event: payload is a JSON object on the wire
// and TEXT-holding-JSON in the store.
type eventDTO struct {
	ID      string          `json:"id"`
	AgentID string          `json:"agent_id,omitempty"`
	PlanID  string          `json:"plan_id,omitempty"`
	StepID  string          `json:"step_id,omitempty"`
	TaskID  string          `json:"task_id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	At      string          `json:"at"`
}

func eventsFromStore(evs []store.Event) []eventDTO {
	out := make([]eventDTO, 0, len(evs))
	for _, ev := range evs {
		out = append(out, eventDTO{
			ID: ev.ID, AgentID: ev.AgentID, PlanID: ev.PlanID, StepID: ev.StepID,
			TaskID: ev.TaskID, Type: ev.Type, Payload: json.RawMessage(ev.Payload), At: ev.At,
		})
	}
	return out
}

// eventInput mirrors EventInput in the spec.
type eventInput struct {
	AgentID string          `json:"agent_id"`
	PlanID  string          `json:"plan_id"`
	StepID  string          `json:"step_id"`
	TaskID  string          `json:"task_id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// eventTypes is the events.type CHECK enum in the store; validating here maps
// a bad type to 400 instead of an internal constraint error.
var eventTypes = map[string]bool{
	"started": true, "progress": true, "needs_input": true, "finished": true,
	"steer": true, "error": true, "note": true,
}

// toStoreEvent validates the payload object and returns the store row.
func (in eventInput) toStoreEvent() (store.Event, error) {
	if in.Type == "" {
		return store.Event{}, domain.Errf(domain.Invalid, "type is required")
	}
	if !eventTypes[in.Type] {
		return store.Event{}, domain.Errf(domain.Invalid, "unknown event type %q", in.Type)
	}
	payload := "{}"
	if len(in.Payload) > 0 {
		if err := validateJSONObject(in.Payload); err != nil {
			return store.Event{}, domain.Errf(domain.Invalid, "payload must be a JSON object")
		}
		payload = string(in.Payload)
	}
	return store.Event{
		AgentID: in.AgentID, PlanID: in.PlanID, StepID: in.StepID, TaskID: in.TaskID,
		Type: in.Type, Payload: payload,
	}, nil
}

func (s *Server) appendEvent(w http.ResponseWriter, r *http.Request) {
	in, err := decode[eventInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	ev, err := in.toStoreEvent()
	if err != nil {
		writeProblem(w, err)
		return
	}
	out, err := s.db.AppendEvent(r.Context(), ev)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, eventsFromStore([]store.Event{out})[0])
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	f, wait, err := parseEventQuery(r, 100)
	if err != nil {
		writeProblem(w, err)
		return
	}
	if err := waitUntil(r.Context(), wait, func() (bool, error) {
		evs, err := s.db.ListEvents(r.Context(), f)
		if err != nil {
			return false, err
		}
		return len(evs) > 0, nil
	}); err != nil {
		writeProblem(w, err)
		return
	}
	s.writeEventPage(w, r, f)
}

func (s *Server) writeEventPage(w http.ResponseWriter, r *http.Request, f store.EventFilter) {
	evs, err := s.db.ListEvents(r.Context(), f)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{
		Items:      eventsFromStore(evs),
		NextCursor: store.NextEventCursor(evs),
	})
}

const eventLimit = 500

// parseEventQuery reads the /events query parameters. defaultLimit is 100 for
// the global log; the per-agent route uses the default page size (50/200).
func parseEventQuery(r *http.Request, defaultLimit int) (store.EventFilter, time.Duration, error) {
	q := r.URL.Query()
	limit := defaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return store.EventFilter{}, 0, domain.Errf(domain.Invalid, "limit must be an integer between 1 and 500")
		}
		limit = n
	}
	f := store.EventFilter{
		AgentID: q.Get("agent_id"),
		PlanID:  q.Get("plan_id"),
		StepID:  q.Get("step_id"),
		TaskID:  q.Get("task_id"),
		Since:   q.Get("since"),
		Types:   q["type"],
		Limit:   limit,
	}
	return f, parseWait(q.Get("wait")), nil
}

func (s *Server) listAgentEvents(w http.ResponseWriter, r *http.Request) {
	if _, err := s.db.GetAgent(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	f, _, err := parseEventQuery(r, 100)
	if err != nil {
		writeProblem(w, err)
		return
	}
	f.AgentID = r.PathValue("id")
	s.writeEventPage(w, r, f)
}

func (s *Server) appendAgentEvent(w http.ResponseWriter, r *http.Request) {
	if _, err := s.db.GetAgent(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	in, err := decode[eventInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	ev, err := in.toStoreEvent()
	if err != nil {
		writeProblem(w, err)
		return
	}
	ev.AgentID = r.PathValue("id")
	out, err := s.db.AppendEvent(r.Context(), ev)
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, eventsFromStore([]store.Event{out})[0])
}
