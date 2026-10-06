package api

import (
	"context"
	"strconv"
	"time"
)

const (
	pollInterval = 100 * time.Millisecond
	maxWait      = 60 * time.Second
)

// parseWait reads the ?wait=<seconds> query value (0 if absent/invalid, capped at 60s).
func parseWait(v string) time.Duration {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > maxWait {
		d = maxWait
	}
	return d
}

// waitUntil calls check immediately and then every pollInterval until it
// reports done, wait elapses, or ctx is cancelled. It always returns the last
// check result, so callers can render an empty response on timeout.
// Long polling is implemented by polling the store (localhost, tiny load)
// instead of an in-process bus: simpler, and correct across restarts.
func waitUntil(ctx context.Context, wait time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(wait)
	for {
		done, err := check()
		if err != nil || done || wait <= 0 || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
	}
}
