package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arnaubennassar/compainion/internal/store"
)

// newAPI starts an httptest server over a fresh in-memory store (testutil
// imports api, so the api package's own tests build the server directly).
func newItAPI(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv := httptest.NewServer(New(db).Handler())
	t.Cleanup(srv.Close)
	return srv
}

type itResult struct {
	Status int
	Body   []byte
}

func (r itResult) Unmarshal(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode response %q: %v", r.Body, err)
	}
}

// do issues a JSON request against srv.
func itDo(t *testing.T, srv *httptest.Server, method, path string, body any, _ any) itResult {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return itResult{Status: resp.StatusCode, Body: buf.Bytes()}
}

func itBody(topic, priority string, blocking bool) map[string]any {
	return map[string]any{
		"topic":    topic,
		"kind":     "decision",
		"priority": priority,
		"digest":   "digest for " + topic,
		"blocking": blocking,
		"questions": []map[string]any{{
			"text":        "Proceed?",
			"answer_type": "confirm",
			"suggestions": []map[string]any{
				{"label": "Yes", "recommended": true},
				{"label": "No"},
			},
		}},
	}
}

func TestInterruptionCreate(t *testing.T) {
	srv := newItAPI(t)
	t.Run("created", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/interruptions", itBody("plan-approval", "high", true), nil)
		if res.Status != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", res.Status, res.Body)
		}
		var it store.Interruption
		res.Unmarshal(t, &it)
		if it.ID == "" || it.Status != "open" || it.Topic != "plan-approval" {
			t.Fatalf("unexpected interruption %+v", it)
		}
		if len(it.Questions) != 1 || len(it.Questions[0].Suggestions) != 2 {
			t.Fatalf("questions not expanded: %+v", it.Questions)
		}
	})
	t.Run("question without suggestion is 422", func(t *testing.T) {
		body := itBody("t2", "normal", true)
		body["questions"] = []map[string]any{{"text": "?", "answer_type": "confirm"}}
		res := itDo(t, srv, "POST", "/interruptions", body, nil)
		if res.Status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %s", res.Status, res.Body)
		}
		if !strings.Contains(string(res.Body), `"status":422`) {
			t.Fatalf("body is not problem+json: %s", res.Body)
		}
	})
	t.Run("unknown fields rejected", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/interruptions", map[string]any{"nope": 1}, nil)
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", res.Status)
		}
	})
}

func TestInterruptionGetListDelete(t *testing.T) {
	srv := newItAPI(t)
	var a, b store.Interruption
	itDo(t, srv, "POST", "/interruptions", itBody("ws-topic", "normal", true), nil).Unmarshal(t, &a)
	body := itBody("other-topic", "low", false)
	body["blocking"] = false
	itDo(t, srv, "POST", "/interruptions", body, nil).Unmarshal(t, &b)

	t.Run("get expanded", func(t *testing.T) {
		res := itDo(t, srv, "GET", "/interruptions/"+a.ID, nil, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d", res.Status)
		}
		var it store.Interruption
		res.Unmarshal(t, &it)
		if len(it.Questions) != 1 || it.Questions[0].Status != "open" {
			t.Fatalf("not expanded: %+v", it)
		}
	})
	t.Run("get unknown 404", func(t *testing.T) {
		if got := itDo(t, srv, "GET", "/interruptions/01NOPE", nil, nil).Status; got != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", got)
		}
	})
	t.Run("list filter by topic", func(t *testing.T) {
		var page struct {
			Items []store.Interruption `json:"items"`
		}
		itDo(t, srv, "GET", "/interruptions?topic=ws-topic", nil, nil).Unmarshal(t, &page)
		if len(page.Items) != 1 || page.Items[0].ID != a.ID {
			t.Fatalf("filter topic failed: %+v", page.Items)
		}
	})
	t.Run("list filter by status presented is empty", func(t *testing.T) {
		var page struct {
			Items []store.Interruption `json:"items"`
		}
		itDo(t, srv, "GET", "/interruptions?status=presented", nil, nil).Unmarshal(t, &page)
		if len(page.Items) != 0 {
			t.Fatalf("expected empty, got %+v", page.Items)
		}
	})
	t.Run("delete then 404", func(t *testing.T) {
		if got := itDo(t, srv, "DELETE", "/interruptions/"+b.ID, nil, nil).Status; got != http.StatusNoContent {
			t.Fatalf("delete status = %d, want 204", got)
		}
		if got := itDo(t, srv, "GET", "/interruptions/"+b.ID, nil, nil).Status; got != http.StatusNotFound {
			t.Fatalf("after delete GET status = %d, want 404", got)
		}
	})
}

