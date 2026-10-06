package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/arnaubennassar/compainion/internal/store"
)

const (
	// streamPollInterval is how often the SSE handler polls the events table.
	streamPollInterval = 200 * time.Millisecond
	// streamPingInterval is the gap between `: ping` comments.
	streamPingInterval = 15 * time.Second
)

// registerStream is called from New to expose the SSE event stream.
func (s *Server) registerStream() {
	s.handle("GET /stream", s.stream)
}

// stream serves GET /stream?types=a,b&workstream_id=... as a
// text/event-stream of signal-only messages:
//
//	id: <event id>\nevent: <type>\ndata: {"type":..,"id":..}\n\n
//
// It polls the events table every streamPollInterval using ListEvents with an
// exclusive Since cursor (no in-process bus). Without a Last-Event-ID header
// it starts from the latest event at connect time; with one it replays all
// events after that id. A `: ping` comment is written when streamPingInterval
// has passed since the last write. The stream ends when the client
// disconnects (request context cancelled).
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, errors.New("streaming unsupported"))
		return
	}
	q := r.URL.Query()
	var types []string
	if t := q.Get("types"); t != "" {
		types = strings.Split(t, ",")
	}
	wsID := q.Get("workstream_id")

	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		latest, err := store.LatestEventID(r.Context(), s.db)
		if err != nil {
			writeProblem(w, err)
			return
		}
		cursor = latest
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	lastWrite := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			evs, err := s.db.ListEvents(ctx, store.EventFilter{Types: types, Since: cursor, Limit: 500})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.log.Error("stream: list events", "err", err)
				continue
			}
			for _, ev := range evs {
				cursor = ev.ID
				if wsID != "" {
					evWS, err := store.EventWorkstreamID(ctx, s.db, ev)
					if err != nil {
						s.log.Error("stream: resolve workstream", "err", err)
						continue
					}
					if evWS != wsID {
						continue
					}
				}
				if _, err := w.Write(sseMessage(ev)); err != nil {
					return
				}
				flusher.Flush()
				lastWrite = time.Now()
			}
			if len(evs) == 0 && time.Since(lastWrite) >= streamPingInterval {
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flusher.Flush()
				lastWrite = time.Now()
			}
		}
	}
}

// sseMessage renders one SSE message (signal only: type and id, no payload).
func sseMessage(ev store.Event) []byte {
	return []byte("id: " + ev.ID + "\nevent: " + ev.Type +
		"\ndata: {\"type\":\"" + ev.Type + "\",\"id\":\"" + ev.ID + "\"}\n\n")
}
