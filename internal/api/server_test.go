package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/arnaubennassar/compainion/internal/store"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	s := New(db)
	// Test-only routes exercising the helpers.
	s.handle("GET /_ifmatch", func(w http.ResponseWriter, r *http.Request) {
		v, err := parseIfMatch(r)
		if err != nil {
			writeProblem(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"version": v})
	})
	s.handle("POST /_echo", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeProblem(w, err)
			return
		}
		w.Header().Set("X-Echo", string(b))
		writeJSON(w, http.StatusCreated, map[string]string{"body": string(b)})
	})
	s.handle("POST /_decode", func(w http.ResponseWriter, r *http.Request) {
		in, err := decode[struct {
			Name string `json:"name"`
		}](r)
		if err != nil {
			writeProblem(w, err)
			return
		}
		writeJSON(w, http.StatusOK, in)
	})
	s.handle("GET /_page", func(w http.ResponseWriter, r *http.Request) {
		limit, cursor, err := parsePage(r)
		if err != nil {
			writeProblem(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"limit": limit, "cursor": cursor})
	})
	s.handle("GET /_panic", func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	return s.Handler()
}

func do(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) (int, string, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := &recorder{header: http.Header{}}
	h.ServeHTTP(rec, req)
	return rec.code, rec.body, rec.header
}

type recorder struct {
	header http.Header
	code   int
	body   string
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(c int)           { r.code = c }
func (r *recorder) Write(b []byte) (int, error) { r.body += string(b); return len(b), nil }

func TestHealthz(t *testing.T) {
	h := newTestServer(t)
	code, body, hdr := do(t, h, "GET", "/healthz", "", nil)
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	if body != `{"status":"ok"}`+"\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestNotFoundProblem(t *testing.T) {
	h := newTestServer(t)
	code, body, hdr := do(t, h, "GET", "/definitely-not-a-route", "", nil)
	if code != 404 {
		t.Fatalf("status = %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	var prob struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(body), &prob); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, body)
	}
	if prob.Status != 404 || prob.Type == "" || prob.Title == "" || prob.Detail == "" {
		t.Fatalf("problem incomplete: %+v", prob)
	}
}

func TestParseIfMatch(t *testing.T) {
	h := newTestServer(t)
	cases := []struct {
		hdr      string
		wantCode int
		wantVer  int
	}{
		{hdr: `"7"`, wantCode: 200, wantVer: 7},
		{hdr: "", wantCode: 428},
		{hdr: `abc`, wantCode: 400},
		{hdr: `"0"`, wantCode: 400},
		{hdr: `"-3"`, wantCode: 400},
	}
	for _, c := range cases {
		hdrs := map[string]string{}
		if c.hdr != "" {
			hdrs["If-Match"] = c.hdr
		}
		code, body, _ := do(t, h, "GET", "/_ifmatch", "", hdrs)
		if code != c.wantCode {
			t.Fatalf("If-Match %q: status = %d, want %d (body %s)", c.hdr, code, c.wantCode, body)
		}
		if c.wantCode == 200 {
			var got struct {
				Version int `json:"version"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil || got.Version != c.wantVer {
				t.Fatalf("If-Match %q: version = %d want %d (%v)", c.hdr, got.Version, c.wantVer, err)
			}
		}
	}
}

func TestIdempotency(t *testing.T) {
	h := newTestServer(t)
	key := map[string]string{"Idempotency-Key": "k1", "Content-Type": "application/json"}

	// First POST stores the response.
	code, body, _ := do(t, h, "POST", "/_echo", `{"a":1}`, key)
	if code != 201 {
		t.Fatalf("first POST status = %d", code)
	}
	// Same key + same body -> replay.
	code2, body2, hdr2 := do(t, h, "POST", "/_echo", `{"a":1}`, key)
	if code2 != 201 || body2 != body {
		t.Fatalf("replay: status=%d body=%q (first body %q)", code2, body2, body)
	}
	if hdr2.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("missing Idempotent-Replayed header")
	}
	// Same key + different body -> 422 problem.
	code3, _, hdr3 := do(t, h, "POST", "/_echo", `{"a":2}`, key)
	if code3 != 422 {
		t.Fatalf("conflict status = %d", code3)
	}
	if ct := hdr3.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("conflict content-type = %q", ct)
	}
	// No header: no caching, two independent responses.
	c1, _, _ := do(t, h, "POST", "/_echo", `{"a":1}`, nil)
	c2, _, _ := do(t, h, "POST", "/_echo", `{"a":1}`, nil)
	if c1 != 201 || c2 != 201 {
		t.Fatalf("no-key POSTs: %d %d", c1, c2)
	}
}

func TestDecodeStrict(t *testing.T) {
	h := newTestServer(t)
	code, _, _ := do(t, h, "POST", "/_decode", `{"name":"x"}`, map[string]string{"Content-Type": "application/json"})
	if code != 200 {
		t.Fatalf("valid body status = %d", code)
	}
	code, body, hdr := do(t, h, "POST", "/_decode", `{"name":"x","extra":1}`, map[string]string{"Content-Type": "application/json"})
	if code != 400 {
		t.Fatalf("unknown field status = %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(body, "extra") {
		t.Fatalf("detail should mention the field: %s", body)
	}
}

func TestParsePage(t *testing.T) {
	h := newTestServer(t)
	cases := []struct {
		q        string
		wantCode int
		limit    int
		cursor   string
	}{
		{q: "", wantCode: 200, limit: 50},
		{q: "?limit=200", wantCode: 200, limit: 200},
		{q: "?limit=201", wantCode: 400},
		{q: "?limit=0", wantCode: 400},
		{q: "?limit=abc", wantCode: 400},
		{q: "?cursor=ev01", wantCode: 200, limit: 50, cursor: "ev01"},
	}
	for _, c := range cases {
		code, body, _ := do(t, h, "GET", "/_page"+c.q, "", nil)
		if code != c.wantCode {
			t.Fatalf("%q: status = %d, want %d (body %s)", c.q, code, c.wantCode, body)
		}
		if c.wantCode == 200 {
			var got struct {
				Limit  int    `json:"limit"`
				Cursor string `json:"cursor"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil || got.Limit != c.limit || got.Cursor != c.cursor {
				t.Fatalf("%q: got %+v (err %v), want limit %d cursor %q", c.q, got, err, c.limit, c.cursor)
			}
		}
	}
}

func TestPanicRecovery(t *testing.T) {
	h := newTestServer(t)
	code, _, hdr := do(t, h, "GET", "/_panic", "", nil)
	if code != 500 {
		t.Fatalf("status = %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestEnvelopeShape(t *testing.T) {
	env := Envelope{Items: []string{"a"}, NextCursor: "z"}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"items":["a"],"next_cursor":"z"}`)
	if string(b) != want {
		t.Fatalf("envelope = %s, want %s", b, want)
	}
}
