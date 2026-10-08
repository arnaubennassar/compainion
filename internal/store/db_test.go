package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestOpenMemory(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) = %v", err)
	}
	defer db.Close()
	if db.SchemaVersion() != 2 {
		t.Errorf("schema version = %d, want 2", db.SchemaVersion())
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
	if db2.SchemaVersion() != 2 {
		t.Errorf("schema version = %d, want 2", db2.SchemaVersion())
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
			VALUES ('i1', NULL, NULL, NULL, NULL, NULL, 't', 'approval', 'normal', '', 0, 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		_, err = tx.Exec(`INSERT INTO questions (id, interruption_id, position, text, answer_type, status, created_at, updated_at)
			VALUES ('q1', 'i1', 0, 'q?', 'choice', 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
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

func TestEnumChecks(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	defer db.Close()

	ts := "'2026-01-01T00:00:00Z'"
	insertInterruption := func(id, kind, status string) error {
		_, err := db.Exec(`INSERT INTO interruptions (id, topic, kind, priority, status, created_at, updated_at)
			VALUES (` + id + `, 't', '` + kind + `', 'normal', '` + status + `', ` + ts + `, ` + ts + `)`)
		return err
	}
	insertQuestion := func(id, status string) error {
		_, err := db.Exec(`INSERT INTO questions (id, interruption_id, position, text, answer_type, status, created_at, updated_at)
			VALUES (` + id + `, 'ik0', 0, 'q?', 'choice', '` + status + `', ` + ts + `, ` + ts + `)`)
		return err
	}

	if err := insertInterruption("'ik0'", "decision", "open"); err != nil {
		t.Fatalf("seed interruption: %v", err)
	}

	for _, kind := range []string{"decision", "approval", "clarification", "finding", "fyi"} {
		id := "'ik_" + kind + "'"
		if err := insertInterruption(id, kind, "open"); err != nil {
			t.Errorf("interruptions.kind %q must be accepted, got %v", kind, err)
		}
	}
	if err := insertInterruption("'ik_bad'", "question", "open"); err == nil {
		t.Error("interruptions.kind 'question' must be rejected")
	}

	for _, status := range []string{"open", "presented", "answered", "dismissed", "expired"} {
		id := "'is_" + status + "'"
		if err := insertInterruption(id, "fyi", status); err != nil {
			t.Errorf("interruptions.status %q must be accepted, got %v", status, err)
		}
	}
	if err := insertInterruption("'is_bad'", "fyi", "pending"); err == nil {
		t.Error("interruptions.status 'pending' must be rejected")
	}

	for _, status := range []string{"open", "answered", "skipped"} {
		id := "'qs_" + status + "'"
		if err := insertQuestion(id, status); err != nil {
			t.Errorf("questions.status %q must be accepted, got %v", status, err)
		}
	}
	if err := insertQuestion("'qs_bad'", "pending"); err == nil {
		t.Error("questions.status 'pending' must be rejected")
	}

	_, err = db.Exec(`INSERT INTO findings (id, category, severity, title, fingerprint, status, created_at, updated_at)
		VALUES ('f_seed', 'bug', 'low', 't', 'fp_seed', 'new', ` + ts + `, ` + ts + `)`)
	if err != nil {
		t.Fatalf("seed finding: %v", err)
	}
	for i, category := range []string{"bug", "docs", "config", "observability", "tech_debt", "security", "other"} {
		id := fmt.Sprintf("'fc_%d'", i)
		_, err := db.Exec(`INSERT INTO findings (id, category, severity, title, fingerprint, status, created_at, updated_at)
			VALUES (` + id + `, '` + category + `', 'low', 't', 'fp_c` + fmt.Sprint(i) + `', 'new', ` + ts + `, ` + ts + `)`)
		if err != nil {
			t.Errorf("findings.category %q must be accepted, got %v", category, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO findings (id, category, severity, title, fingerprint, status, created_at, updated_at)
		VALUES ('fc_bad', 'performance', 'low', 't', 'fp_cbad', 'new', ` + ts + `, ` + ts + `)`); err == nil {
		t.Error("findings.category 'performance' must be rejected")
	}

	for i, severity := range []string{"low", "medium", "high"} {
		_, err := db.Exec(`INSERT INTO findings (id, category, severity, title, fingerprint, status, created_at, updated_at)
			VALUES ('fs_` + fmt.Sprint(i) + `', 'bug', '` + severity + `', 't', 'fp_s` + fmt.Sprint(i) + `', 'new', ` + ts + `, ` + ts + `)`)
		if err != nil {
			t.Errorf("findings.severity %q must be accepted, got %v", severity, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO findings (id, category, severity, title, fingerprint, status, created_at, updated_at)
		VALUES ('fs_bad', 'bug', 'critical', 't', 'fp_sbad', 'new', ` + ts + `, ` + ts + `)`); err == nil {
		t.Error("findings.severity 'critical' must be rejected")
	}
}
