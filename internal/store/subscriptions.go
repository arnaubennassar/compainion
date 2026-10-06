package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

const subscriptionCols = `id, method, target, types, filter, secret, active, created_at, updated_at`

func scanSubscription(r interface{ Scan(...any) error }) (Subscription, error) {
	var s Subscription
	var secret sql.NullString
	var active int
	if err := r.Scan(&s.ID, &s.Method, &s.Target, &s.Types, &s.Filter, &secret, &active, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return Subscription{}, err
	}
	s.Active = active == 1
	if secret.Valid {
		v := secret.String
		s.Secret = &v
	}
	return s, nil
}

// UpsertSubscription registers a hook idempotently per (method, target):
// an existing row is updated in place and keeps its id.
func (d *DB) UpsertSubscription(ctx context.Context, s Subscription) (Subscription, error) {
	if s.Method != "webhook" && s.Method != "command" {
		return Subscription{}, domain.Errf(domain.Invalid, "method must be webhook or command")
	}
	if s.Target == "" {
		return Subscription{}, domain.Errf(domain.Invalid, "target is required")
	}
	if s.Types == "" {
		s.Types = "[]"
	}
	if s.Filter == "" {
		s.Filter = "{}"
	}
	n := now()
	res, err := d.ExecContext(ctx, `INSERT INTO subscriptions (id, method, target, types, filter, secret, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(method, target) DO UPDATE SET
			types = excluded.types, filter = excluded.filter, secret = excluded.secret, active = 1, updated_at = excluded.updated_at`,
		ids.New(), s.Method, s.Target, s.Types, s.Filter, nullStr(ptrStr(s.Secret)), n, n)
	if err != nil {
		return Subscription{}, fmt.Errorf("store: upsert subscription: %w", err)
	}
	_ = res
	return d.GetSubscriptionByTarget(ctx, s.Method, s.Target)
}

// GetSubscription returns one subscription or NotFound.
func (d *DB) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	s, err := scanSubscription(d.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Subscription{}, domain.Errf(domain.NotFound, "subscription %s not found", id)
	}
	return s, err
}

// GetSubscriptionByTarget looks a subscription up by its natural key.
func (d *DB) GetSubscriptionByTarget(ctx context.Context, method, target string) (Subscription, error) {
	s, err := scanSubscription(d.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE method = ? AND target = ?`, method, target))
	if err == sql.ErrNoRows {
		return Subscription{}, domain.Errf(domain.NotFound, "subscription %s %s not found", method, target)
	}
	return s, err
}

// ListSubscriptions returns all subscriptions in creation order.
func (d *DB) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list subscriptions: %w", err)
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSubscription removes one; NotFound if unknown.
func (d *DB) DeleteSubscription(ctx context.Context, id string) error {
	res, err := d.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete subscription: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.NotFound, "subscription %s not found", id)
	}
	return nil
}

// SetSubscriptionActive toggles a hook on/off without deleting it.
func (d *DB) SetSubscriptionActive(ctx context.Context, id string, active bool) error {
	var a int
	if active {
		a = 1
	}
	res, err := d.ExecContext(ctx, `UPDATE subscriptions SET active = ?, updated_at = ? WHERE id = ?`, a, now(), id)
	if err != nil {
		return fmt.Errorf("store: set subscription active: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Errf(domain.NotFound, "subscription %s not found", id)
	}
	return nil
}