func TestInterruptionNext(t *testing.T) {
	srv := newItAPI(t)

	t.Run("204 when none", func(t *testing.T) {
		if got := itDo(t, srv, "GET", "/interruptions/next", nil, nil).Status; got != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", got)
		}
	})

	var urgent, normal, batch1, batch2 store.Interruption
	mk := func(pr, topic string, blocking bool) store.Interruption {
		var it store.Interruption
		itDo(t, srv, "POST", "/interruptions", itBody(topic, pr, blocking), nil).Unmarshal(t, &it)
		return it
	}
	batch1 = mk("low", "findings-batch", true)
	batch2 = mk("low", "findings-batch", true)
	urgent = mk("urgent", "solo", false)
	normal = mk("normal", "solo", true)

	t.Run("urgent before normal, status presented, batch included", func(t *testing.T) {
		res := itDo(t, srv, "GET", "/interruptions/next", nil, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var it store.Interruption
		res.Unmarshal(t, &it)
		if it.ID != urgent.ID {
			t.Fatalf("expected urgent %s first, got %s (%s)", urgent.ID, it.ID, it.Priority)
		}
		if it.Status != "presented" {
			t.Fatalf("status = %q, want presented", it.Status)
		}
	})
	t.Run("second next returns the other one", func(t *testing.T) {
		var it store.Interruption
		itDo(t, srv, "GET", "/interruptions/next", nil, nil).Unmarshal(t, &it)
		if it.ID != normal.ID {
			t.Fatalf("expected normal %s, got %s", normal.ID, it.ID)
		}
		if len(it.Batch) != 0 {
			t.Fatalf("solo interruption should have empty batch, got %v", it.Batch)
		}
	})
	t.Run("batch lists same-topic open ids", func(t *testing.T) {
		var it store.Interruption
		itDo(t, srv, "GET", "/interruptions/next", nil, nil).Unmarshal(t, &it)
		if it.ID != batch1.ID && it.ID != batch2.ID {
			t.Fatalf("expected one of the batch pair, got %s", it.ID)
		}
		if len(it.Batch) != 1 || (it.Batch[0] != batch1.ID && it.Batch[0] != batch2.ID) {
			t.Fatalf("batch = %v, want exactly the sibling id", it.Batch)
		}
	})
	t.Run("drained", func(t *testing.T) {
		// one left (the second of the batch pair)
		var it store.Interruption
		itDo(t, srv, "GET", "/interruptions/next", nil, nil).Unmarshal(t, &it)
		if it.ID == "" {
			t.Fatalf("expected the last one")
		}
		if got := itDo(t, srv, "GET", "/interruptions/next", nil, nil).Status; got != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 after drain", got)
		}
	})
}

func TestInterruptionNextWait(t *testing.T) {
	srv := newItAPI(t)

	t.Run("times out with 204 after the wait", func(t *testing.T) {
		start := time.Now()
		res := itDo(t, srv, "GET", "/interruptions/next?wait=1", nil, nil)
		elapsed := time.Since(start)
		if res.Status != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", res.Status)
		}
		if elapsed < time.Second {
			t.Fatalf("elapsed = %v, want >= 1s", elapsed)
		}
	})
	t.Run("returns promptly when one arrives concurrently", func(t *testing.T) {
		go func() {
			time.Sleep(150 * time.Millisecond)
			itDo(t, srv, "POST", "/interruptions", itBody("wake", "high", true), nil)
		}()
		start := time.Now()
		res := itDo(t, srv, "GET", "/interruptions/next?wait=5", nil, nil)
		elapsed := time.Since(start)
		var it store.Interruption
		res.Unmarshal(t, &it)
		if res.Status != http.StatusOK || it.Topic != "wake" {
			t.Fatalf("status = %d topic = %q body %s", res.Status, it.Topic, res.Body)
		}
		if elapsed >= 2*time.Second {
			t.Fatalf("long poll did not return promptly: %v", elapsed)
		}
	})
}

func TestInterruptionAnswers(t *testing.T) {
	srv := newItAPI(t)
	var it store.Interruption
	itDo(t, srv, "POST", "/interruptions", itBody("qa", "normal", true), nil).Unmarshal(t, &it)
	q := it.Questions[0]
	rec := q.Suggestions[0]
	other := q.Suggestions[1]

	t.Run("suggestion mode needs a suggestion of that question", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/interruptions/"+it.ID+"/answers",
			map[string]any{"question_id": q.ID, "author": "user", "mode": "suggestion"}, nil)
		if res.Status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %s", res.Status, res.Body)
		}
	})
	t.Run("final suggestion answer closes question and interruption", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/interruptions/"+it.ID+"/answers",
			map[string]any{"question_id": q.ID, "author": "user", "mode": "suggestion", "suggestion_id": rec.ID, "final": true}, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var out store.Interruption
		res.Unmarshal(t, &out)
		if out.Questions[0].Status != "answered" {
			t.Fatalf("question status = %q, want answered", out.Questions[0].Status)
		}
		if out.Status != "answered" || out.AnsweredAt == nil {
			t.Fatalf("interruption not answered: %+v", out)
		}
		if len(out.Questions[0].Answers) != 1 || out.Questions[0].Answers[0].SuggestionID == nil {
			t.Fatalf("answers not recorded: %+v", out.Questions[0].Answers)
		}
	})
	t.Run("answering an answered interruption is 409", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/interruptions/"+it.ID+"/answers",
			map[string]any{"question_id": q.ID, "author": "companion", "mode": "free", "text": "hi"}, nil)
		if res.Status != http.StatusConflict {
			t.Fatalf("status = %d, want 409", res.Status)
		}
	})
	t.Run("unknown question 404", func(t *testing.T) {
		var it2 store.Interruption
		itDo(t, srv, "POST", "/interruptions", itBody("qa2", "normal", true), nil).Unmarshal(t, &it2)
		res := itDo(t, srv, "POST", "/interruptions/"+it2.ID+"/answers",
			map[string]any{"question_id": "01NOPE", "author": "user", "mode": "free", "text": "x", "final": true}, nil)
		if res.Status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", res.Status)
		}
	})
	t.Run("suggestion of another question rejected", func(t *testing.T) {
		var it3 store.Interruption
		itDo(t, srv, "POST", "/interruptions", itBody("qa3", "normal", true), nil).Unmarshal(t, &it3)
		res := itDo(t, srv, "POST", "/interruptions/"+it3.ID+"/answers",
			map[string]any{"question_id": it3.Questions[0].ID, "author": "user", "mode": "suggestion", "suggestion_id": other.ID}, nil)
		// other belongs to the first interruption's question, not this one.
		if res.Status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", res.Status)
		}
	})
}

