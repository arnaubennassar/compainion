package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arnaubennassar/compainion/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func runDispatcher(t *testing.T, db *store.DB, client *http.Client, sleep func(time.Duration)) (context.Context, context.CancelFunc) {
	t.Helper()
	if sleep == nil {
		sleep = func(time.Duration) {}
	}
	d := &Dispatcher{DB: db, Client: client, Sleep: sleep, Logger: slog.Default(), started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	<-d.started // boot cursor read; later events will be delivered
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("dispatcher did not stop after cancel")
		}
	})
	return ctx, cancel
}

func TestWebhookDelivered(t *testing.T) {
	db := newTestDB(t)
	var calls int32
	var body, sig string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		sig = r.Header.Get("X-Companion-Signature")
		w.WriteHeader(200)
	}))
	defer hook.Close()

	secret := "s3cr3t"
	if _, err := db.UpsertSubscription(context.Background(), store.Subscription{
		Method: "webhook", Target: hook.URL, Types: `["needs_input"]`, Secret: &secret,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, _ := runDispatcher(t, db, hook.Client(), nil)
	ev, err := db.AppendEvent(ctx, store.Event{Type: "needs_input"})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("webhook calls = %d, want 1", got)
	}
	wantBody := fmt.Sprintf(`{"type":"needs_input","id":%q}`, ev.ID)
	if body != wantBody {
		t.Errorf("body = %q, want %q", body, wantBody)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	if want := hex.EncodeToString(mac.Sum(nil)); sig != want {
		t.Errorf("signature = %q, want %q", sig, want)
	}
}

func TestWebhookTypeNonMatch(t *testing.T) {
	db := newTestDB(t)
	var calls int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(200)
	}))
	defer hook.Close()

	if _, err := db.UpsertSubscription(context.Background(), store.Subscription{
		Method: "webhook", Target: hook.URL, Types: `["needs_input"]`,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, _ := runDispatcher(t, db, hook.Client(), nil)
	if _, err := db.AppendEvent(ctx, store.Event{Type: "note"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("webhook calls = %d, want 0 for non-matching type", got)
	}
}

func TestWebhookRetries(t *testing.T) {
	db := newTestDB(t)
	var calls int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(500)
	}))
	defer hook.Close()

	if _, err := db.UpsertSubscription(context.Background(), store.Subscription{
		Method: "webhook", Target: hook.URL, Types: `["needs_input"]`,
	}); err != nil {
		t.Fatal(err)
	}

	var sleeps []time.Duration
	sleep := func(d time.Duration) { sleeps = append(sleeps, d) }
	ctx, _ := runDispatcher(t, db, hook.Client(), sleep)
	if _, err := db.AppendEvent(ctx, store.Event{Type: "needs_input"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("webhook calls = %d, want 3 (retries then give up)", got)
	}
	if len(sleeps) != 2 || sleeps[0] != 100*time.Millisecond || sleeps[1] != 200*time.Millisecond {
		t.Errorf("sleeps = %v, want [100ms 200ms]", sleeps)
	}
}

func TestFilterKind(t *testing.T) {
	db := newTestDB(t)
	var calls int32
	var lastBody string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		w.WriteHeader(200)
	}))
	defer hook.Close()

	if _, err := db.UpsertSubscription(context.Background(), store.Subscription{
		Method: "webhook", Target: hook.URL, Types: `["note"]`,
		Filter: `{"kind":["interruption_created","agent_waiting"]}`,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, _ := runDispatcher(t, db, hook.Client(), nil)
	// kind mismatch -> no delivery
	if _, err := db.AppendEvent(ctx, store.Event{Type: "note", Payload: `{"kind":"agent_lost"}`}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("calls after non-matching filter = %d, want 0", got)
	}
	// kind match -> delivered
	ev, err := db.AppendEvent(ctx, store.Event{Type: "note", Payload: `{"kind":"interruption_created","interruption_id":"i1"}`})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 after matching filter", got)
	}
	want := fmt.Sprintf(`{"type":"note","id":%q}`, ev.ID)
	if lastBody != want {
		t.Errorf("body = %q, want %q", lastBody, want)
	}
}

func TestCommandDelivery(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "signal.txt")
	target := fmt.Sprintf(`printf "%%s|%%s" "$COMPANION_EVENT_TYPE" "$COMPANION_EVENT_ID" > %s`, out)
	if _, err := db.UpsertSubscription(context.Background(), store.Subscription{
		Method: "command", Target: target, Types: `["needs_input"]`,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, _ := runDispatcher(t, db, nil, nil)
	ev, err := db.AppendEvent(ctx, store.Event{Type: "needs_input"})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		content, _ = os.ReadFile(out)
		if content != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if content == nil {
		t.Fatalf("command never wrote %s", out)
	}
	if want := fmt.Sprintf("needs_input|%s", ev.ID); string(content) != want {
		t.Errorf("file content = %q, want %q", content, want)
	}
}
