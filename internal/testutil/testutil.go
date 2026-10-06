// Package testutil provides an in-memory API server and a small JSON client
// helper for HTTP-level tests.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arnaubennassar/compainion/internal/api"
	"github.com/arnaubennassar/compainion/internal/store"
)

// NewServer starts an httptest.Server backed by an in-memory store and
// registers a cleanup on t.
func NewServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("testutil: open store: %v", err)
	}
	srv := httptest.NewServer(api.New(db).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// Result is the outcome of a JSON request.
type Result struct {
	Status  int
	Body    []byte
	Headers http.Header
}

// Unmarshal decodes the response body into v.
func (r Result) Unmarshal(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("testutil: decode response %q: %v", r.Body, err)
	}
}

// Do issues a JSON request against the server. A non-nil body is marshalled to
// JSON; nil body sends no body. headers are set verbatim.
func Do(t *testing.T, srv *httptest.Server, method, path string, body any, headers map[string]string) Result {
	t.Helper()
	var rd *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("testutil: marshal request: %v", err)
		}
		rd = bytes.NewBuffer(b)
	} else {
		rd = bytes.NewBuffer(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("testutil: build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("testutil: %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("testutil: read response: %v", err)
	}
	if resp.StatusCode >= 500 {
		// Keep 5xx visible; tests should assert on status explicitly.
		_ = fmt.Sprintf
	}
	return Result{Status: resp.StatusCode, Body: buf.Bytes(), Headers: resp.Header}
}
