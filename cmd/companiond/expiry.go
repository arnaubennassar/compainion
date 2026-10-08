package main

// expireLoop enforces the interruption timeout policy: every expiryPoll
// interval it applies the default_action of lapsed interruptions
// (expires_at passed, still open/presented).
//
//   - pause / deny: the interruption is closed with status 'expired' and a
//     note event notifies the raising agent. Nothing is approved: a worker
//     gate defaults to pause, never auto-approve.
//   - escalate: user-facing decisions are NOT silently expired; the sweep
//     bumps priority to urgent once and keeps the interruption open.
//
// Errors are logged, not fatal: the next tick retries (single-writer SQLite
// keeps this loop cheap).
import (
	"context"
	"log/slog"
	"time"

	"github.com/arnaubennassar/compainion/internal/store"
)

const expiryPoll = 30 * time.Second

func expireLoop(ctx context.Context, db *store.DB, log *slog.Logger) {
	ticker := time.NewTicker(expiryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			touched, err := db.ExpireLapsedInterruptions(ctx, time.Now().UTC().Format(time.RFC3339))
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Error("companiond: expiry sweep failed", "err", err)
				continue
			}
			for _, id := range touched {
				log.Info("companiond: interruption expiry applied", "interruption_id", id)
			}
		}
	}
}
