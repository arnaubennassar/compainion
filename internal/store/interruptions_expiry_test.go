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

func TestDefaultExpiryActionAndValidation(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	// Worker approval gates default to pause: never auto-approve.
	it := mkInterruption(t, d, "gate", "approval", "", "")
	if it.DefaultAction != "pause" {
		t.Fatalf("approval default_action = %q, want pause", it.DefaultAction)
	}
	// User-facing kinds default to escalate and persist.
	it = mkInterruption(t, d, "blocked", "decision", "", "")
	if it.DefaultAction != "escalate" {
		t.Fatalf("decision default_action = %q, want escalate", it.DefaultAction)
	}
	// Explicit actions are honored; invalid ones rejected.
	if it := mkInterruption(t, d, "x", "approval", "deny", ""); it.DefaultAction != "deny" {
		t.Fatalf("explicit default_action = %q, want deny", it.DefaultAction)
	}
	if _, err := d.CreateInterruption(ctx, Interruption{
		Topic: "bad", Kind: "approval", DefaultAction: "approve",
		Questions: []Question{{Text: "?", AnswerType: "confirm", Suggestions: []Suggestion{{Label: "Yes"}}}},
	}); err == nil {
		t.Fatal("invalid default_action accepted, want error")
	}
	// expires_at must be RFC3339.
	if _, err := d.CreateInterruption(ctx, Interruption{
		Topic: "bad", Kind: "approval", ExpiresAt: "tomorrow",
		Questions: []Question{{Text: "?", AnswerType: "confirm", Suggestions: []Suggestion{{Label: "Yes"}}}},
	}); err == nil {
		t.Fatal("non-RFC3339 expires_at accepted, want error")
	}
}

func TestExpireLapsedInterruptionsPauseClosesWithoutApproving(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	it := mkInterruption(t, d, "gate", "approval", "pause", pastRFC3339())
	alive := mkInterruption(t, d, "gate2", "approval", "pause", futureRFC3339())

	touched, err := d.ExpireLapsedInterruptions(ctx, now())
	if err != nil {
		t.Fatalf("ExpireLapsedInterruptions: %v", err)
	}
	if len(touched) != 1 || touched[0] != it.ID {
		t.Fatalf("touched = %v, want [%s]", touched, it.ID)
	}
	out, err := d.GetInterruption(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "expired" {
		t.Fatalf("status = %q, want expired", out.Status)
	}
	// Questions are skipped (closed), never answered by an auto-approval.
	for _, q := range out.Questions {
		if q.Status != "skipped" {
			t.Fatalf("question status = %q, want skipped", q.Status)
		}
		for _, a := range q.Answers {
			t.Fatalf("expired pause gate must not record answers, got %#v", a)
		}
	}
	aliveOut, _ := d.GetInterruption(ctx, alive.ID)
	if aliveOut.Status != "open" {
		t.Fatalf("future expiry interrupted early: status = %q", aliveOut.Status)
	}
}

func TestExpireLapsedInterruptionsDenyCloses(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	it := mkInterruption(t, d, "gate", "approval", "deny", pastRFC3339())
	if _, err := d.ExpireLapsedInterruptions(ctx, now()); err != nil {
		t.Fatal(err)
	}
	out, _ := d.GetInterruption(ctx, it.ID)
	if out.Status != "expired" {
		t.Fatalf("status = %q, want expired", out.Status)
	}
}

func TestExpireLapsedInterruptionsEscalateKeepsOpen(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	it := mkInterruption(t, d, "blocked", "decision", "escalate", pastRFC3339())
	if _, err := d.ExpireLapsedInterruptions(ctx, now()); err != nil {
		t.Fatal(err)
	}
	out, _ := d.GetInterruption(ctx, it.ID)
	if out.Status != "open" {
		t.Fatalf("escalated interruption status = %q, want open (no silent expiry)", out.Status)
	}
	if out.Priority != "urgent" {
		t.Fatalf("priority = %q, want urgent", out.Priority)
	}
	// expires_at cleared so the sweep does not re-escalate forever.
	touched, err := d.ExpireLapsedInterruptions(ctx, now())
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 0 {
		t.Fatalf("second sweep touched %v, want none", touched)
	}
}

func TestExpireLapsedInterruptionsEmitsNotifyingEvents(t *testing.T) {
	d, _ := mustOpen(t)
	ctx := context.Background()
	it := mkInterruption(t, d, "gate", "approval", "pause", pastRFC3339())
	if _, err := d.ExpireLapsedInterruptions(ctx, now()); err != nil {
		t.Fatal(err)
	}
	evs, err := d.ListEvents(ctx, EventFilter{Types: []string{"note"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if contains(ev.Payload, `"kind":"interruption_expired"`) && contains(ev.Payload, it.ID) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an interruption_expired note event for the raising agent")
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
