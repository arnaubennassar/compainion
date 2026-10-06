// Package store holds the SQLite persistence layer: connection open +
// migrations (db.go), shared row models (models.go) and one repository file
// per entity. Repositories are methods on *DB.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)

	"github.com/arnaubennassar/compainion/internal/ids"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps the SQLite handle. All repositories are methods on *DB.
//
// Transactions: repositories can run multi-statement work either directly on
// the *DB or inside a transaction via WithTx (or BeginTx for callers that
// manage commit/rollback themselves). AppendEventTx lets any repository emit
// a lifecycle event inside the caller's transaction so side-effect events are
// atomic with the state change.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// pending migrations. ":memory:" uses a named shared-cache in-memory DB so a
// file path round-trip and tests behave identically.
//
// MaxOpenConns(1): SQLite serializes writes anyway; a single connection
// avoids SQLITE_BUSY and simplifies transactional read-modify-write logic at
// localhost scale. Revisit with Postgres.
func Open(path string) (*DB, error) {
	dsn := dsnFor(path)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	raw.SetMaxOpenConns(1)
	if err := raw.Ping(); err != nil {
		raw.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	db := &DB{DB: raw}
	if err := db.migrate(); err != nil {
		raw.Close()
		return nil, err
	}
	return db, nil
}

func dsnFor(path string) string {
	if path == ":memory:" {
		return fmt.Sprintf("file:mem%s?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", ids.New())
	}
	v := url.Values{}
	v.Add("_pragma", "foreign_keys(1)")
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + v.Encode()
}

func (d *DB) migrate() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("store: schema_migrations: %w", err)
	}
	var current int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("store: read migrations: %w", err)
	}
	for _, e := range entries {
		var version int
		if _, err := fmt.Sscanf(e.Name(), "%d", &version); err != nil {
			return fmt.Errorf("store: bad migration name %q", e.Name())
		}
		if version <= current {
			continue
		}
		sqlText, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("store: read %s: %w", e.Name(), err)
		}
		tx, err := d.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", version, err)
		}
		if _, err := tx.Exec(string(sqlText)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, version); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", version, err)
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version (0 if none).
func (d *DB) SchemaVersion() int {
	var v int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0
	}
	return v
}

// WithTx runs fn inside a transaction, committing on nil error and rolling
// back otherwise. Later repositories should use this for multi-statement
// writes; the tx passed to fn can be used with AppendEventTx.
func (d *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
