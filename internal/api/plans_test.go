package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/arnaubennassar/compainion/internal/store"
	"net/http"
)

// newAPI boots an in-memory server and returns the db (for seeding parent
// rows) and the handler to issue HTTP requests against.
func newAPI(t *testing.T) (*store.DB, http.Handler) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return db, New(db).Handler()
}

// res is a JSON response (mirrors the tuple returned by do).
type res struct {
	Status  int
	Body    string
	Headers http.Header
}

func (r res) Unmarshal(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		t.Fatalf("decode response %q: %v", r.Body, err)
	}
}

// jdo issues a JSON request via the shared do helper (body marshalled; nil
// body sends nothing).
func jdo(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string) res {
	t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		raw = string(b)
	}
	code, rbody, hdr := do(t, h, method, path, raw, headers)
	return res{Status: code, Body: rbody, Headers: hdr}
}

func mustWorkstream(t *testing.T, db *store.DB) store.Workstream {
	t.Helper()
	w, err := db.CreateWorkstream(context.Background(), store.Workstream{Title: "ws"})
	if err != nil {
		t.Fatalf("create workstream: %v", err)
	}
	return w
}

func mustAgent(t *testing.T, db *store.DB, role string) store.Agent {
	t.Helper()
	a, err := db.RegisterAgent(context.Background(), store.Agent{Role: role, Status: "running"})
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	return a
}

// mustAnsweredInterruption creates an answered interruption on planID and
// returns its id (usable as approval_ref for goal edits).
func mustAnsweredInterruption(t *testing.T, db *store.DB, planID string) string {
	t.Helper()
	text := "yes"
	raiser := mustAgent(t, db, "orchestrator")
	it, err := db.CreateInterruption(context.Background(), store.Interruption{
		RaisedByAgentID: raiser.ID,
		PlanID:          &planID,
		Topic:           "plan-approval",
		Kind:            "approval",
		Priority:        "high",
		Digest:          "approve the change",
		Blocking:        true,
		Questions: []store.Question{{
			Position:   0,
			Text:       "ok?",
			AnswerType: "confirm",
			Suggestions: []store.Suggestion{
				{Label: "yes", Recommended: true},
			},
		}},
	})
	if err != nil {
		t.Fatalf("create interruption: %v", err)
	}
	if _, err := db.AnswerInterruption(context.Background(), it.ID, store.Answer{
		QuestionID: it.Questions[0].ID,
		Author:     "user",
		Mode:       "free",
		Text:       &text,
		Final:      true,
	}); err != nil {
		t.Fatalf("answer interruption: %v", err)
	}
	return it.ID
}

func mustPlan(t *testing.T, srv http.Handler, wsID string) res {
	t.Helper()
	return jdo(t, srv, "POST", "/plans", map[string]any{
		"workstream_id": wsID, "title": "P", "goals": "ship it",
		"acceptance_checks": []string{"t1"},
		"scope":             map[string]any{"read": []string{"src/a"}},
	}, nil)
}

func TestPlanCreateAndGet(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)

	res := mustPlan(t, srv, ws.ID)
	if res.Status != 201 {
		t.Fatalf("create status = %d body %s", res.Status, res.Body)
	}
	if etag := res.Headers.Get("ETag"); etag != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", etag)
	}
	var p struct {
		ID                 string         `json:"id"`
		Status             string         `json:"status"`
		Version            int            `json:"version"`
		AcceptanceChecks   []string       `json:"acceptance_checks"`
		Scope              map[string]any `json:"scope"`
		GoalStepID         string         `json:"goal_step_id"`
		AcceptanceCriteria string         `json:"acceptance_criteria"`
	}
	res.Unmarshal(t, &p)
	if p.Status != "draft" || p.Version != 1 || p.ID == "" {
		t.Fatalf("plan = %+v", p)
	}
	if len(p.AcceptanceChecks) != 1 || p.AcceptanceChecks[0] != "t1" {
		t.Fatalf("acceptance_checks = %v, want [t1]", p.AcceptanceChecks)
	}
	if p.Scope["read"] == nil {
		t.Fatalf("scope not decoded as object: %v", p.Scope)
	}
	if p.GoalStepID == "" {
		t.Fatalf("goal_step_id missing")
	}

	res = jdo(t, srv, "GET", "/plans/"+p.ID, nil, nil)
	if res.Status != 200 || res.Headers.Get("ETag") != `"1"` {
		t.Fatalf("get status = %d etag %q", res.Status, res.Headers.Get("ETag"))
	}
}

