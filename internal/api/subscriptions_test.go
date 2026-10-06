package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/arnaubennassar/compainion/internal/api"
	"github.com/arnaubennassar/compainion/internal/store"
	"github.com/arnaubennassar/compainion/internal/testutil"
)

type subDTO struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Types  json.RawMessage `json:"types"`
	Secret *string         `json:"secret"`
	Active bool            `json:"active"`
}

func TestSubscriptions(t *testing.T) {
	srv := testutil.NewServer(t)

	// POST happy path.
	res := testutil.Do(t, srv, "POST", "/subscriptions", map[string]any{
		"method": "webhook", "target": "http://127.0.0.1:9/hook",
		"types": []string{"needs_input"}, "filter": map[string]any{"kind": []string{"interruption_created"}},
		"secret": "s3cret",
	}, nil)
	if res.Status != 201 {
		t.Fatalf("POST /subscriptions status = %d, want 201: %s", res.Status, res.Body)
	}
	var sub subDTO
	res.Unmarshal(t, &sub)
	if sub.ID == "" || sub.Method != "webhook" || sub.Secret == nil || !sub.Active {
		t.Fatalf("created subscription = %+v", sub)
	}

	// POST same method+target -> 200 with the SAME id (idempotent upsert).
	res = testutil.Do(t, srv, "POST", "/subscriptions", map[string]any{
		"method": "webhook", "target": "http://127.0.0.1:9/hook",
		"types": []string{"note"},
	}, nil)
	if res.Status != 200 {
		t.Fatalf("idempotent upsert status = %d, want 200: %s", res.Status, res.Body)
	}
	var sub2 subDTO
	res.Unmarshal(t, &sub2)
	if sub2.ID != sub.ID {
		t.Fatalf("upsert changed id: %s -> %s", sub.ID, sub2.ID)
	}

	// POST invalid method -> 422.
	res = testutil.Do(t, srv, "POST", "/subscriptions", map[string]any{
		"method": "carrier-pigeon", "target": "x", "types": []string{"note"},
	}, nil)
	if res.Status != 422 {
		t.Fatalf("invalid method status = %d, want 422", res.Status)
	}

	// POST missing target -> 400 (store Invalid).
	res = testutil.Do(t, srv, "POST", "/subscriptions", map[string]any{
		"method": "webhook", "types": []string{"note"},
	}, nil)
	if res.Status != 400 {
		t.Fatalf("missing target status = %d, want 400", res.Status)
	}

	// GET list.
	res = testutil.Do(t, srv, "GET", "/subscriptions", nil, nil)
	var page struct {
		Items []subDTO `json:"items"`
	}
	if res.Status != 200 {
		t.Fatalf("GET /subscriptions status = %d", res.Status)
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != sub.ID {
		t.Fatalf("GET /subscriptions = %+v", page.Items)
	}

	// GET one + 404.
	res = testutil.Do(t, srv, "GET", "/subscriptions/"+sub.ID, nil, nil)
	if res.Status != 200 {
		t.Fatalf("GET subscription status = %d", res.Status)
	}
	if res := testutil.Do(t, srv, "GET", "/subscriptions/nope", nil, nil); res.Status != 404 {
		t.Fatalf("GET missing subscription status = %d, want 404", res.Status)
	}

	// DELETE -> 204 then 404.
	if res := testutil.Do(t, srv, "DELETE", "/subscriptions/"+sub.ID, nil, nil); res.Status != 204 {
		t.Fatalf("DELETE subscription status = %d, want 204", res.Status)
	}
	if res := testutil.Do(t, srv, "DELETE", "/subscriptions/"+sub.ID, nil, nil); res.Status != 404 {
		t.Fatalf("DELETE missing subscription status = %d, want 404", res.Status)
	}
}

func TestOpenapiServed(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	srv := httptest.NewServer(api.New(db).Handler())
	defer srv.Close()

	res := testutil.Do(t, srv, "GET", "/openapi.yaml", nil, nil)
	if res.Status != 200 {
		t.Fatalf("GET /openapi.yaml status = %d", res.Status)
	}
	if ct := res.Headers.Get("Content-Type"); ct != "application/yaml" {
		t.Fatalf("content-type = %q, want application/yaml", ct)
	}
	if len(res.Body) == 0 || string(res.Body[:8]) != "openapi:" {
		t.Fatalf("body does not look like the spec: %q", string(res.Body[:min(40, len(res.Body))]))
	}
}
