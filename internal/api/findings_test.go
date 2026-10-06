package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/arnaubennassar/compainion/internal/store"
)

// findingJSON mirrors the wire shape (evidence is a JSON array).
type findingJSON struct {
	store.Finding
	Evidence []map[string]any `json:"evidence"`
	Created  bool             `json:"created"`
}

func fBody(title string) map[string]any {
	return map[string]any{
		"category": "bug",
		"severity": "high",
		"location": "internal/store/steps.go:12",
		"title":    title,
		"details":  "  Too MANY logs!! ",
		"evidence": []map[string]any{{"line": 12, "snippet": "m[k]=v"}},
	}
}

func TestFindingCreate(t *testing.T) {
	srv := newItAPI(t)

	t.Run("201 on create", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings", fBody("Nil map write"), nil)
		if res.Status != http.StatusCreated {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var f findingJSON
		res.Unmarshal(t, &f)
		if f.ID == "" || f.Occurrences != 1 || f.Status != "new" || f.Fingerprint == "" {
			t.Fatalf("unexpected finding %+v", f)
		}
		if !strings.Contains(string(res.Body), `"created":true`) {
			t.Fatalf("missing created:true: %s", res.Body)
		}
		if strings.Contains(string(res.Body), `"evidence":"`) || strings.Contains(string(res.Body), `"evidence":null`) {
			t.Fatalf("evidence not a JSON array: %s", res.Body)
		}
	})
	t.Run("200 on dedupe with occurrences bumped", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings", fBody("nil map WRITE"), nil) // same after normalisation
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var f findingJSON
		res.Unmarshal(t, &f)
		if f.Occurrences != 2 {
			t.Fatalf("occurrences = %d, want 2", f.Occurrences)
		}
		if !strings.Contains(string(res.Body), `"created":false`) {
			t.Fatalf("missing created:false: %s", res.Body)
		}
	})
	t.Run("missing category is 400", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings", map[string]any{"title": "x", "details": "y"}, nil)
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", res.Status, res.Body)
		}
	})
}

func TestFindingGetList(t *testing.T) {
	srv := newItAPI(t)
	var f findingJSON
	itDo(t, srv, "POST", "/findings", fBody("Flaky retry"), nil).Unmarshal(t, &f)

	t.Run("get", func(t *testing.T) {
		var got findingJSON
		itDo(t, srv, "GET", "/findings/"+f.ID, nil, nil).Unmarshal(t, &got)
		if got.ID != f.ID || got.Title != "Flaky retry" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("get unknown 404", func(t *testing.T) {
		if got := itDo(t, srv, "GET", "/findings/01NOPE", nil, nil).Status; got != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", got)
		}
	})
	t.Run("list query matches title case-insensitively", func(t *testing.T) {
		var page struct {
			Items []findingJSON `json:"items"`
		}
		itDo(t, srv, "GET", "/findings?query=flaky", nil, nil).Unmarshal(t, &page)
		if len(page.Items) != 1 || page.Items[0].ID != f.ID {
			t.Fatalf("query filter failed: %+v", page.Items)
		}
		var empty struct {
			Items []findingJSON `json:"items"`
		}
		itDo(t, srv, "GET", "/findings?query=zzz", nil, nil).Unmarshal(t, &empty)
		if len(empty.Items) != 0 {
			t.Fatalf("expected no matches, got %+v", empty.Items)
		}
	})
	t.Run("list status filter", func(t *testing.T) {
		var page struct {
			Items []findingJSON `json:"items"`
		}
		itDo(t, srv, "GET", "/findings?status=surfaced", nil, nil).Unmarshal(t, &page)
		if len(page.Items) != 0 {
			t.Fatalf("expected empty for status=surfaced, got %+v", page.Items)
		}
	})
}

func TestFindingResolve(t *testing.T) {
	srv := newItAPI(t)
	var f findingJSON
	itDo(t, srv, "POST", "/findings", fBody("Resolve me"), nil).Unmarshal(t, &f)

	t.Run("missing resolution is 400", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings/"+f.ID+"/resolve", map[string]any{}, nil)
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", res.Status)
		}
	})
	t.Run("issue_opened without ref is 422", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings/"+f.ID+"/resolve", map[string]any{"resolution": "issue_opened"}, nil)
		if res.Status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", res.Status)
		}
	})
	t.Run("step_added with unknown step is 404", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings/"+f.ID+"/resolve",
			map[string]any{"resolution": "step_added", "resolution_ref": "01NOPE"}, nil)
		if res.Status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", res.Status)
		}
	})
	t.Run("ignored resolves", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings/"+f.ID+"/resolve", map[string]any{"resolution": "ignored"}, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var got findingJSON
		res.Unmarshal(t, &got)
		if got.Status != "resolved" || got.Resolution == nil || *got.Resolution != "ignored" {
			t.Fatalf("not resolved: %+v", got)
		}
	})
	t.Run("re-resolving is 409", func(t *testing.T) {
		if got := itDo(t, srv, "POST", "/findings/"+f.ID+"/resolve", map[string]any{"resolution": "ignored"}, nil).Status; got != http.StatusConflict {
			t.Fatalf("status = %d, want 409", got)
		}
	})
}

func TestFindingSurface(t *testing.T) {
	srv := newItAPI(t)
	var f findingJSON
	itDo(t, srv, "POST", "/findings", fBody("Surface me"), nil).Unmarshal(t, &f)
	var it store.Interruption
	itDo(t, srv, "POST", "/interruptions", itBody("finding", "low", false), nil).Unmarshal(t, &it)

	t.Run("surface marks and links", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/findings/"+f.ID+"/surface",
			map[string]any{"interruption_id": it.ID}, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var got findingJSON
		res.Unmarshal(t, &got)
		if got.Status != "surfaced" || got.InterruptionID == nil || *got.InterruptionID != it.ID {
			t.Fatalf("not surfaced: %+v", got)
		}
	})
	t.Run("unknown finding 404", func(t *testing.T) {
		if got := itDo(t, srv, "POST", "/findings/01NOPE/surface",
			map[string]any{"interruption_id": it.ID}, nil).Status; got != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", got)
		}
	})
}
