package api

import (
	"encoding/json"
	"testing"
)

func TestStepLifecycleRules(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)

	res := jdo(t, srv, "POST", "/plans/"+p.ID+"/steps", map[string]any{
		"kind": "task", "title": "A", "added_by": "planner",
		"scope": map[string]any{"read": []string{"src/a"}},
	}, nil)
	if res.Status != 201 {
		t.Fatalf("create step: %d body %s", res.Status, res.Body)
	}
	var s struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &s)
	id := s.ID
	worker := mustAgent(t, db, "worker")
	worker2 := mustAgent(t, db, "worker")

	// GET with ready + scope object + ETag
	res = jdo(t, srv, "GET", "/steps/"+id, nil, nil)
	if res.Status != 200 {
		t.Fatalf("get step: %d", res.Status)
	}
	var got struct {
		Status string         `json:"status"`
		Ready  bool           `json:"ready"`
		Scope  map[string]any `json:"scope"`
	}
	res.Unmarshal(t, &got)
	if got.Status != "pending" || !got.Ready {
		t.Fatalf("step = %+v", got)
	}
	if got.Scope["read"] == nil {
		t.Fatalf("scope not an object: %v", got.Scope)
	}
	if res.Headers.Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q", res.Headers.Get("ETag"))
	}

	// PATCH rules
	res = jdo(t, srv, "PATCH", "/steps/"+id, map[string]string{"title": "A2"}, nil)
	if res.Status != 428 {
		t.Fatalf("patch without If-Match: %d", res.Status)
	}
	res = jdo(t, srv, "PATCH", "/steps/"+id, map[string]string{"title": "A2"}, map[string]string{"If-Match": `"7"`})
	if res.Status != 412 {
		t.Fatalf("patch stale If-Match: %d", res.Status)
	}
	res = jdo(t, srv, "PATCH", "/steps/"+id, map[string]string{"title": "A2"}, map[string]string{"If-Match": `"1"`})
	if res.Status != 200 {
		t.Fatalf("patch: %d body %s", res.Status, res.Body)
	}
	var patched struct {
		Title   string `json:"title"`
		Version int    `json:"version"`
	}
	res.Unmarshal(t, &patched)
	if patched.Title != "A2" || patched.Version != 2 {
		t.Fatalf("patched = %+v", patched)
	}

	// claim
	res = jdo(t, srv, "POST", "/steps/"+id+"/claim", map[string]string{"agent_id": worker.ID}, nil)
	if res.Status != 200 {
		t.Fatalf("claim: %d body %s", res.Status, res.Body)
	}
	var claimed struct {
		Status          string `json:"status"`
		Attempt         int    `json:"attempt"`
		AssigneeAgentID string `json:"assignee_agent_id"`
		Version         int    `json:"version"`
	}
	res.Unmarshal(t, &claimed)
	if claimed.Status != "in_progress" || claimed.Attempt != 1 || claimed.Version != 3 {
		t.Fatalf("claimed = %+v", claimed)
	}
	// second claim -> 409
	if res := jdo(t, srv, "POST", "/steps/"+id+"/claim", map[string]string{"agent_id": worker2.ID}, nil); res.Status != 409 {
		t.Fatalf("second claim: %d", res.Status)
	}
	// edit non-pending -> 409
	if res := jdo(t, srv, "PATCH", "/steps/"+id, map[string]string{"title": "no"}, map[string]string{"If-Match": `"2"`}); res.Status != 409 {
		t.Fatalf("edit non-pending: %d", res.Status)
	}
	// delete non-pending -> 409
	if res := jdo(t, srv, "DELETE", "/steps/"+id, nil, nil); res.Status != 409 {
		t.Fatalf("delete non-pending: %d", res.Status)
	}
	// finish done without evidence -> 422
	res = jdo(t, srv, "POST", "/steps/"+id+"/finish", map[string]any{
		"status": "done", "outcome": map[string]any{"result": "ok"},
	}, nil)
	if res.Status != 422 {
		t.Fatalf("finish without evidence: %d body %s", res.Status, res.Body)
	}
	// bad status -> 400
	res = jdo(t, srv, "POST", "/steps/"+id+"/finish", map[string]any{
		"status": "pending", "outcome": map[string]any{"result": "ok"},
	}, nil)
	if res.Status != 400 {
		t.Fatalf("finish bad status: %d", res.Status)
	}
	// finish done with evidence -> 200
	res = jdo(t, srv, "POST", "/steps/"+id+"/finish", map[string]any{
		"status":  "done",
		"outcome": map[string]any{"result": "ok", "evidence": []string{"go test ok"}},
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
	// outcome renders as an object
	var withOutcome struct {
		Outcome struct {
			Result   string   `json:"result"`
			Evidence []string `json:"evidence"`
		} `json:"outcome"`
	}
	jdo(t, srv, "GET", "/steps/"+id, nil, nil).Unmarshal(t, &withOutcome)
	if withOutcome.Outcome.Result != "ok" || len(withOutcome.Outcome.Evidence) != 1 {
		t.Fatalf("outcome = %+v", withOutcome.Outcome)
	}
}

func TestStepClaimNotReady(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)

	res := jdo(t, srv, "POST", "/plans/"+p.ID+"/steps", map[string]any{
		"kind": "task", "title": "A", "added_by": "planner",
	}, nil)
	var a struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &a)
	worker := mustAgent(t, db, "worker")
	res = jdo(t, srv, "POST", "/plans/"+p.ID+"/steps", map[string]any{
		"kind": "task", "title": "B", "added_by": "planner", "deps": []string{a.ID},
	}, nil)
	var b struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &b)

	// B not ready -> 409
	if res := jdo(t, srv, "POST", "/steps/"+b.ID+"/claim", map[string]string{"agent_id": worker.ID}, nil); res.Status != 409 {
		t.Fatalf("claim not-ready: %d body %s", res.Status, res.Body)
	}
	// finish A done
	if res := jdo(t, srv, "POST", "/steps/"+a.ID+"/claim", map[string]string{"agent_id": worker.ID}, nil); res.Status != 200 {
		t.Fatalf("claim A: %d", res.Status)
	}
	if res := jdo(t, srv, "POST", "/steps/"+a.ID+"/finish", map[string]any{
		"status": "done", "outcome": map[string]any{"result": "ok", "evidence": []string{"e"}},
	}, nil); res.Status != 200 {
		t.Fatalf("finish A: %d body %s", res.Status, res.Body)
	}
	// steps/next returns only ready ones (goal not ready until B done)
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		} `json:"items"`
	}
	res = jdo(t, srv, "GET", "/plans/"+p.ID+"/steps/next", nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != b.ID || !page.Items[0].Ready {
		t.Fatalf("next = %+v, want only B ready", page.Items)
	}
	// now B is claimable
	if res := jdo(t, srv, "POST", "/steps/"+b.ID+"/claim", map[string]string{"agent_id": worker.ID}, nil); res.Status != 200 {
		t.Fatalf("claim B after A done: %d body %s", res.Status, res.Body)
	}
}

