package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arnaubennassar/compainion/internal/store"
)

// readDataLine reads SSE lines until the first "data:" line and returns the
// (id line, event line, data line) triple. It fails the test if nothing
// arrives within maxWait.
func readDataLine(t *testing.T, r io.Reader, maxWait time.Duration) (id, event, data string) {
	t.Helper()
	type msg struct {
		lines []string
	}
	ch := make(chan msg, 1)
	go func() {
		var lines []string
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- msg{lines}
				return
			}
			line = strings.TrimRight(line, "\n")
			if line == "" {
				if len(lines) > 0 {
					ch <- msg{lines}
					return
				}
				continue
			}
			lines = append(lines, line)
		}
	}()
	select {
	case m := <-ch:
		for _, l := range m.lines {
			switch {
			case strings.HasPrefix(l, "id: "):
				id = strings.TrimPrefix(l, "id: ")
			case strings.HasPrefix(l, "event: "):
				event = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: "):
				data = strings.TrimPrefix(l, "data: ")
			}
		}
		return id, event, data
	case <-time.After(maxWait):
		t.Fatalf("no SSE message within %s", maxWait)
		return "", "", ""
	}
}

func TestStreamLive(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ts := httptest.NewServer(New(db).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	start := time.Now()
	ev, err := db.AppendEvent(ctx, store.Event{Type: "needs_input"})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	id, event, data := readDataLine(t, resp.Body, time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("event delivered after %s, want < 1s", elapsed)
	}
	if id != ev.ID {
		t.Errorf("id = %q, want %q", id, ev.ID)
	}
	if event != "needs_input" {
		t.Errorf("event = %q, want needs_input", event)
	}
	want := fmt.Sprintf(`{"type":"needs_input","id":%q}`, ev.ID)
	if data != want {
		t.Errorf("data = %q, want %q", data, want)
	}
}

func TestStreamLastEventIDReplay(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ts := httptest.NewServer(New(db).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev1, err := db.AppendEvent(ctx, store.Event{Type: "started"})
	if err != nil {
		t.Fatal(err)
	}
	ev2, err := db.AppendEvent(ctx, store.Event{Type: "finished"})
	if err != nil {
		t.Fatal(err)
	}

	// Replay after Last-Event-ID: only ev2 arrives, without waiting for new
	// events.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", ev1.ID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()
	id, event, data := readDataLine(t, resp.Body, time.Second)
	if id != ev2.ID || event != "finished" {
		t.Errorf("replayed id=%q event=%q, want %q finished", id, event, ev2.ID)
	}
	want := fmt.Sprintf(`{"type":"finished","id":%q}`, ev2.ID)
	if data != want {
		t.Errorf("data = %q, want %q", data, want)
	}
}

func TestStreamStartsFromNowWithoutHeader(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ts := httptest.NewServer(New(db).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.AppendEvent(ctx, store.Event{Type: "note"}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()

	// The pre-existing event must not be replayed; only the newly appended
	// one shows up.
	ev, err := db.AppendEvent(ctx, store.Event{Type: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	id, event, _ := readDataLine(t, resp.Body, time.Second)
	if id != ev.ID || event != "finished" {
		t.Errorf("first message id=%q event=%q, want %q finished (no replay)", id, event, ev.ID)
	}
}

func TestStreamTypeFilter(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ts := httptest.NewServer(New(db).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stream?types=note", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()

	if _, err := db.AppendEvent(ctx, store.Event{Type: "needs_input"}); err != nil {
		t.Fatal(err)
	}
	ev, err := db.AppendEvent(ctx, store.Event{Type: "note"})
	if err != nil {
		t.Fatal(err)
	}
	id, event, _ := readDataLine(t, resp.Body, time.Second)
	if id != ev.ID || event != "note" {
		t.Errorf("first message id=%q event=%q, want %q note (other types filtered)", id, event, ev.ID)
	}
}

func TestStreamWorkstreamFilter(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ts := httptest.NewServer(New(db).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, err := db.CreateWorkstream(ctx, store.Workstream{Title: "w"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := db.RegisterAgent(ctx, store.Agent{Role: "worker", Status: "running", WorkstreamID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stream?workstream_id="+ws.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()

	// An event from an agent outside the workstream is filtered out.
	ws2, err := db.CreateWorkstream(ctx, store.Workstream{Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.RegisterAgent(ctx, store.Agent{Role: "worker", Status: "running", WorkstreamID: ws2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AppendEvent(ctx, store.Event{Type: "note", AgentID: other.ID}); err != nil {
		t.Fatal(err)
	}
	ev, err := db.AppendEvent(ctx, store.Event{Type: "note", AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := readDataLine(t, resp.Body, time.Second)
	if id != ev.ID {
		t.Errorf("first message id=%q, want %q (foreign workstream filtered)", id, ev.ID)
	}
}
