package store

import (
	"context"
	"testing"

	"github.com/arnaubennassar/compainion/internal/domain"
)

func TestIdempotencyLookupSave(t *testing.T) {
	db, _ := mustOpen(t)
	ctx := context.Background()
	if _, err := db.LookupIdempotency(ctx, "k1", "POST", "/tasks"); err == nil {
		t.Fatal("first lookup must miss")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
	rec := IdempotencyRecord{Key: "k1", Method: "POST", Path: "/tasks", RequestHash: "h1", StatusCode: 201, ResponseBody: `{"id":"x"}`}
	if err := db.SaveIdempotency(ctx, rec); err != nil {
		t.Fatalf("Save = %v", err)
	}
	got, err := db.LookupIdempotency(ctx, "k1", "POST", "/tasks")
	if err != nil {
		t.Fatalf("Lookup = %v", err)
	}
	if got.StatusCode != 201 || got.ResponseBody != rec.ResponseBody || got.RequestHash != "h1" {
		t.Errorf("Lookup = %+v", got)
	}
	// same key, different request_hash -> 422
	rec2 := rec
	rec2.RequestHash = "h2"
	if err := db.SaveIdempotency(ctx, rec2); err == nil {
		t.Error("same key different hash must be rejected")
	} else if de, ok := err.(*domain.Error); !ok || de.Kind != domain.Unprocessable {
		t.Errorf("expected Unprocessable, got %v", err)
	}
	// save is idempotent for the identical record
	if err := db.SaveIdempotency(ctx, rec); err != nil {
		t.Errorf("re-saving identical record must succeed, got %v", err)
	}
}