func TestStepDepsEndpoints(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)

	mkStep := func(title string) string {
		res := jdo(t, srv, "POST", "/plans/"+p.ID+"/steps", map[string]any{
			"kind": "task", "title": title, "added_by": "planner",
		}, nil)
		if res.Status != 201 {
			t.Fatalf("create %s: %d", title, res.Status)
		}
		var s struct {
			ID string `json:"id"`
		}
		res.Unmarshal(t, &s)
		return s.ID
	}
	a, b := mkStep("A"), mkStep("B")

	// missing If-Match -> 428
	res := jdo(t, srv, "POST", "/steps/"+b+"/deps", map[string]string{"depends_on_id": a}, nil)
	if res.Status != 428 {
		t.Fatalf("add dep without If-Match: %d", res.Status)
	}
	// stale If-Match -> 412
	res = jdo(t, srv, "POST", "/steps/"+b+"/deps", map[string]string{"depends_on_id": a},
		map[string]string{"If-Match": `"9"`})
	if res.Status != 412 {
		t.Fatalf("add dep stale If-Match: %d", res.Status)
	}
	// happy -> 204
	res = jdo(t, srv, "POST", "/steps/"+b+"/deps", map[string]string{"depends_on_id": a},
		map[string]string{"If-Match": `"1"`})
	if res.Status != 204 {
		t.Fatalf("add dep: %d body %s", res.Status, res.Body)
	}
	// duplicate dep -> 409
	res = jdo(t, srv, "POST", "/steps/"+b+"/deps", map[string]string{"depends_on_id": a},
		map[string]string{"If-Match": `"1"`})
	if res.Status != 409 {
		t.Fatalf("dup dep: %d body %s", res.Status, res.Body)
	}
	// GET shows the dep
	var got struct {
		Deps []string `json:"deps"`
	}
	jdo(t, srv, "GET", "/steps/"+b, nil, nil).Unmarshal(t, &got)
	if len(got.Deps) != 1 || got.Deps[0] != a {
		t.Fatalf("deps = %v", got.Deps)
	}
	// cycle: A depends on B while B depends on A -> 409
	res = jdo(t, srv, "POST", "/steps/"+a+"/deps", map[string]string{"depends_on_id": b},
		map[string]string{"If-Match": `"1"`})
	if res.Status != 409 {
		t.Fatalf("cycle dep: %d body %s", res.Status, res.Body)
	}
	// delete dep: 428 without If-Match
	res = jdo(t, srv, "DELETE", "/steps/"+b+"/deps/"+a, nil, nil)
	if res.Status != 428 {
		t.Fatalf("delete dep without If-Match: %d", res.Status)
	}
	res = jdo(t, srv, "DELETE", "/steps/"+b+"/deps/"+a, nil, map[string]string{"If-Match": `"1"`})
	if res.Status != 204 {
		t.Fatalf("delete dep: %d body %s", res.Status, res.Body)
	}
	// deleting a missing edge -> 404
	res = jdo(t, srv, "DELETE", "/steps/"+b+"/deps/"+a, nil, map[string]string{"If-Match": `"1"`})
	if res.Status != 404 {
		t.Fatalf("delete missing dep: %d", res.Status)
	}
	// goal deps are not editable -> 409
	var pl struct {
		GoalStepID string `json:"goal_step_id"`
	}
	jdo(t, srv, "GET", "/plans/"+p.ID, nil, nil).Unmarshal(t, &pl)
	res = jdo(t, srv, "POST", "/steps/"+pl.GoalStepID+"/deps", map[string]string{"depends_on_id": a},
		map[string]string{"If-Match": `"1"`})
	if res.Status != 409 {
		t.Fatalf("goal dep edit: %d body %s", res.Status, res.Body)
	}
}

