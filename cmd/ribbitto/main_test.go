package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestWorktreeHealth(t *testing.T) {
	for _, marker := range []string{"", "ribbitto_dev_example"} {
		for _, path := range []string{"/healthz", "/other"} {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				t.Run(marker+path+method, func(t *testing.T) {
					next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
					response := httptest.NewRecorder()
					worktreeHealth(next, marker).ServeHTTP(response, httptest.NewRequest(method, path, nil))
					if marker != "" && path == "/healthz" && method == http.MethodGet {
						if response.Code != http.StatusOK || response.Header().Get("X-Ribbitto-Worktree") != marker || response.Body.String() != "ok\n" {
							t.Fatal("health response did not identify the worktree")
						}
					} else if response.Code != http.StatusTeapot || response.Header().Get("X-Ribbitto-Worktree") != "" {
						t.Fatal("request was not delegated to the original handler")
					}
				})
			}
		}
	}
}

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

// blockingCleanup blocks until its context ends, like a query waiting on a
// lock.
type blockingCleanup struct{ started chan struct{} }

func (s blockingCleanup) DeleteExpired(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestSessionCleanupStops(t *testing.T) {
	store := blockingCleanup{started: make(chan struct{})}
	// The parent context stays alive, as when serve returns a listener error.
	stop := startSessionCleanup(context.Background(), store)
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

func (s blockingSequences) CommittedSequences(ctx context.Context, _ []kernel.ID) (map[kernel.ID]int64, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// stop must end a watermark check blocked in its read, so the check never
// outlives the pool that serve closes next.
func TestWatermarkStops(t *testing.T) {
	hub := realtime.NewHub()
	_, unregister, err := hub.Register(context.Background(), realtime.Connection{Organization: kernel.ID{1}, Account: kernel.ID{2}, Session: kernel.ID{3}}, 1)
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

type blockingEventCleaner struct {
	started, canceled, release chan struct{}
}

func (c blockingEventCleaner) ExpireEvents(ctx context.Context, _ time.Time) error {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	return ctx.Err()
}

func TestRetentionStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cleaner := blockingEventCleaner{make(chan struct{}), make(chan struct{}), make(chan struct{})}
		// The parent stays alive, as when serve returns a listener error.
		stop := startRealtimeWorker(context.Background(), realtime.Retention{Events: cleaner}, time.Hour)
		defer func() {
			close(cleaner.release)
			stop()
		}()
		synctest.Wait()
		select {
		case <-cleaner.started:
		default:
			t.Fatal("retention did not start before the first tick")
		}
		stopped := make(chan struct{})
		go func() {
			stop()
			close(stopped)
		}()
		synctest.Wait()
		select {
		case <-cleaner.canceled:
		default:
			t.Fatal("stop did not cancel a blocked clean-up")
		}
		select {
		case <-stopped:
			t.Fatal("stop returned before clean-up finished")
		default:
		}
		// Let clean-up return before stop can let serve close the pool.
		cleaner.release <- struct{}{}
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Fatal("stop did not wait for clean-up to return")
		}
	})
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

func TestMaxStreams(t *testing.T) {
	for _, tt := range []struct {
		name, value string
		want        int
	}{
		{name: "unset", want: 5000},
		{name: "one", value: "1", want: 1},
		{name: "custom", value: "7000", want: 7000},
		{name: "empty", value: ""},
		{name: "zero", value: "0"},
		{name: "negative", value: "-1"},
		{name: "unparsable", value: "invalid"},
		{name: "fraction", value: "1.5"},
		{name: "whitespace", value: " 1"},
		{name: "overflow", value: "999999999999999999999999999999"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RIBBITTO_MAX_STREAMS", tt.value)
			if tt.name == "unset" {
				if err := os.Unsetenv("RIBBITTO_MAX_STREAMS"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := maxStreams()
			if got != tt.want || (err != nil) != (tt.want == 0) {
				t.Fatalf("maxStreams() = %d, %v; want %d, invalid=%t", got, err, tt.want, tt.want == 0)
			}
			if tt.want == 0 {
				if err := serve(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "RIBBITTO_MAX_STREAMS") {
					t.Fatalf("invalid stream cap did not prevent startup: %v", err)
				}
			}
		})
	}
}