func TestInterruptionDismissReopen(t *testing.T) {
	srv := newItAPI(t)
	var open, presented, answered store.Interruption
	itDo(t, srv, "POST", "/interruptions", itBody("d1", "normal", true), nil).Unmarshal(t, &open)
	itDo(t, srv, "POST", "/interruptions", itBody("d2", "normal", true), nil).Unmarshal(t, &presented)
	itDo(t, srv, "POST", "/interruptions", itBody("d3", "normal", true), nil).Unmarshal(t, &answered)
	// present d2 and d3
	itDo(t, srv, "GET", "/interruptions/next", nil, nil)
	itDo(t, srv, "GET", "/interruptions/next", nil, nil)
	itDo(t, srv, "GET", "/interruptions/next", nil, nil)
	// answer d3 fully
	for _, s := range answered.Questions {
		itDo(t, srv, "POST", "/interruptions/"+answered.ID+"/answers",
			map[string]any{"question_id": s.ID, "author": "user", "mode": "free", "text": "ok", "final": true}, nil)
	}
	// wrong question ids: answered.Questions elements from the create response
	// actually have the real ids; redo with the loaded one
	var loaded store.Interruption
	itDo(t, srv, "GET", "/interruptions/"+answered.ID, nil, nil).Unmarshal(t, &loaded)
	itDo(t, srv, "POST", "/interruptions/"+answered.ID+"/answers",
		map[string]any{"question_id": loaded.Questions[0].ID, "author": "user", "mode": "free", "text": "ok", "final": true}, nil)

	t.Run("dismiss open", func(t *testing.T) {
		res := itDo(t, srv, "POST", "/interruptions/"+open.ID+"/dismiss", nil, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var it store.Interruption
		res.Unmarshal(t, &it)
		if it.Status != "dismissed" {
			t.Fatalf("status = %q, want dismissed", it.Status)
		}
	})
	t.Run("dismiss presented", func(t *testing.T) {
		var it store.Interruption
		itDo(t, srv, "POST", "/interruptions/"+presented.ID+"/dismiss", nil, nil).Unmarshal(t, &it)
		if it.Status != "dismissed" {
			t.Fatalf("status = %q, want dismissed", it.Status)
		}
	})
	t.Run("dismiss answered is 409", func(t *testing.T) {
		if got := itDo(t, srv, "POST", "/interruptions/"+answered.ID+"/dismiss", nil, nil).Status; got != http.StatusConflict {
			t.Fatalf("status = %d, want 409", got)
		}
	})
	t.Run("reopen presented back to open", func(t *testing.T) {
		var again store.Interruption
		itDo(t, srv, "POST", "/interruptions", itBody("r1", "normal", true), nil).Unmarshal(t, &again)
		itDo(t, srv, "GET", "/interruptions/next", nil, nil) // present it
		res := itDo(t, srv, "POST", "/interruptions/"+again.ID+"/reopen", nil, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", res.Status, res.Body)
		}
		var it store.Interruption
		res.Unmarshal(t, &it)
		if it.Status != "open" {
			t.Fatalf("status = %q, want open", it.Status)
		}
		// it is presentable again
		var next store.Interruption
		itDo(t, srv, "GET", "/interruptions/next", nil, nil).Unmarshal(t, &next)
		if next.ID != again.ID {
			t.Fatalf("reopened interruption not presentable: got %q", next.ID)
		}
	})
	t.Run("reopen dismissed is 409", func(t *testing.T) {
		if got := itDo(t, srv, "POST", "/interruptions/"+open.ID+"/reopen", nil, nil).Status; got != http.StatusConflict {
			t.Fatalf("status = %d, want 409", got)
		}
	})
}