func TestPlanGetNotFound(t *testing.T) {
	_, srv := newAPI(t)
	res := jdo(t, srv, "GET", "/plans/nope", nil, nil)
	if res.Status != 404 {
		t.Fatalf("status = %d", res.Status)
	}
	var prob struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	res.Unmarshal(t, &prob)
	if prob.Type == "" || prob.Title == "" || prob.Status != 404 || prob.Detail == "" {
		t.Fatalf("problem incomplete: %+v", prob)
	}
}

func TestPlanPatchIfMatchRules(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)
	patch := map[string]any{"title": "P2"}

	res := jdo(t, srv, "PATCH", "/plans/"+p.ID, patch, nil)
	if res.Status != 428 {
		t.Fatalf("missing If-Match: status = %d body %s", res.Status, res.Body)
	}
	res = jdo(t, srv, "PATCH", "/plans/"+p.ID, patch, map[string]string{"If-Match": `"9"`})
	if res.Status != 412 {
		t.Fatalf("stale If-Match: status = %d", res.Status)
	}
	res = jdo(t, srv, "PATCH", "/plans/"+p.ID, patch, map[string]string{"If-Match": `"1"`})
	if res.Status != 200 {
		t.Fatalf("happy patch: status = %d body %s", res.Status, res.Body)
	}
	var got struct {
		Title   string `json:"title"`
		Version int    `json:"version"`
	}
	res.Unmarshal(t, &got)
	if got.Title != "P2" || got.Version != 2 {
		t.Fatalf("patched plan = %+v", got)
	}
	if etag := res.Headers.Get("ETag"); etag != `"2"` {
		t.Fatalf("ETag = %q, want \"2\"", etag)
	}
}

func TestPlanGoalChangeRequiresApprovalRef(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	orch := mustAgent(t, db, "orchestrator")
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)

	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/approve", map[string]string{"approved_by": "user-1"}, nil); res.Status != 200 {
		t.Fatalf("approve: status = %d body %s", res.Status, res.Body)
	}
	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/assign", map[string]string{"agent_id": orch.ID}, nil); res.Status != 200 {
		t.Fatalf("assign: status = %d body %s", res.Status, res.Body)
	}

	// goal change without approval_ref -> 428
	res := jdo(t, srv, "PATCH", "/plans/"+p.ID, map[string]any{"goals": "new goals"},
		map[string]string{"If-Match": `"2"`})
	if res.Status != 428 {
		t.Fatalf("goal change without approval_ref: status = %d body %s", res.Status, res.Body)
	}
	// with an answered interruption -> 200
	ref := mustAnsweredInterruption(t, db, p.ID)
	res = jdo(t, srv, "PATCH", "/plans/"+p.ID, map[string]any{"goals": "new goals", "approval_ref": ref},
		map[string]string{"If-Match": `"3"`})
	if res.Status != 200 {
		t.Fatalf("goal change with approval_ref: status = %d body %s", res.Status, res.Body)
	}
	var got struct {
		Goals      string `json:"goals"`
		ApprovedBy string `json:"approved_by"`
	}
	res.Unmarshal(t, &got)
	if got.Goals != "new goals" || got.ApprovedBy == "" {
		t.Fatalf("patched plan = %+v", got)
	}
}

