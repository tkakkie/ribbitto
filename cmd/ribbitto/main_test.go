package main

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestSetupToken(t *testing.T) {
	for _, value := range []string{"unset", "", strings.Repeat("x", 31), strings.Repeat("x", 32)} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RIBBITTO_SETUP_TOKEN", value)
			if value == "unset" {
				if err := os.Unsetenv("RIBBITTO_SETUP_TOKEN"); err != nil {
					t.Fatal(err)
				}
				value = ""
			}
			token, err := setupToken()
			if (err != nil) != (len(value) == 31) || (err == nil && token != value) {
				t.Fatalf("setupToken() = %q, %v", token, err)
			}
		})
	}
}

// blockingStore's clean-up blocks until its context ends, like a query
// waiting on a lock.
type blockingStore struct{ started chan struct{} }

func (blockingStore) CreateSession(context.Context, []byte, domain.ID, time.Time) error { return nil }
func (blockingStore) SessionAccount(context.Context, []byte, time.Time) (domain.Account, auth.Session, error) {
	return domain.Account{}, auth.Session{}, auth.ErrNoSession
}
func (blockingStore) DeleteSession(context.Context, []byte) (domain.ID, bool, error) {
	return domain.ID{}, false, nil
}
func (blockingStore) ReplaceSession(context.Context, []byte, []byte, domain.ID, time.Time) (domain.ID, bool, error) {
	return domain.ID{}, false, nil
}
func (s blockingStore) DeleteExpiredSessions(ctx context.Context, _ time.Time) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestSessionCleanupStops(t *testing.T) {
	store := blockingStore{started: make(chan struct{})}
	// The parent context stays alive, as when serve returns a listener error.
	stop := startSessionCleanup(context.Background(), auth.NewSessions(store, time.Now))
	<-store.started
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not cancel a blocked clean-up")
	}
}

// blockingSequences blocks its reads until their context ends and reports
// the first one.
type blockingSequences struct {
	started chan struct{}
	once    *sync.Once
}

func (s blockingSequences) CommittedSequences(ctx context.Context, _ []domain.ID) (map[domain.ID]int64, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// stop must end a watermark check blocked in its read, so the check never
// outlives the pool that serve closes next.
func TestWatermarkStops(t *testing.T) {
	hub := realtime.NewHub()
	_, unregister, err := hub.Register(context.Background(), realtime.Connection{Organization: domain.ID{1}, Account: domain.ID{2}, Session: domain.ID{3}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	sequences := blockingSequences{started: make(chan struct{}), once: &sync.Once{}}
	// A long timeout, so only stop can end the blocked read.
	stop := startRealtimeWorker(context.Background(), realtime.Watermark{Hub: hub, Sequences: sequences, Timeout: time.Hour}, time.Millisecond)
	select {
	case <-sequences.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the watermark never read")
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not cancel a blocked watermark check")
	}
}

func TestSignupEnabled(t *testing.T) {
	for _, value := range []string{"on", "off", "", "ON", "true", " on"} {
		enabled, err := signupEnabled(value)
		if enabled != (value == "on") || (err != nil) != (value != "on" && value != "off" && value != "") {
			t.Fatalf("%q: %t, %v", value, enabled, err)
		}
	}
	t.Setenv("RIBBITTO_SIGNUP", "invalid")
	if err := serve(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "RIBBITTO_SIGNUP") {
		t.Fatalf("invalid signup switch did not prevent startup: %v", err)
	}
}
