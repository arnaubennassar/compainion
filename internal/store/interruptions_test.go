package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/arnaubennassar/compainion/internal/domain"
)

func seedInterruptionParents(t *testing.T, db *DB) (string, string) {
	t.Helper()
	ws, err := db.CreateWorkstream(context.Background(), Workstream{Title: "ws"})
	if err != nil {
		t.Fatalf("CreateWorkstream = %v", err)
	}
	a := seedAgent(t, db, "01TESTRAISER000000000000000")
	return ws.ID, a.ID
}

func mkQuestion(text, atype string, sugg ...Suggestion) Question {
	return Question{Text: text, AnswerType: atype, Suggestions: sugg}
}

func mkSuggestion(label string, recommended bool) Suggestion {
	return Suggestion{Label: label, Recommended: recommended}
}

func TestInterruptionCreateValidatesQuestions(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)

	// no suggestions -> 422
	_, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "decision",
		Questions: []Question{mkQuestion("q", "choice")},
	})
	if err == nil {
		t.Fatal("question without suggestions must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
		t.Errorf("expected Unprocessable, got %v", err)
	}

	// two recommended -> 422
	_, err = db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "decision",
		Questions: []Question{mkQuestion("q", "choice",
			mkSuggestion("a", true), mkSuggestion("b", true))},
	})
	if err == nil {
		t.Fatal("two recommended suggestions must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
		t.Errorf("expected Unprocessable, got %v", err)
	}

	// invalid answer_type -> 422
	_, err = db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "decision",
		Questions: []Question{mkQuestion("q", "essay", mkSuggestion("a", false))},
	})
	if err == nil {
		t.Fatal("invalid answer_type must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
		t.Errorf("expected Unprocessable, got %v", err)
	}

	// valid create: status open, positions auto-assigned, note event emitted
	it, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "decision",
		Questions: []Question{mkQuestion("q1", "choice", mkSuggestion("a", true), mkSuggestion("b", false))},
	})
	if err != nil {
		t.Fatalf("CreateInterruption = %v", err)
	}
	if it.ID == "" || it.Status != "open" {
		t.Errorf("created = %+v", it)
	}
	if len(it.Questions) != 1 || it.Questions[0].Position != 0 || it.Questions[0].ID == "" {
		t.Errorf("question not persisted correctly: %+v", it.Questions)
	}
	if len(it.Questions[0].Suggestions) != 2 || it.Questions[0].Suggestions[0].ID == "" {
		t.Errorf("suggestions not persisted: %+v", it.Questions[0].Suggestions)
	}
	evs, err := db.ListEvents(ctx, EventFilter{AgentID: agentID})
	if err != nil || len(evs) != 1 {
		t.Fatalf("ListEvents = %v, %d events", err, len(evs))
	}
	if evs[0].Type != "note" {
		t.Errorf("event type = %q, want note", evs[0].Type)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(evs[0].Payload), &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p["kind"] != "interruption_created" || p["interruption_id"] != it.ID {
		t.Errorf("payload = %v", p)
	}
}