func TestPlanApproveAndAssignRules(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	worker := mustAgent(t, db, "worker")
	orch := mustAgent(t, db, "orchestrator")
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)

	// assign on draft -> 409
	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/assign", map[string]string{"agent_id": orch.ID}, nil); res.Status != 409 {
		t.Fatalf("assign on draft: status = %d", res.Status)
	}
	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/approve", map[string]string{"approved_by": "u"}, nil); res.Status != 200 {
		t.Fatalf("approve: status = %d body %s", res.Status, res.Body)
	}
	// approve again -> 409
	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/approve", map[string]string{"approved_by": "u"}, nil); res.Status != 409 {
		t.Fatalf("double approve: status = %d", res.Status)
	}
	// non-orchestrator -> 422
	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/assign", map[string]string{"agent_id": worker.ID}, nil); res.Status != 422 {
		t.Fatalf("assign worker: status = %d", res.Status)
	}
	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/assign", map[string]string{"agent_id": orch.ID}, nil); res.Status != 200 {
		t.Fatalf("assign orchestrator: status = %d body %s", res.Status, res.Body)
	}
	var got struct {
		OrchestratorAgentID string `json:"orchestrator_agent_id"`
	}
	jdo(t, srv, "GET", "/plans/"+p.ID, nil, nil).Unmarshal(t, &got)
	if got.OrchestratorAgentID != orch.ID {
		t.Fatalf("orchestrator = %q", got.OrchestratorAgentID)
	}
}

func TestPlanDelete(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)

	// approved plan cannot be deleted
	if res := jdo(t, srv, "POST", "/plans/"+p.ID+"/approve", map[string]string{"approved_by": "u"}, nil); res.Status != 200 {
		t.Fatalf("approve: %d", res.Status)
	}
	if res := jdo(t, srv, "DELETE", "/plans/"+p.ID, nil, nil); res.Status != 409 {
		t.Fatalf("delete approved: status = %d", res.Status)
	}
	// draft plan deletes (use a fresh plan)
	body = mustPlan(t, srv, ws.ID)
	var p2 struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p2)
	if res := jdo(t, srv, "DELETE", "/plans/"+p2.ID, nil, nil); res.Status != 204 {
		t.Fatalf("delete draft: status = %d body %s", res.Status, res.Body)
	}
	if res := jdo(t, srv, "GET", "/plans/"+p2.ID, nil, nil); res.Status != 404 {
		t.Fatalf("get deleted: status = %d", res.Status)
	}
}

