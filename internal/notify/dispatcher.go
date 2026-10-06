// Package notify delivers subscribed events to external hooks: HTTP webhooks
// and local commands. It polls the store's append-only events table (no
// in-process bus) so delivery survives restarts and stays correct with a
// single-writer SQLite database.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/arnaubennassar/compainion/internal/store"
)

const (
	// pollInterval is how often the dispatcher looks for new events.
	pollInterval = 200 * time.Millisecond
	// webhookAttempts is the total number of delivery attempts per webhook.
	webhookAttempts = 3

	// commandTimeout bounds each command hook run.
	commandTimeout = 10 * time.Second
	// requestTimeout bounds each webhook POST.
	requestTimeout = 10 * time.Second
)

// webhookBackoff are the pauses between webhook attempts.
var webhookBackoff = [2]time.Duration{100 * time.Millisecond, 200 * time.Millisecond}

// Dispatcher delivers matching events to active subscriptions.
//
// Sleep is injected so tests run without real delays (retry backoff and the
// poll interval both go through it).
type Dispatcher struct {
	DB     *store.DB
	Client *http.Client
	Sleep  func(time.Duration)
	Logger *slog.Logger

	once    sync.Once
	started chan struct{} // closed after the boot cursor is read (test sync)
}

// Run polls the event log from a cursor (starting at the latest event id on
// boot, so existing events are not re-delivered) and delivers each new event
// to every active subscription whose type and filter match. Blocks until ctx
// is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	d.init()
	d.Logger.Info("notify: dispatcher started")
	cursor, err := store.LatestEventID(ctx, d.DB)
	if err != nil {
		d.Logger.Error("notify: latest event id", "err", err)
	}
	close(d.started)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
		evs, err := d.DB.ListEvents(ctx, store.EventFilter{Since: cursor, Limit: 500})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.Logger.Error("notify: list events", "err", err)
			continue
		}
		for _, ev := range evs {
			cursor = ev.ID
			d.deliver(ctx, ev)
		}
	}
}

func (d *Dispatcher) init() {
	d.once.Do(func() {
		if d.Sleep == nil {
			d.Sleep = time.Sleep
		}
		if d.Client == nil {
			d.Client = &http.Client{Timeout: requestTimeout}
		}
		if d.Logger == nil {
			d.Logger = slog.Default()
		}
	})
}

// deliver runs one event against all active subscriptions, sequentially (the
// event log cursor already guarantees each event is seen exactly once).
func (d *Dispatcher) deliver(ctx context.Context, ev store.Event) {
	subs, err := d.DB.ListSubscriptions(ctx)
	if err != nil {
		d.Logger.Error("notify: list subscriptions", "err", err)
		return
	}
	for _, sub := range subs {
		if !sub.Active || !matchesSubscription(sub, ev) {
			continue
		}
		switch sub.Method {
		case "webhook":
			d.deliverWebhook(ctx, sub, ev)
		case "command":
			d.deliverCommand(ctx, sub, ev)
		}
	}
}

// matchesSubscription reports whether the subscription wants this event: its
// types list is empty (match all) or contains the event type, and its filter
// object (`{"kind":["a","b"]}`) has every listed payload field holding a
// value from the list.
func matchesSubscription(sub store.Subscription, ev store.Event) bool {
	var types []string
	if err := json.Unmarshal([]byte(sub.Types), &types); err == nil && len(types) > 0 {
		ok := false
		for _, t := range types {
			if t == ev.Type {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	var filter map[string][]string
	if err := json.Unmarshal([]byte(sub.Filter), &filter); err != nil || len(filter) == 0 {
		return err == nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
		return false
	}
	for key, allowed := range filter {
		val, ok := payload[key].(string)
		if !ok {
			return false
		}
		found := false
		for _, a := range allowed {
			if a == val {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// signal is the payload delivered to hooks: type and id only, never the event
// payload.
type signal struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// deliverWebhook POSTs the signal JSON to the target, signing with
// X-Companion-Signature = hex HMAC-SHA256(body, secret) when a secret is set.
// Retries up to webhookAttempts total attempts with webhookBackoff pauses on
// transport errors and non-2xx responses.
func (d *Dispatcher) deliverWebhook(ctx context.Context, sub store.Subscription, ev store.Event) {
	body, _ := json.Marshal(signal{Type: ev.Type, ID: ev.ID})
	for attempt := 1; attempt <= webhookAttempts; attempt++ {
		rctx, cancel := context.WithTimeout(ctx, requestTimeout)
		req, err := http.NewRequestWithContext(rctx, http.MethodPost, sub.Target, bytes.NewReader(body))
		if err != nil {
			cancel()
			d.Logger.Error("notify: webhook request", "err", err, "target", sub.Target)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if sub.Secret != nil && *sub.Secret != "" {
			mac := hmac.New(sha256.New, []byte(*sub.Secret))
			mac.Write(body)
			req.Header.Set("X-Companion-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := d.Client.Do(req)
		if err == nil {
			if code := resp.StatusCode; code >= 200 && code < 300 {
				resp.Body.Close()
				cancel()
				return
			}
			resp.Body.Close()
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		cancel()
		d.Logger.Warn("notify: webhook attempt failed", "err", err, "target", sub.Target, "attempt", attempt)
		if attempt < webhookAttempts {
			d.Sleep(webhookBackoff[attempt-1])
		}
	}
}

// deliverCommand runs the target as `sh -c <target>` with the event signalled
// ONLY through environment variables (COMPANION_EVENT_TYPE,
// COMPANION_EVENT_ID) — the payload never enters the command line. Bounded by
// commandTimeout; no retry.
func (d *Dispatcher) deliverCommand(ctx context.Context, sub store.Subscription, ev store.Event) {
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", sub.Target)
	cmd.Env = append(os.Environ(),
		"COMPANION_EVENT_TYPE="+ev.Type,
		"COMPANION_EVENT_ID="+ev.ID)
	if out, err := cmd.CombinedOutput(); err != nil {
		d.Logger.Error("notify: command hook failed", "err", err, "output", string(out))
	}
}