func TestInterruptionGetExpanded(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)
	it, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "approval",
		Questions: []Question{
			mkQuestion("q1", "choice", mkSuggestion("yes", true), mkSuggestion("no", false)),
			mkQuestion("q2", "free_text", mkSuggestion("n/a", false)),
		},
	})
	if err != nil {
		t.Fatalf("CreateInterruption = %v", err)
	}
	q1 := it.Questions[0].ID
	if it.Questions[0].Position != 0 || it.Questions[1].Position != 1 {
		t.Errorf("positions not auto-assigned: %+v", it.Questions)
	}
	// answer q1 non-final
	_, err = db.AnswerInterruption(ctx, it.ID, Answer{
		QuestionID: q1, Author: "user", Mode: "suggestion", Final: true,
		SuggestionID: &it.Questions[0].Suggestions[0].ID,
	})
	if err != nil {
		t.Fatalf("AnswerInterruption = %v", err)
	}
	got, err := db.GetInterruption(ctx, it.ID)
	if err != nil {
		t.Fatalf("GetInterruption = %v", err)
	}
	if len(got.Questions) != 2 {
		t.Fatalf("Get must expand questions, got %d", len(got.Questions))
	}
	if got.Questions[0].Status != "answered" {
		t.Errorf("q1 status = %q, want answered", got.Questions[0].Status)
	}
	if got.Questions[1].Status != "open" {
		t.Errorf("q2 status = %q, want open", got.Questions[1].Status)
	}
	if len(got.Questions[0].Suggestions) != 2 {
		t.Errorf("Get must expand suggestions, got %d", len(got.Questions[0].Suggestions))
	}
	if len(got.Questions[0].Answers) != 1 || got.Questions[0].Answers[0].Author != "user" {
		t.Errorf("Get must expand answers: %+v", got.Questions[0].Answers)
	}
	if got.Status != "open" || got.AnsweredAt != nil {
		t.Errorf("interruption must still be open: %+v", got)
	}
}

func TestInterruptionListFilters(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)
	a, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "alpha", Kind: "decision",
		Questions: []Question{mkQuestion("q", "confirm", mkSuggestion("ok", true))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "beta", Kind: "fyi",
		Questions: []Question{mkQuestion("q", "confirm", mkSuggestion("ok", true))},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.DismissInterruption(ctx, a.ID); err != nil {
		t.Fatalf("DismissInterruption = %v", err)
	}
	got, err := db.ListInterruptions(ctx, InterruptionFilter{Status: "open"})
	if err != nil || len(got) != 1 || got[0].Topic != "beta" {
		t.Fatalf("status filter = %v, %+v", err, got)
	}
	got, err = db.ListInterruptions(ctx, InterruptionFilter{Topic: "alpha"})
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("topic filter = %v, %+v", err, got)
	}
	got, err = db.ListInterruptions(ctx, InterruptionFilter{WorkstreamID: wsID})
	if err != nil || len(got) != 2 {
		t.Fatalf("workstream filter = %v, %d", err, len(got))
	}
	got, err = db.ListInterruptions(ctx, InterruptionFilter{Status: "dismissed", Topic: "alpha"})
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("combined filter = %v, %+v", err, got)
	}
}

func TestInterruptionNextOrderingAndBatch(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)

	mk := func(topic, priority string, blocking bool) Interruption {
		it, err := db.CreateInterruption(ctx, Interruption{
			WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: topic, Kind: "decision",
			Priority: priority, Blocking: blocking,
			Questions: []Question{mkQuestion("q", "confirm", mkSuggestion("ok", true))},
		})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	a := mk("t1", "low", true) // created first but lowest priority
	b := mk("t1", "urgent", false)
	c := mk("t2", "normal", true) // same priority as d, blocking first
	d := mk("t2", "normal", false)

	// urgent first; batch lists other open interruptions with the same topic
	got, err := db.NextInterruption(ctx)
	if err != nil {
		t.Fatalf("NextInterruption = %v", err)
	}
	if got.ID != b.ID {
		t.Errorf("Next = %s, want %s (urgent first)", got.ID, b.ID)
	}
	if got.Status != "presented" {
		t.Errorf("status = %q, want presented", got.Status)
	}
	if len(got.Batch) != 1 || got.Batch[0] != a.ID {
		t.Errorf("batch = %v, want [%s]", got.Batch, a.ID)
	}

	// ties on priority: blocking first, then oldest
	got, err = db.NextInterruption(ctx)
	if err != nil || got.ID != c.ID {
		t.Errorf("second Next = %v, %s; want %s (blocking before non-blocking)", err, got.ID, c.ID)
	}
	got, err = db.NextInterruption(ctx)
	if err != nil || got.ID != d.ID {
		t.Errorf("third Next = %v, %s; want %s", err, got.ID, d.ID)
	}
	got, err = db.NextInterruption(ctx)
	if err != nil || got.ID != a.ID {
		t.Errorf("fourth Next = %v, %s; want %s", err, got.ID, a.ID)
	}
	// presented ones are not returned again: nothing left
	got, err = db.NextInterruption(ctx)
	if err != nil {
		t.Fatalf("fifth Next = %v", err)
	}
	if got.ID != "" {
		t.Errorf("fifth Next returned %q; want empty (nothing open)", got.ID)
	}
}

