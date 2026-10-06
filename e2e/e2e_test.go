//go:build e2e

// Package e2e drives the full HTTP API through the spec's six flows plus the
// lost-agent scenario, using only testutil (in-memory server + JSON client).
package e2e

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arnaubennassar/compainion/internal/testutil"
)

type obj = map[string]any

var idemSeq atomic.Uint64

// do issues a request and fails the test on any unexpected status.
func do(t *testing.T, srv *httptest.Server, want int, method, path string, body obj) []byte {
	t.Helper()
	var hdr map[string]string
	if method == "POST" && body != nil {
		hdr = map[string]string{"Idempotency-Key": fmt.Sprintf("e2e-%d", idemSeq.Add(1))}
	}
	res := testutil.Do(t, srv, method, path, body, hdr)
	if res.Status != want {
		t.Fatalf("%s %s: status = %d, want %d: %s", method, path, res.Status, want, res.Body)
	}
	return res.Body
}

// page is the standard list envelope.
type page struct {
	Items      []obj  `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func id(t *testing.T, body []byte) string {
	t.Helper()
	var m obj
	decode(t, body, &m)
	s, _ := m["id"].(string)
	if s == "" {
		t.Fatalf("no id in %s", body)
	}
	return s
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

// mustInterruption creates a one-question interruption and returns id+question id.
func mustInterruption(t *testing.T, srv *httptest.Server, planID, topic string) (string, string) {
	t.Helper()
	body := obj{
		"topic": topic, "kind": "approval", "priority": "high",
		"digest": "digest " + topic, "blocking": true,
		"questions": []obj{{
			"text": "Proceed?", "answer_type": "confirm",
			"suggestions": []obj{{"label": "Yes", "recommended": true}, {"label": "No"}},
		}},
	}
	if planID != "" {
		body["plan_id"] = planID
	}
	b := do(t, srv, 201, "POST", "/interruptions", body)
	var it struct {
		ID        string `json:"id"`
		Questions []struct {
			ID string `json:"id"`
		} `json:"questions"`
	}
	decode(t, b, &it)
	return it.ID, it.Questions[0].ID
}

// answer posts one answer and returns the returned interruption body.
func answer(t *testing.T, srv *httptest.Server, itID string, in obj) []byte {
	t.Helper()
	return do(t, srv, 200, "POST", "/interruptions/"+itID+"/answers", in)
}

func mustAgent(t *testing.T, srv *httptest.Server, role string) string {
	t.Helper()
	return id(t, do(t, srv, 201, "POST", "/agents", obj{"role": role}))
}

func mustWorkstream(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return id(t, do(t, srv, 201, "POST", "/workstreams", obj{"title": "ws"}))
}

func claim(t *testing.T, srv *httptest.Server, kind, id, agentID string) {
	t.Helper()
	do(t, srv, 200, "POST", "/"+kind+"/"+id+"/claim", obj{"agent_id": agentID})
}

func finish(t *testing.T, srv *httptest.Server, kind, id, status string, evidence []string) {
	t.Helper()
	out := obj{"result": "result for " + id}
	if evidence != nil {
		out["evidence"] = evidence
	}
	do(t, srv, 200, "POST", "/"+kind+"/"+id+"/finish", obj{"status": status, "outcome": out})
}

// Flow 1: simple task claim/finish with events.
func TestFlow1SimpleTask(t *testing.T) {
	srv := testutil.NewServer(t)
	ws := mustWorkstream(t, srv)
	worker := mustAgent(t, srv, "worker")

	task := id(t, do(t, srv, 201, "POST", "/tasks", obj{
		"workstream_id": ws, "requested_by": "companion", "title": "say hi",
		"added_by": "companion",
	}))

	claim(t, srv, "tasks", task, worker)
	finish(t, srv, "tasks", task, "done", []string{"go test ./... -> ok"})

	var got obj
	decode(t, do(t, srv, 200, "GET", "/tasks/"+task, nil), &got)
	if got["status"] != "done" {
		t.Fatalf("task = %v", got)
	}

	// lifecycle events are recorded and addressable by task
	var ev page
	decode(t, do(t, srv, 200, "GET", "/events?task_id="+task, nil), &ev)
	if len(ev.Items) < 2 {
		t.Fatalf("want started+finished events, got %v", ev.Items)
	}
	types := map[string]bool{}
	for _, e := range ev.Items {
		types[e["type"].(string)] = true
	}
	if !types["started"] || !types["finished"] {
		t.Fatalf("missing started/finished events: %v", types)
	}
}

// Flow 2: plan -> steps -> goal deps {verify}; approval interruption with 3
// questions (Q3 needs_details -> companion free -> user final); approve,
// assign, progression, finishing verify then goal -> plan done.
func TestFlow2PlanExecution(t *testing.T) {
	srv := testutil.NewServer(t)
	ws := mustWorkstream(t, srv)

	plan := id(t, do(t, srv, 201, "POST", "/plans", obj{
		"workstream_id": ws, "title": "P", "goals": "ship it",
		"acceptance_checks": []string{"t1"},
	}))
	var p obj
	decode(t, do(t, srv, 200, "GET", "/plans/"+plan, nil), &p)
	goal := p["goal_step_id"].(string)

	mkStep := func(title string, deps []string) string {
		b := obj{"kind": "task", "title": title, "added_by": "planner"}
		if deps != nil {
			b["deps"] = deps
		}
		return id(t, do(t, srv, 201, "POST", "/plans/"+plan+"/steps", b))
	}
	a := mkStep("A", nil)
	b := mkStep("B", []string{a})
	verify := mkStep("verify", []string{b})

	// goal deps == {verify}
	var g struct {
		Deps []string `json:"deps"`
	}
	decode(t, do(t, srv, 200, "GET", "/steps/"+goal, nil), &g)
	if len(g.Deps) != 1 || g.Deps[0] != verify {
		t.Fatalf("goal deps = %v, want [verify]", g.Deps)
	}

	// approval interruption with 3 questions; stays open until all final
	app := do(t, srv, 201, "POST", "/interruptions", obj{
		"topic": "plan-approval", "kind": "approval", "priority": "high",
		"digest": "approve P", "blocking": true, "plan_id": plan,
		"questions": []obj{
			{"text": "Q1?", "answer_type": "confirm", "suggestions": []obj{{"label": "ok", "recommended": true}}},
			{"text": "Q2?", "answer_type": "choice", "suggestions": []obj{{"label": "opt1", "recommended": true}}},
			{"text": "Q3?", "answer_type": "free_text", "suggestions": []obj{{"label": "skip", "recommended": true}}},
		},
	})
	var it struct {
		ID        string `json:"id"`
		Questions []struct {
			ID string `json:"id"`
		} `json:"questions"`
	}
	decode(t, app, &it)
	q1, q2, q3 := it.Questions[0].ID, it.Questions[1].ID, it.Questions[2].ID

	answer(t, srv, it.ID, obj{"question_id": q1, "author": "user", "mode": "free", "text": "ok", "final": true})
	var mid obj
	decode(t, answer(t, srv, it.ID, obj{"question_id": q2, "author": "user", "mode": "free", "text": "because", "final": true}), &mid)
	if mid["status"] != "open" {
		t.Fatalf("interruption closed early: %v", mid["status"])
	}
	// Q3: user asks for details, companion clarifies, user answers finally
	answer(t, srv, it.ID, obj{"question_id": q3, "author": "user", "mode": "needs_details"})
	var mfree obj
	decode(t, answer(t, srv, it.ID, obj{"question_id": q3, "author": "companion", "mode": "free", "text": "Q3 means this"}), &mfree)
	if mfree["status"] != "open" {
		t.Fatalf("interruption closed before final Q3 answer: %v", mfree["status"])
	}
	var done obj
	decode(t, answer(t, srv, it.ID, obj{"question_id": q3, "author": "user", "mode": "free", "text": "got it", "final": true}), &done)
	if done["status"] != "answered" {
		t.Fatalf("interruption = %v, want answered", done["status"])
	}

	// approve + assign orchestrator
	orch := mustAgent(t, srv, "orchestrator")
	do(t, srv, 200, "POST", "/plans/"+plan+"/approve", obj{"approved_by": "user-1"})
	do(t, srv, 200, "POST", "/plans/"+plan+"/assign", obj{"agent_id": orch})

	// progression: next returns only A, then B, then verify, then goal
	worker := mustAgent(t, srv, "worker")
	run := func(id string) {
		var np page
		decode(t, do(t, srv, 200, "GET", "/plans/"+plan+"/steps/next", nil), &np)
		if len(np.Items) != 1 || np.Items[0]["id"].(string) != id {
			t.Fatalf("steps/next = %v, want [%s]", np.Items, id)
		}
		claim(t, srv, "steps", id, worker)
		finish(t, srv, "steps", id, "done", []string{"evidence for " + id})
	}
	run(a)
	run(b)
	run(verify)
	run(goal)

	decode(t, do(t, srv, 200, "GET", "/plans/"+plan, nil), &p)
	if p["status"] != "done" {
		t.Fatalf("plan = %v, want done", p["status"])
	}
}

// Flow 3: failed step then plan change rules.
func TestFlow3PlanChange(t *testing.T) {
	srv := testutil.NewServer(t)
	ws := mustWorkstream(t, srv)
	worker := mustAgent(t, srv, "worker")

	plan := id(t, do(t, srv, 201, "POST", "/plans", obj{
		"workstream_id": ws, "title": "P", "goals": "g1",
	}))

	mk := func(title string) string {
		return id(t, do(t, srv, 201, "POST", "/plans/"+plan+"/steps",
			obj{"kind": "task", "title": title, "added_by": "planner"}))
	}
	done := mk("done-step")
	failed := mk("failed-step")
	pending := mk("pending-step")

	do(t, srv, 200, "POST", "/plans/"+plan+"/approve", obj{"approved_by": "u"})
	claim(t, srv, "steps", done, worker)
	finish(t, srv, "steps", done, "done", []string{"e"})
	claim(t, srv, "steps", failed, worker)
	finish(t, srv, "steps", failed, "failed", nil)

	// edit a done step -> 409
	if res := testutil.Do(t, srv, "PATCH", "/steps/"+done,
		obj{"title": "nope"}, map[string]string{"If-Match": `"1"`}); res.Status != 409 {
		t.Fatalf("edit done step: status = %d, want 409", res.Status)
	}
	// add a new step depending on the done one, and rewire the pending step's deps
	dep := id(t, do(t, srv, 201, "POST", "/plans/"+plan+"/steps",
		obj{"kind": "task", "title": "retry", "added_by": "orchestrator", "deps": []string{done}}))
	if res := testutil.Do(t, srv, "POST", "/steps/"+pending+"/deps", obj{"depends_on_id": dep},
		map[string]string{"If-Match": `"1"`}); res.Status != 204 {
		t.Fatalf("add dep: status = %d: %s", res.Status, res.Body)
	}
	var d struct {
		Deps []string `json:"deps"`
	}
	decode(t, do(t, srv, 200, "GET", "/steps/"+pending, nil), &d)
	if len(d.Deps) != 1 || d.Deps[0] != dep {
		t.Fatalf("rewired deps = %v", d.Deps)
	}

	// PATCH plan goals without approval_ref -> 428; with answered interruption id -> 200
	if res := testutil.Do(t, srv, "PATCH", "/plans/"+plan,
		obj{"goals": "g2"}, map[string]string{"If-Match": `"2"`}); res.Status != 428 {
		t.Fatalf("goal change w/o approval_ref: status = %d, want 428", res.Status)
	}
	refID, q := mustInterruption(t, srv, plan, "goal-change")
	answer(t, srv, refID, obj{"question_id": q, "author": "user", "mode": "free", "text": "yes", "final": true})
	if res := testutil.Do(t, srv, "PATCH", "/plans/"+plan,
		obj{"goals": "g2", "approval_ref": refID},
		map[string]string{"If-Match": `"2"`}); res.Status != 200 {
		t.Fatalf("goal change with approval_ref: status = %d: %s", res.Status, res.Body)
	}
}

// Flow 4: finding dedupe incl. ignored resolution.
func TestFlow4FindingDedupe(t *testing.T) {
	srv := testutil.NewServer(t)
	agent := mustAgent(t, srv, "worker")
	in := func(line int) obj {
		return obj{
			"reported_by_agent_id": agent, "category": "bug", "severity": "high",
			"location": "internal/x.go", "title": "too many logs", "details": "d",
			"evidence": []obj{{"line": line}},
		}
	}
	first := testutil.Do(t, srv, "POST", "/findings", in(12), nil)
	if first.Status != 201 {
		t.Fatalf("first finding status = %d, want 201: %s", first.Status, first.Body)
	}
	var f obj
	decode(t, first.Body, &f)
	fid := f["id"].(string)
	if f["created"] != true {
		t.Fatalf("first finding created = %v", f["created"])
	}

	second := testutil.Do(t, srv, "POST", "/findings", in(99), nil)
	if second.Status != 200 {
		t.Fatalf("dup finding status = %d, want 200: %s", second.Status, second.Body)
	}
	decode(t, second.Body, &f)
	if f["id"] != fid || f["created"] != false || f["occurrences"].(float64) != 2 {
		t.Fatalf("dup finding = %v", f)
	}

	do(t, srv, 200, "POST", "/findings/"+fid+"/resolve", obj{"resolution": "ignored"})

	third := testutil.Do(t, srv, "POST", "/findings", in(123), nil)
	if third.Status != 200 {
		t.Fatalf("post-resolve finding status = %d, want 200: %s", third.Status, third.Body)
	}
	decode(t, third.Body, &f)
	if f["id"] != fid || f["status"] != "resolved" || f["occurrences"].(float64) != 3 {
		t.Fatalf("post-resolve finding = %v", f)
	}
	// no new row: ?status=new is empty
	var fp page
	decode(t, do(t, srv, 200, "GET", "/findings?status=new", nil), &fp)
	if len(fp.Items) != 0 {
		t.Fatalf("new findings = %v, want none", fp.Items)
	}
}

// Flow 5: /events long poll returns upon concurrent append.
func TestFlow5EventsLongPoll(t *testing.T) {
	srv := testutil.NewServer(t)
	since := id(t, do(t, srv, 201, "POST", "/events", obj{"type": "note"}))

	go func() {
		time.Sleep(150 * time.Millisecond)
		testutil.Do(t, srv, "POST", "/events", obj{"type": "progress", "payload": obj{"n": 1}}, nil)
	}()
	start := time.Now()
	res := testutil.Do(t, srv, "GET", "/events?since="+since+"&wait=2", nil, nil)
	elapsed := time.Since(start)
	if res.Status != 200 {
		t.Fatalf("long poll status = %d", res.Status)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("long poll waited %v, want release before the 2s timeout", elapsed)
	}
	var ev page
	decode(t, res.Body, &ev)
	if len(ev.Items) != 1 || ev.Items[0]["type"].(string) != "progress" {
		t.Fatalf("long poll items = %v", ev.Items)
	}
}

// Flow 6: agent waiting + steer event recorded and listed.
func TestFlow6AgentSteer(t *testing.T) {
	srv := testutil.NewServer(t)
	agent := mustAgent(t, srv, "worker")
	do(t, srv, 200, "PATCH", "/agents/"+agent, obj{"status": "running"})
	do(t, srv, 200, "PATCH", "/agents/"+agent, obj{"status": "waiting"})

	do(t, srv, 201, "POST", "/agents/"+agent+"/events",
		obj{"type": "steer", "payload": obj{"text": "continue", "reason": "auto-steer"}})

	var ev page
	decode(t, do(t, srv, 200, "GET", "/agents/"+agent+"/events", nil), &ev)
	if len(ev.Items) != 1 || ev.Items[0]["type"].(string) != "steer" {
		t.Fatalf("agent events = %v", ev.Items)
	}
}

// Lost agent holding an in_progress step: the backend never mutates the step.
func TestLostAgentKeepsStepInProgress(t *testing.T) {
	srv := testutil.NewServer(t)
	ws := mustWorkstream(t, srv)
	agent := mustAgent(t, srv, "worker")
	plan := id(t, do(t, srv, 201, "POST", "/plans", obj{
		"workstream_id": ws, "title": "P", "goals": "g",
	}))
	step := id(t, do(t, srv, 201, "POST", "/plans/"+plan+"/steps",
		obj{"kind": "task", "title": "A", "added_by": "planner"}))
	claim(t, srv, "steps", step, agent)

	do(t, srv, 200, "PATCH", "/agents/"+agent, obj{"status": "lost"})

	var s obj
	decode(t, do(t, srv, 200, "GET", "/steps/"+step, nil), &s)
	if s["status"] != "in_progress" {
		t.Fatalf("step = %v, want in_progress untouched", s["status"])
	}
}
