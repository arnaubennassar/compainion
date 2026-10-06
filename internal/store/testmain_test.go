package store

import "testing"

// mustOpen opens a throwaway in-memory database for repository tests.
func mustOpen(t *testing.T) (*DB, error) {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, nil
}
