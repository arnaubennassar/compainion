package api

import (
	"testing"

	"github.com/arnaubennassar/compainion/internal/store"
)

func TestTaskCreateAndGet(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)

	res := jdo(t, srv, "POST", "/tasks", map[string]any{
		"workstream_id": ws.ID, "requested_by": "user-1", "title": "T",
		"added_by": "user", "scope": map[string]any{"read": []string{"src"}},
	}, nil)
	if res.Status != 201 {
		t.Fatalf("create: %d body %s", res.Status, res.Body)
	}
	if res.Headers.Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q", res.Headers.Get("ETag"))
	}
	var tk struct {
		ID          string         `json:"id"`
		Status      string         `json:"status"`
		Scope       map[string]any `json:"scope"`
		RequestedBy string         `json:"requested_by"`
	}
	res.Unmarshal(t, &tk)
	if tk.Status != "pending" || tk.ID == "" || tk.RequestedBy != "user-1" {
		t.Fatalf("task = %+v", tk)
	}
	if tk.Scope["read"] == nil {
		t.Fatalf("scope not an object: %v", tk.Scope)
	}
	res = jdo(t, srv, "GET", "/tasks/"+tk.ID, nil, nil)
	if res.Status != 200 || res.Headers.Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag %q", res.Status, res.Headers.Get("ETag"))
	}
	if res := jdo(t, srv, "GET", "/tasks/missing", nil, nil); res.Status != 404 {
		t.Fatalf("get missing: %d", res.Status)
	}
	// validation: unknown field -> 400; missing workstream -> 400
	if res := jdo(t, srv, "POST", "/tasks", map[string]any{"title": "x", "bogus": 1}, nil); res.Status != 400 {
		t.Fatalf("unknown field: %d", res.Status)
	}
	if res := jdo(t, srv, "POST", "/tasks", map[string]any{"title": "x", "requested_by": "u", "added_by": "user"}, nil); res.Status != 400 {
		t.Fatalf("missing workstream: %d", res.Status)
	}
	_ = db
}

func TestTaskPatchAndDeleteRules(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	res := jdo(t, srv, "POST", "/tasks", map[string]any{
		"workstream_id": ws.ID, "requested_by": "u", "title": "T", "added_by": "user",
	}, nil)
	var tk struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &tk)
	worker := mustAgent(t, db, "worker")

	// PATCH requires If-Match
	if res := jdo(t, srv, "PATCH", "/tasks/"+tk.ID, map[string]string{"title": "T2"}, nil); res.Status != 428 {
		t.Fatalf("patch without If-Match: %d", res.Status)
	}
	// stale
	if res := jdo(t, srv, "PATCH", "/tasks/"+tk.ID, map[string]string{"title": "T2"}, map[string]string{"If-Match": `"5"`}); res.Status != 412 {
		t.Fatalf("stale If-Match: %d", res.Status)
	}
	// happy
	if res := jdo(t, srv, "PATCH", "/tasks/"+tk.ID, map[string]string{"title": "T2"}, map[string]string{"If-Match": `"1"`}); res.Status != 200 {
		t.Fatalf("patch: %d body %s", res.Status, res.Body)
	}
	var got struct {
		Title   string `json:"title"`
		Version int    `json:"version"`
	}
	jdo(t, srv, "GET", "/tasks/"+tk.ID, nil, nil).Unmarshal(t, &got)
	if got.Title != "T2" || got.Version != 2 {
		t.Fatalf("patched = %+v", got)
	}
	// claim then edit/delete -> 409
	if res := jdo(t, srv, "POST", "/tasks/"+tk.ID+"/claim", map[string]string{"agent_id": worker.ID}, nil); res.Status != 200 {
		t.Fatalf("claim: %d", res.Status)
	}
	if res := jdo(t, srv, "PATCH", "/tasks/"+tk.ID, map[string]string{"title": "no"}, map[string]string{"If-Match": `"2"`}); res.Status != 409 {
		t.Fatalf("edit non-pending: %d", res.Status)
	}
	if res := jdo(t, srv, "DELETE", "/tasks/"+tk.ID, nil, nil); res.Status != 409 {
		t.Fatalf("delete non-pending: %d", res.Status)
	}
}

