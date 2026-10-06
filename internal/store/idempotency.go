package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arnaubennassar/compainion/internal/domain"
)

// LookupIdempotency returns the cached response for a POST idempotency key or
// NotFound when the key is unseen.
func (d *DB) LookupIdempotency(ctx context.Context, key, method, path string) (IdempotencyRecord, error) {
	var r IdempotencyRecord
	err := d.QueryRowContext(ctx,
		`SELECT key, method, path, request_hash, status_code, response_body
		 FROM idempotency_keys WHERE key = ? AND method = ? AND path = ?`, key, method, path).
		Scan(&r.Key, &r.Method, &r.Path, &r.RequestHash, &r.StatusCode, &r.ResponseBody)
	if err == sql.ErrNoRows {
		return IdempotencyRecord{}, domain.Errf(domain.NotFound, "no cached response for key %s", key)
	}
	return r, err
}

// SaveIdempotency caches a POST response. Same key + same request hash is a
// no-op (idempotent retry); same key + different request hash -> Unprocessable
// (422): the key must not be reused for a different request.
func (d *DB) SaveIdempotency(ctx context.Context, r IdempotencyRecord) error {
	n := now()
	_, err := d.ExecContext(ctx, `INSERT INTO idempotency_keys (key, method, path, request_hash, status_code, response_body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key, method, path) DO NOTHING`,
		r.Key, r.Method, r.Path, r.RequestHash, r.StatusCode, r.ResponseBody, n, n)
	if err != nil {
		return fmt.Errorf("store: save idempotency: %w", err)
	}
	existing, err := d.LookupIdempotency(ctx, r.Key, r.Method, r.Path)
	if err != nil {
		return err
	}
	if existing.RequestHash != r.RequestHash {
		return domain.Errf(domain.Unprocessable, "idempotency key %s was used for a different request", r.Key)
	}
	return nil
}