func TestInterruptionAnswerValidation(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)
	it, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "decision",
		Questions: []Question{
			mkQuestion("q1", "choice", mkSuggestion("yes", true), mkSuggestion("no", false)),
			mkQuestion("q2", "free_text", mkSuggestion("n/a", false)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	q1, q2 := it.Questions[0].ID, it.Questions[1].ID
	sugg := it.Questions[0].Suggestions[0].ID
	other := it.Questions[1].Suggestions[0].ID
	text := "some text"

	cases := []struct {
		name string
		ans  Answer
	}{
		{"suggestion mode without suggestion", Answer{QuestionID: q1, Author: "user", Mode: "suggestion"}},
		{"suggestion from another question", Answer{QuestionID: q1, Author: "user", Mode: "suggestion", SuggestionID: &other}},
		{"unknown suggestion id", Answer{QuestionID: q1, Author: "user", Mode: "suggestion_with_comment", SuggestionID: &text}},
		{"free without text", Answer{QuestionID: q1, Author: "user", Mode: "free"}},
		{"user pushback", Answer{QuestionID: q1, Author: "user", Mode: "pushback", Text: &text}},
		{"companion suggestion", Answer{QuestionID: q1, Author: "companion", Mode: "suggestion", SuggestionID: &sugg}},
		{"companion pushback without text", Answer{QuestionID: q1, Author: "companion", Mode: "pushback"}},
		{"unknown author", Answer{QuestionID: q1, Author: "robot", Mode: "free", Text: &text}},
		{"unknown mode", Answer{QuestionID: q1, Author: "user", Mode: "maybe", Text: &text}},
	}
	for _, tc := range cases {
		_, err := db.AnswerInterruption(ctx, it.ID, tc.ans)
		if err == nil {
			t.Errorf("%s: must be rejected", tc.name)
		} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
			t.Errorf("%s: expected Unprocessable, got %v", tc.name, err)
		}
	}

	// question that does not belong -> NotFound
	_, err = db.AnswerInterruption(ctx, "01NOSUCHINTERRUP00000000000", Answer{QuestionID: q1, Author: "user", Mode: "suggestion", SuggestionID: &sugg})
	if err == nil {
		t.Error("answer to unknown interruption must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
	_, err = db.AnswerInterruption(ctx, it.ID, Answer{QuestionID: "01NOSUCHQUESTION000000000", Author: "user", Mode: "free", Text: &text})
	if err == nil {
		t.Error("answer to unknown question must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}

	// user needs_details (no text) is valid and non-final: still open
	if _, err := db.AnswerInterruption(ctx, it.ID, Answer{QuestionID: q1, Author: "user", Mode: "needs_details"}); err != nil {
		t.Fatalf("valid needs_details = %v", err)
	}
	// companion pushback with text is valid
	if _, err := db.AnswerInterruption(ctx, it.ID, Answer{QuestionID: q1, Author: "companion", Mode: "pushback", Text: &text}); err != nil {
		t.Fatalf("valid pushback = %v", err)
	}
	got, _ := db.GetInterruption(ctx, it.ID)
	if got.Status != "open" || got.AnsweredAt != nil {
		t.Errorf("non-final answers must keep the interruption open: %+v", got)
	}

	// final user suggestion closes q1; interruption stays open until q2 answered
	if _, err := db.AnswerInterruption(ctx, it.ID, Answer{QuestionID: q1, Author: "user", Mode: "suggestion_with_comment", SuggestionID: &sugg, Text: &text, Final: true}); err != nil {
		t.Fatalf("valid final answer = %v", err)
	}
	got, _ = db.GetInterruption(ctx, it.ID)
	if got.Questions[0].Status != "answered" || got.Status != "open" {
		t.Errorf("after q1 final: %+v", got)
	}
	// final free answer on q2 closes the interruption
	if _, err := db.AnswerInterruption(ctx, it.ID, Answer{QuestionID: q2, Author: "user", Mode: "free", Text: &text, Final: true}); err != nil {
		t.Fatalf("valid final free answer = %v", err)
	}
	got, _ = db.GetInterruption(ctx, it.ID)
	if got.Status != "answered" {
		t.Errorf("status = %q, want answered", got.Status)
	}
	if got.AnsweredAt == nil {
		t.Error("answered_at must be set when the interruption closes")
	}
	// note event targeted at raised_by_agent_id
	evs, err := db.ListEvents(ctx, EventFilter{AgentID: agentID, Types: []string{"note"}})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range evs {
		if strings.Contains(ev.Payload, "interruption_answered") {
			found = true
			var p map[string]any
			if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
				t.Fatalf("payload not JSON: %v", err)
			}
			if p["interruption_id"] != it.ID {
				t.Errorf("payload = %v", p)
			}
		}
	}
	if !found {
		t.Error("interruption_answered note event not emitted")
	}
}

func TestInterruptionAnsweredWithSkippedQuestion(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)
	it, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "decision",
		Questions: []Question{
			mkQuestion("q1", "choice", mkSuggestion("yes", true)),
			mkQuestion("q2", "choice", mkSuggestion("yes", true)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// skip q2 directly (the API layer skips via its own flow)
	if _, err := db.Exec(`UPDATE questions SET status = 'skipped' WHERE id = ?`, it.Questions[1].ID); err != nil {
		t.Fatal(err)
	}
	text := "ok"
	if _, err := db.AnswerInterruption(ctx, it.ID, Answer{QuestionID: it.Questions[0].ID, Author: "user", Mode: "free", Text: &text, Final: true}); err != nil {
		t.Fatalf("AnswerInterruption = %v", err)
	}
	got, _ := db.GetInterruption(ctx, it.ID)
	if got.Status != "answered" {
		t.Errorf("answered+skipped must close the interruption, got %q", got.Status)
	}
}

func TestInterruptionDismissAndReopen(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	wsID, agentID := seedInterruptionParents(t, db)
	it, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "fyi",
		Questions: []Question{mkQuestion("q", "confirm", mkSuggestion("ok", true))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DismissInterruption(ctx, it.ID); err != nil {
		t.Fatalf("DismissInterruption = %v", err)
	}
	got, _ := db.GetInterruption(ctx, it.ID)
	if got.Status != "dismissed" {
		t.Errorf("status = %q, want dismissed", got.Status)
	}
	// dismissed cannot be reopened (only presented -> open)
	if err := db.ReopenInterruption(ctx, it.ID); err == nil {
		t.Error("reopen from dismissed must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Conflict {
		t.Errorf("expected Conflict, got %v", err)
	}
	// presented -> open
	it2, err := db.CreateInterruption(ctx, Interruption{
		WorkstreamID: wsID, RaisedByAgentID: agentID, Topic: "t", Kind: "fyi",
		Questions: []Question{mkQuestion("q", "confirm", mkSuggestion("ok", true))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.NextInterruption(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenInterruption(ctx, it2.ID); err != nil {
		t.Fatalf("ReopenInterruption = %v", err)
	}
	got, _ = db.GetInterruption(ctx, it2.ID)
	if got.Status != "open" {
		t.Errorf("status = %q, want open", got.Status)
	}
	// dismiss a presented one works
	if err := db.DismissInterruption(ctx, it2.ID); err != nil {
		t.Fatalf("DismissInterruption = %v", err)
	}
	// dismiss of unknown id -> NotFound
	if err := db.DismissInterruption(ctx, "01NOSUCHINTERRUP00000000000"); err == nil {
		t.Error("dismiss unknown must fail")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}