func TestTaskClaimAndFinish(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	res := jdo(t, srv, "POST", "/tasks", map[string]any{
		"workstream_id": ws.ID, "requested_by": "u", "title": "T", "added_by": "user",
	}, nil)
	var tk struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &tk)
	worker := mustAgent(t, db, "worker")
	worker2 := mustAgent(t, db, "worker")

	res = jdo(t, srv, "POST", "/tasks/"+tk.ID+"/claim", map[string]string{"agent_id": worker.ID}, nil)
	if res.Status != 200 {
		t.Fatalf("claim: %d body %s", res.Status, res.Body)
	}
	var claimed struct {
		Status          string `json:"status"`
		Attempt         int    `json:"attempt"`
		AssigneeAgentID string `json:"assignee_agent_id"`
	}
	res.Unmarshal(t, &claimed)
	if claimed.Status != "in_progress" || claimed.Attempt != 1 || claimed.AssigneeAgentID == "" {
		t.Fatalf("claimed = %+v", claimed)
	}
	// second claim -> 409
	if res := jdo(t, srv, "POST", "/tasks/"+tk.ID+"/claim", map[string]string{"agent_id": worker2.ID}, nil); res.Status != 409 {
		t.Fatalf("second claim: %d", res.Status)
	}
	// done without evidence -> 422
	res = jdo(t, srv, "POST", "/tasks/"+tk.ID+"/finish", map[string]any{
		"status": "done", "outcome": map[string]any{"result": "ok"},
	}, nil)
	if res.Status != 422 {
		t.Fatalf("finish without evidence: %d body %s", res.Status, res.Body)
	}
	// happy finish
	res = jdo(t, srv, "POST", "/tasks/"+tk.ID+"/finish", map[string]any{
		"status": "done", "outcome": map[string]any{"result": "ok", "evidence": []string{"file exists"}},
	}, nil)
	if res.Status != 200 {
		t.Fatalf("finish: %d body %s", res.Status, res.Body)
	}
	var done struct {
		Status string `json:"status"`
	}
	res.Unmarshal(t, &done)
	if done.Status != "done" {
		t.Fatalf("finished = %+v", done)
	}
	// outcome renders as object
	var got struct {
		Outcome struct {
			Result   string   `json:"result"`
			Evidence []string `json:"evidence"`
		} `json:"outcome"`
	}
	jdo(t, srv, "GET", "/tasks/"+tk.ID, nil, nil).Unmarshal(t, &got)
	if got.Outcome.Result != "ok" || len(got.Outcome.Evidence) != 1 {
		t.Fatalf("outcome = %+v", got.Outcome)
	}
	// failed then claimed-work path: create, claim, finish failed (no evidence needed)
	res = jdo(t, srv, "POST", "/tasks", map[string]any{
		"workstream_id": ws.ID, "requested_by": "u", "title": "F", "added_by": "user",
	}, nil)
	var t2 struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &t2)
	if res := jdo(t, srv, "POST", "/tasks/"+t2.ID+"/claim", map[string]string{"agent_id": worker.ID}, nil); res.Status != 200 {
		t.Fatalf("claim: %d", res.Status)
	}
	if res := jdo(t, srv, "POST", "/tasks/"+t2.ID+"/finish", map[string]any{
		"status": "failed", "outcome": map[string]any{"result": "boom"},
	}, nil); res.Status != 200 {
		t.Fatalf("finish failed: %d body %s", res.Status, res.Body)
	}
}

func TestTaskDeletePending(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	res := jdo(t, srv, "POST", "/tasks", map[string]any{
		"workstream_id": ws.ID, "requested_by": "u", "title": "T", "added_by": "user",
	}, nil)
	var tk struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &tk)
	if res := jdo(t, srv, "DELETE", "/tasks/"+tk.ID, nil, nil); res.Status != 204 {
		t.Fatalf("delete: %d body %s", res.Status, res.Body)
	}
	if res := jdo(t, srv, "GET", "/tasks/"+tk.ID, nil, nil); res.Status != 404 {
		t.Fatalf("get deleted: %d", res.Status)
	}
	if res := jdo(t, srv, "DELETE", "/tasks/missing", nil, nil); res.Status != 404 {
		t.Fatalf("delete missing: %d", res.Status)
	}
}

func TestTaskListFilterAndPagination(t *testing.T) {
	db, srv := newAPI(t)
	ws1 := mustWorkstream(t, db)
	ws2 := mustWorkstream(t, db)
	mk := func(ws store.Workstream, title string) string {
		res := jdo(t, srv, "POST", "/tasks", map[string]any{
			"workstream_id": ws.ID, "requested_by": "u", "title": title, "added_by": "user",
		}, nil)
		var tk struct {
			ID string `json:"id"`
		}
		res.Unmarshal(t, &tk)
		return tk.ID
	}
	mk(ws1, "one")
	mk(ws1, "two")
	other := mk(ws2, "other")

	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	res := jdo(t, srv, "GET", "/tasks?workstream_id="+ws1.ID+"&limit=1", nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page1 = %+v", page)
	}
	first := page.Items[0].ID
	res = jdo(t, srv, "GET", "/tasks?workstream_id="+ws1.ID+"&limit=1&cursor="+page.NextCursor, nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID == first || page.NextCursor != "" {
		t.Fatalf("page2 = %+v", page)
	}
	// workstream filter excludes other tasks
	res = jdo(t, srv, "GET", "/tasks?workstream_id="+ws2.ID, nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != other {
		t.Fatalf("filter = %+v", page.Items)
	}
}