func TestStepDeleteAndGoalProtection(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID         string `json:"id"`
		GoalStepID string `json:"goal_step_id"`
	}
	body.Unmarshal(t, &p)

	// goal can never be deleted (409 even with approval_ref available)
	ref := mustAnsweredInterruption(t, db, p.ID)
	if res := jdo(t, srv, "DELETE", "/steps/"+p.GoalStepID, nil, map[string]string{"X-Approval-Ref": ref}); res.Status != 409 {
		t.Fatalf("delete goal: %d", res.Status)
	}

	// pending step deletes -> 204
	res := jdo(t, srv, "POST", "/plans/"+p.ID+"/steps", map[string]any{
		"kind": "task", "title": "A", "added_by": "planner",
	}, nil)
	var s struct {
		ID string `json:"id"`
	}
	res.Unmarshal(t, &s)
	if res := jdo(t, srv, "DELETE", "/steps/"+s.ID, nil, nil); res.Status != 204 {
		t.Fatalf("delete pending: %d body %s", res.Status, res.Body)
	}
	if res := jdo(t, srv, "GET", "/steps/"+s.ID, nil, nil); res.Status != 404 {
		t.Fatalf("get deleted: %d", res.Status)
	}
	// unknown step -> 404
	if res := jdo(t, srv, "DELETE", "/steps/missing", nil, nil); res.Status != 404 {
		t.Fatalf("delete missing: %d", res.Status)
	}
}

func TestGoalStepEditRequiresApprovalRef(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID         string `json:"id"`
		GoalStepID string `json:"goal_step_id"`
	}
	body.Unmarshal(t, &p)

	// goal step PATCH without approval_ref -> 428
	res := jdo(t, srv, "PATCH", "/steps/"+p.GoalStepID, map[string]string{"title": "New Goal"},
		map[string]string{"If-Match": `"1"`})
	if res.Status != 428 {
		t.Fatalf("goal edit without approval_ref: %d body %s", res.Status, res.Body)
	}
	ref := mustAnsweredInterruption(t, db, p.ID)
	res = jdo(t, srv, "PATCH", "/steps/"+p.GoalStepID,
		map[string]any{"title": "New Goal", "approval_ref": ref},
		map[string]string{"If-Match": `"1"`})
	if res.Status != 200 {
		t.Fatalf("goal edit with approval_ref: %d body %s", res.Status, res.Body)
	}
	var got struct {
		Title string `json:"title"`
	}
	res.Unmarshal(t, &got)
	if got.Title != "New Goal" {
		t.Fatalf("goal = %+v", got)
	}
	// non-goal step edit needs no approval_ref (covered in lifecycle test)
	_ = json.Marshal
}
