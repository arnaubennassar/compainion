package store

import (
	"context"
	"testing"
)

func TestSubscriptionUpsertIdempotent(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	s1, err := db.UpsertSubscription(ctx, Subscription{Method: "webhook", Target: "http://x/hook", Types: `["note"]`, Filter: `{}`})
	if err != nil {
		t.Fatalf("UpsertSubscription = %v", err)
	}
	if s1.ID == "" || !s1.Active {
		t.Errorf("created = %+v", s1)
	}
	secret := "s3cret"
	s2, err := db.UpsertSubscription(ctx, Subscription{Method: "webhook", Target: "http://x/hook", Types: `["note","error"]`, Secret: &secret})
	if err != nil {
		t.Fatalf("second upsert = %v", err)
	}
	if s2.ID != s1.ID {
		t.Errorf("upsert must reuse the same id: %q vs %q", s2.ID, s1.ID)
	}
	if s2.Types != `["note","error"]` || s2.Secret == nil || *s2.Secret != "s3cret" {
		t.Errorf("upsert must update fields, got %+v", s2)
	}
}

func TestSubscriptionListDeleteSetActive(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	s, _ := db.UpsertSubscription(ctx, Subscription{Method: "command", Target: "touch /tmp/x"})
	s2, _ := db.UpsertSubscription(ctx, Subscription{Method: "webhook", Target: "http://y"})
	list, err := db.ListSubscriptions(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %d", err, len(list))
	}
	if err := db.SetSubscriptionActive(ctx, s.ID, false); err != nil {
		t.Fatalf("SetActive = %v", err)
	}
	got, _ := db.GetSubscription(ctx, s.ID)
	if got.Active {
		t.Error("SetActive(false) must deactivate")
	}
	if err := db.DeleteSubscription(ctx, s.ID); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	if err := db.DeleteSubscription(ctx, s.ID); err == nil {
		t.Error("deleting twice must fail (NotFound)")
	}
	list, err = db.ListSubscriptions(ctx)
	if err != nil || len(list) != 1 || list[0].ID != s2.ID {
		t.Fatalf("after delete = %v, %+v", err, list)
	}
}