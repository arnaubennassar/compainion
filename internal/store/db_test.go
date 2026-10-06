package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestOpenMemory(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) = %v", err)
	}
	defer db.Close()
	if db.SchemaVersion() != 1 {
		t.Errorf("schema version = %d, want 1", db.SchemaVersion())
	}
}

func TestOpenIdempotentMigrations(t *testing.T) {
	path := t.TempDir() + "/t.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open = %v", err)
	}
	db.Close()
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open = %v", err)
	}
	defer db2.Close()
	if db2.SchemaVersion() != 1 {
		t.Errorf("schema version = %d, want 1", db2.SchemaVersion())
	}
}

func TestForeignKeyEnforcement(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	defer db.Close()
	// suggestions without a question must be rejected by FK.
	_, err = db.Exec(`INSERT INTO suggestions (id, question_id, label, rationale, recommended, created_at, updated_at)
		VALUES ('s1', 'nope', 'x', '', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("expected foreign key violation, got %v", err)
	}
}

func TestOneRecommendedSuggestion(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO interruptions (id, workstream_id, raised_by_agent_id, plan_id, step_id, task_id, topic, kind, priority, digest, blocking, status, created_at, updated_at)
			VALUES ('i1', NULL, NULL, NULL, NULL, NULL, 't', 'question', 'normal', '', 0, 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		_, err = tx.Exec(`INSERT INTO questions (id, interruption_id, position, text, answer_type, status, created_at, updated_at)
			VALUES ('q1', 'i1', 0, 'q?', 'choice', 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO suggestions (id, question_id, label, rationale, recommended, created_at, updated_at)
			VALUES ('s1', 'q1', 'a', '', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO suggestions (id, question_id, label, rationale, recommended, created_at, updated_at)
			VALUES ('s2', 'q1', 'b', '', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("second recommended suggestion must fail with UNIQUE constraint, got %v", err)
	}
}
