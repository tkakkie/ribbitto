package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
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
func (blockingStore) SessionAccount(context.Context, []byte, time.Time) (domain.Account, error) {
	return domain.Account{}, auth.ErrNoSession
}
func (blockingStore) DeleteSession(context.Context, []byte) error { return nil }
func (blockingStore) ReplaceSession(context.Context, []byte, []byte, domain.ID, time.Time) error {
	return nil
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
