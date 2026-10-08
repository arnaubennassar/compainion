package store

import (
	"context"
	"testing"
	"time"
)

func mkInterruption(t *testing.T, d *DB, topic, kind, action, expiresAt string) Interruption {
	t.Helper()
	it, err := d.CreateInterruption(context.Background(), Interruption{
		Topic: topic, Kind: kind, Priority: "normal",
		DefaultAction:   action,
		ExpiresAt:       expiresAt,
		Questions: []Question{{
			Text: "Proceed?", AnswerType: "choice",
			Suggestions: []Suggestion{{Label: "Yes", Recommended: true}},
		}},
	})
	if err != nil {
		t.Fatalf("CreateInterruption(%s/%s/%s): %v", topic, kind, action, err)
	}
	return it
}

func futureRFC3339() string { return time.Now().UTC().Add(time.Hour).Format(time.RFC3339) }
func pastRFC3339() string   { return time.Now().UTC().Add(-time.Hour).Format(time.RFC3339) }

func TestDefaultExpiryActionIsEscalateOnly(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	// Every user-facing interruption defaults to escalate: expiry can only
	// raise urgency, never close or decide the interruption.
	for _, kind := range []string{"decision", "approval", "clarification"} {
		it := mkInterruption(t, d, "t-"+kind, kind, "", "")
		if it.DefaultAction != "escalate" {
			t.Fatalf("kind %s default_action = %q, want escalate", kind, it.DefaultAction)
		}
	}
	// Any other default_action is rejected: there is no auto-close.
	if _, err := d.CreateInterruption(ctx, Interruption{
		Topic: "bad", Kind: "decision", DefaultAction: "pause",
		Questions: []Question{{Text: "?", AnswerType: "confirm", Suggestions: []Suggestion{{Label: "Yes"}}}},
	}); err == nil {
		t.Fatal("pause default_action accepted, want error")
	}
	if _, err := d.CreateInterruption(ctx, Interruption{
		Topic: "bad", Kind: "decision", DefaultAction: "deny",
		Questions: []Question{{Text: "?", AnswerType: "confirm", Suggestions: []Suggestion{{Label: "Yes"}}}},
	}); err == nil {
		t.Fatal("deny default_action accepted, want error")
	}
	// expires_at must be RFC3339.
	if _, err := d.CreateInterruption(ctx, Interruption{
		Topic: "bad", Kind: "decision", ExpiresAt: "tomorrow",
		Questions: []Question{{Text: "?", AnswerType: "confirm", Suggestions: []Suggestion{{Label: "Yes"}}}},
	}); err == nil {
		t.Fatal("non-RFC3339 expires_at accepted, want error")
	}
}

func TestExpireLapsedInterruptionsEscalateKeepsOpen(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	it := mkInterruption(t, d, "blocked", "decision", "escalate", pastRFC3339())
	alive := mkInterruption(t, d, "gate2", "approval", "escalate", futureRFC3339())
	touched, err := d.ExpireLapsedInterruptions(ctx, now())
	if err != nil {
		t.Fatalf("ExpireLapsedInterruptions: %v", err)
	}
	if len(touched) != 1 || touched[0] != it.ID {
		t.Fatalf("touched = %v, want [%s]", touched, it.ID)
	}
	out, _ := d.GetInterruption(ctx, it.ID)
	if out.Status != "open" {
		t.Fatalf("escalated interruption status = %q, want open (no silent expiry)", out.Status)
	}
	if out.Priority != "urgent" {
		t.Fatalf("priority = %q, want urgent", out.Priority)
	}
	// Questions stay open and unanswered: no decision was made on the user's
	// behalf.
	for _, q := range out.Questions {
		if q.Status != "open" {
			t.Fatalf("question status = %q, want open", q.Status)
		}
		for _, a := range q.Answers {
			t.Fatalf("expired interruption must not record answers, got %#v", a)
		}
	}
	aliveOut, _ := d.GetInterruption(ctx, alive.ID)
	if aliveOut.Status != "open" || aliveOut.Priority != "normal" {
		t.Fatalf("future expiry interrupted early: status = %q priority = %q", aliveOut.Status, aliveOut.Priority)
	}
	// expires_at cleared so the sweep does not re-escalate forever.
	touched, err = d.ExpireLapsedInterruptions(ctx, now())
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 0 {
		t.Fatalf("second sweep touched %v, want none", touched)
	}
}

func TestExpireLapsedInterruptionsEmitsNotifyingEvent(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	it := mkInterruption(t, d, "blocked", "decision", "escalate", pastRFC3339())
	if _, err := d.ExpireLapsedInterruptions(ctx, now()); err != nil {
		t.Fatal(err)
	}
	evs, err := d.ListEvents(ctx, EventFilter{Types: []string{"note"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if contains(ev.Payload, `"kind":"interruption_escalated"`) && contains(ev.Payload, it.ID) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an interruption_escalated note event for the raising agent")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}