func TestPlanListPagination(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	var ids []string
	for i := 0; i < 3; i++ {
		body := mustPlan(t, srv, ws.ID)
		var p struct {
			ID string `json:"id"`
		}
		body.Unmarshal(t, &p)
		ids = append(ids, p.ID)
	}
	res := jdo(t, srv, "GET", "/plans?limit=2", nil, nil)
	if res.Status != 200 {
		t.Fatalf("list: %d", res.Status)
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page 1 = %+v", page)
	}
	if page.Items[0].ID != ids[0] || page.Items[1].ID != ids[1] {
		t.Fatalf("page 1 order = %+v", page.Items)
	}
	res = jdo(t, srv, "GET", "/plans?limit=2&cursor="+page.NextCursor, nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != ids[2] || page.NextCursor != "" {
		t.Fatalf("page 2 = %+v", page)
	}
}

func TestPlanGraphAndSteps(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID         string `json:"id"`
		GoalStepID string `json:"goal_step_id"`
	}
	body.Unmarshal(t, &p)

	var aID, bID string
	{
		res := jdo(t, srv, "POST", "/plans/"+p.ID+"/steps", map[string]any{
			"kind": "task", "title": "A", "added_by": "planner",
		}, nil)
		if res.Status != 201 {
			t.Fatalf("create A: %d body %s", res.Status, res.Body)
		}
		if etag := res.Headers.Get("ETag"); etag != `"1"` {
			t.Fatalf("A ETag = %q", etag)
		}
		var sa struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		}
		res.Unmarshal(t, &sa)
		aID = sa.ID
		if !sa.Ready {
			t.Fatalf("A ready = false, want true")
		}
		res = jdo(t, srv, "POST", "/plans/"+p.ID+"/steps", map[string]any{
			"kind": "task", "title": "B", "added_by": "planner", "deps": []string{aID},
		}, nil)
		if res.Status != 201 {
			t.Fatalf("create B: %d body %s", res.Status, res.Body)
		}
		var sb struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		}
		res.Unmarshal(t, &sb)
		bID = sb.ID
		if sb.Ready {
			t.Fatalf("B ready = true, want false (dep A pending)")
		}
		if res.Headers.Get("ETag") != `"1"` {
			t.Fatalf("B ETag = %q", res.Headers.Get("ETag"))
		}
	}

	// list shows both with correct ready flags
	res := jdo(t, srv, "GET", "/plans/"+p.ID+"/steps", nil, nil)
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	res.Unmarshal(t, &page)
	if len(page.Items) != 3 { // goal + A + B
		t.Fatalf("steps list = %+v", page.Items)
	}
	ready := map[string]bool{}
	for _, s := range page.Items {
		ready[s.ID] = s.Ready
	}
	if !ready[aID] || ready[bID] {
		t.Fatalf("ready flags wrong: %v", ready)
	}

	// next returns only ready ones (goal is blocked by A,B; A ready)
	res = jdo(t, srv, "GET", "/plans/"+p.ID+"/steps/next", nil, nil)
	res.Unmarshal(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != aID {
		t.Fatalf("steps/next = %+v", page.Items)
	}

	// graph: edges step -> depends_on
	res = jdo(t, srv, "GET", "/plans/"+p.ID+"/graph", nil, nil)
	var g struct {
		Steps []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		} `json:"steps"`
		Edges []struct {
			StepID      string `json:"step_id"`
			DependsOnID string `json:"depends_on_id"`
		} `json:"edges"`
	}
	res.Unmarshal(t, &g)
	if len(g.Steps) != 3 {
		t.Fatalf("graph steps = %d", len(g.Steps))
	}
	edges := map[[2]string]bool{}
	for _, e := range g.Edges {
		edges[[2]string{e.StepID, e.DependsOnID}] = true
	}
	// goal's deps are the current leaves: B (nothing depends on B), not A
	if !edges[[2]string{bID, aID}] || !edges[[2]string{p.GoalStepID, bID}] {
		t.Fatalf("edges missing: %v", g.Edges)
	}
	if len(g.Edges) != 2 {
		t.Fatalf("unexpected extra edges: %v", g.Edges)
	}
}

func TestPlanCreateStepValidation(t *testing.T) {
	db, srv := newAPI(t)
	ws := mustWorkstream(t, db)
	body := mustPlan(t, srv, ws.ID)
	var p struct {
		ID string `json:"id"`
	}
	body.Unmarshal(t, &p)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"goal kind", map[string]any{"kind": "goal", "title": "g", "added_by": "planner"}, 400},
		{"missing added_by", map[string]any{"kind": "task", "title": "x"}, 400},
		{"unknown dep", map[string]any{"kind": "task", "title": "x", "added_by": "planner", "deps": []string{"nope"}}, 400},
		{"bad added_by", map[string]any{"kind": "task", "title": "x", "added_by": "nobody"}, 400},
		{"unknown field", map[string]any{"kind": "task", "title": "x", "added_by": "planner", "bogus": 1}, 400},
	}
	for _, c := range cases {
		res := jdo(t, srv, "POST", fmt.Sprintf("/plans/%s/steps", p.ID), c.body, nil)
		if res.Status != c.want {
			t.Fatalf("%s: status = %d body %s, want %d", c.name, res.Status, res.Body, c.want)
		}
	}
}

func TestPlanNotFound404(t *testing.T) {
	db, srv := newAPI(t)
	_ = db
	if res := jdo(t, srv, "POST", "/plans/missing/steps", map[string]any{
		"kind": "task", "title": "x", "added_by": "planner",
	}, nil); res.Status != 404 {
		t.Fatalf("steps on missing plan: %d", res.Status)
	}
	if res := jdo(t, srv, "GET", "/plans/missing/steps/next", nil, nil); res.Status != 404 {
		t.Fatalf("next on missing plan: %d", res.Status)
	}
	if res := jdo(t, srv, "GET", "/plans/missing/graph", nil, nil); res.Status != 404 {
		t.Fatalf("graph on missing plan: %d", res.Status)
	}
	_ = db
}
