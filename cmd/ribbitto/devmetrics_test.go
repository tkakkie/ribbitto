package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestDevMetricsSetup(t *testing.T) {
	tests := []struct {
		value string
		ok    bool
	}{
		{"127.0.0.1:9090", true},
		{"127.0.0.2:9090", true},
		{"[::1]:9090", true},
		{":9090", false},
		{"0.0.0.0:9090", false},
		{"[::]:9090", false},
		{"localhost:9090", false},
		{"192.0.2.1:9090", false},
		{"127.0.0.1", false},
		{"127.0.0.1:", false},
		{"127.0.0.1:0", false},
		{"[::1]:0", false},
		{"127.0.0.1:http", false},
		{"127.0.0.1:65536", false},
		{"127.0.0.1:99999999999", false},
		{"127.0.0.1:65535", true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			saveMutexSampling(t)
			runtime.SetMutexProfileFraction(7)
			addr, queries, err := devMetricsSetup(tt.value)
			if tt.ok && (err != nil || addr != tt.value || queries == nil) {
				t.Fatalf("devMetricsSetup(%q) = %q, %v, %v; want it enabled", tt.value, addr, queries, err)
			}
			if !tt.ok && (err == nil || addr != "" || queries != nil) {
				t.Fatalf("devMetricsSetup(%q) = %q, %v, %v; want a refusal", tt.value, addr, queries, err)
			}
			want := 7
			if tt.ok {
				want = 5 // The documented development sampling fraction.
			}
			if got := runtime.SetMutexProfileFraction(-1); got != want {
				t.Fatalf("mutex fraction = %d, want %d", got, want)
			}
		})
	}
}

// Without the opt-in, serve gets no address (so it starts no listener) and
// no counter (so OpenPool installs no tracer; its own test covers that).
func TestDevMetricsOffByDefault(t *testing.T) {
	for _, fraction := range []int{0, 7} {
		t.Run(fmt.Sprint(fraction), func(t *testing.T) {
			saveMutexSampling(t)
			runtime.SetMutexProfileFraction(fraction)
			addr, queries, err := devMetricsSetup("")
			if addr != "" || queries != nil || err != nil {
				t.Fatalf("devMetricsSetup(\"\") = %q, %v, %v; want everything off", addr, queries, err)
			}
			if got := runtime.SetMutexProfileFraction(-1); got != fraction {
				t.Fatalf("mutex fraction = %d, want unchanged %d", got, fraction)
			}
		})
	}
}

// These tests change process-wide sampling and must run serially.
func saveMutexSampling(t *testing.T) {
	t.Helper()
	previous := runtime.SetMutexProfileFraction(-1)
	t.Cleanup(func() { runtime.SetMutexProfileFraction(previous) })
}

func TestDevMetricsApplicationIsolation(t *testing.T) {
	for _, value := range []string{"", "127.0.0.1:9090"} {
		t.Run(value, func(t *testing.T) {
			saveMutexSampling(t)
			if _, _, err := devMetricsSetup(value); err != nil {
				t.Fatal(err)
			}
			handler, _, err := buildHandler(t.Context(), lazyPool(t), handlerConfig{})
			if err != nil {
				t.Fatal(err)
			}
			srv := newServer("", handler, serverTimeouts{})
			if srv.Handler == nil || srv.Handler == http.DefaultServeMux {
				t.Fatal("application server must use its explicit handler")
			}
			for _, path := range []string{
				"/metrics", "/debug/pprof/", "/debug/pprof/profile?seconds=1",
				"/debug/pprof/mutex", "/debug/pprof/heap", "/debug/pprof/goroutine",
				"/debug/pprof/block", "/debug/pprof/trace", "/debug/pprof/cmdline", "/debug/pprof/symbol",
			} {
				t.Run(path, func(t *testing.T) {
					w := httptest.NewRecorder()
					srv.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
					if w.Code != http.StatusNotFound {
						t.Fatalf("public GET %s = %d, want 404", path, w.Code)
					}
				})
			}
		})
	}
}

func TestDevMetricsProfiles(t *testing.T) {
	saveMutexSampling(t)
	addr, _, err := devMetricsSetup("127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	srv := newMetricsServer(addr, devMetrics{})
	if srv.Handler == nil || srv.Handler == http.DefaultServeMux {
		t.Fatal("metrics server must use its explicit handler")
	}
	for _, path := range []string{
		"/debug/pprof/", "/debug/pprof/goroutine", "/debug/pprof/block", "/debug/pprof/trace",
		"/debug/pprof/cmdline", "/debug/pprof/symbol", "/debug/pprof/allocs", "/debug/pprof/threadcreate",
	} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("metrics GET %s = %d, want 404", path, w.Code)
			}
		})
	}
	// A real server supplies ServerContextKey and a ResponseController. Shorten
	// its write timeout below the capture duration to prove pprof extends it.
	srv.WriteTimeout = 500 * time.Millisecond
	server := httptest.NewUnstartedServer(srv.Handler)
	server.Config = srv
	server.Start()
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second
	for _, name := range []string{"profile?seconds=1", "mutex", "heap"} {
		t.Run(name, func(t *testing.T) {
			response, err := client.Get(server.URL + "/debug/pprof/" + name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/octet-stream" || len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
				t.Fatalf("GET %s = %d %v, %d bytes; want gzip profile", name, response.StatusCode, response.Header, len(body))
			}
		})
	}
}

type fakeStreams int

func (f fakeStreams) Connections() int { return int(f) }

func TestDevMetricsSnapshot(t *testing.T) {
	saveMutexSampling(t)
	_, queries, err := devMetricsSetup("127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	handler := devMetrics{queries: queries, pool: lazyPool(t), streams: fakeStreams(7)}.handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /metrics = %d %v", w.Code, w.Header())
	}
	// Counters only: the snapshot's shape is fixed, so nothing else (SQL,
	// tokens, IDs, names, content) can appear in it.
	var got map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"delivery":  {"bucket_upper_bounds_ns", "cancelled", "completion_to_send", "compute", "delivered", "denied", "enabled", "failed", "queue", "received", "submit_to_send", "submitted"},
		"database":  {"queries", "transactions_begun", "transactions_committed", "transactions_rolled_back"},
		"pool":      {"acquire_count", "acquire_duration_ns", "acquired_conns", "canceled_acquire_count", "empty_acquire_count", "empty_acquire_wait_ns", "idle_conns", "max_conns", "total_conns"},
		"runtime":   {"goroutines", "heap_inuse_bytes"},
		"streams":   {"open"},
		"scheduler": {"cancelled", "closed", "completed", "eligible", "enabled", "errors", "errors_after_cancellation", "oldest_pending_age_ns", "oldest_running_age_ns", "pending", "processing_duration_bucket_counts", "processing_duration_bucket_upper_bounds_ns", "processing_duration_ns", "refused_capacity", "refused_closed", "registration_limit", "registrations", "rejected_submissions", "running_workers", "snapshot_at_unix_ns", "waiting_workers", "worker_limit"},
	}
	if len(got) != len(want) {
		t.Fatalf("sections = %v", got)
	}
	for section, keys := range want {
		var have []string
		for key, value := range got[section] {
			switch key {
			case "enabled", "closed":
				if _, ok := value.(bool); !ok {
					t.Errorf("%s.%s = %v, want a boolean", section, key, value)
				}
			case "queue", "compute", "completion_to_send", "submit_to_send":
				d, ok := value.(map[string]any)
				if !ok || len(d) != 3 {
					t.Fatalf("invalid duration aggregate: %v", value)
				}
				if _, ok := d["count"].(float64); !ok {
					t.Fatal("duration count must be numeric")
				}
				if _, ok := d["total_ns"].(float64); !ok {
					t.Fatal("duration total must be numeric")
				}
				if buckets, ok := d["bucket_counts"].([]any); !ok || len(buckets) != 6 {
					t.Fatal("duration buckets must have six entries")
				}
			case "bucket_upper_bounds_ns", "processing_duration_bucket_counts", "processing_duration_bucket_upper_bounds_ns":
				if _, ok := value.([]any); !ok {
					t.Errorf("%s.%s = %v, want an array", section, key, value)
				}
			default:
				if _, ok := value.(float64); !ok {
					t.Errorf("%s.%s = %v, want a number", section, key, value)
				}
			}
			have = append(have, key)
		}
		slices.Sort(have)
		if !slices.Equal(have, keys) {
			t.Errorf("%s keys = %v, want %v", section, have, keys)
		}
	}
	if got["streams"]["open"] != float64(7) || got["runtime"]["goroutines"].(float64) < 1 {
		t.Errorf("streams or runtime not read: %v", got)
	}

	for _, r := range []*http.Request{httptest.NewRequest(http.MethodPost, "/metrics", nil), httptest.NewRequest(http.MethodGet, "/", nil)} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == http.StatusOK {
			t.Errorf("%s %s = 200, want refusal", r.Method, r.URL.Path)
		}
	}
}

func TestDevMetricsScheduler(t *testing.T) {
	pool := lazyPool(t)
	synctest.Test(t, func(t *testing.T) {
		m := devMetrics{queries: platform.NewQueryCounter(), pool: pool, streams: fakeStreams(0)}
		check := func(want schedulerMetrics) {
			t.Helper()
			synctest.Wait()
			if want.Enabled {
				want.SnapshotAtUnixNS = time.Now().UnixNano()
			}
			w := httptest.NewRecorder()
			m.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			var got metricsSnapshot
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Scheduler.Enabled != want.Enabled {
				t.Fatalf("scheduler.enabled = %t, want %t", got.Scheduler.Enabled, want.Enabled)
			}
			if got.Scheduler.Pending != want.Pending {
				t.Fatalf("scheduler.pending = %d, want %d", got.Scheduler.Pending, want.Pending)
			}
			if got.Scheduler.Errors != want.Errors {
				t.Fatalf("scheduler.errors = %d, want %d", got.Scheduler.Errors, want.Errors)
			}
			if got.Scheduler.ErrorsAfterCancellation != want.ErrorsAfterCancellation {
				t.Fatalf("scheduler.errors_after_cancellation = %d, want %d", got.Scheduler.ErrorsAfterCancellation, want.ErrorsAfterCancellation)
			}
			if got.Scheduler != want {
				t.Fatalf("scheduler JSON = %+v; want %+v", got.Scheduler, want)
			}
		}
		check(schedulerMetrics{}) // No dependency: disabled, not an idle scheduler.
		q := realtime.NewRecountScheduler(1, 2)
		defer q.Close()
		m.recounts = q
		want := schedulerMetrics{Enabled: true, WorkerLimit: 1, RegistrationLimit: 2, WaitingWorkers: 1,
			ProcessingDurationBucketUpperBoundsNS: [5]int64{1_000_000, 10_000_000, 100_000_000, 1_000_000_000, 10_000_000_000}}
		check(want) // Enabled and idle, with configured limits.
		a, b := q.Register(), q.Register()
		gate := make(chan struct{})
		defer close(gate)
		compute := func(ctx context.Context) (realtime.Outgoing, error) {
			<-gate
			return realtime.Outgoing{}, ctx.Err()
		}
		a.Submit(1, compute)
		synctest.Wait()
		a.Submit(2, compute)
		b.Submit(3, func(context.Context) (realtime.Outgoing, error) {
			return realtime.Outgoing{}, errors.New("live failure")
		})
		if q.Register() != nil || b.Submit(4, nil) {
			t.Fatal("scheduler accepted refused work")
		}
		<-time.NewTimer(3 * time.Millisecond).C
		want.Pending, want.Eligible, want.Registrations = 2, 1, 2
		want.RunningWorkers, want.WaitingWorkers = 1, 0
		want.OldestPendingAgeNS, want.OldestRunningAgeNS = 3_000_000, 3_000_000
		want.RefusedCapacity, want.RejectedSubmissions = 1, 1
		check(want) // Saturated: a successor and another eligible registration.
		a.Cancel()
		want.Pending, want.Cancelled = 1, 2
		check(want) // Cancellation retains the worker and registration until return.
		// Release without closing: the deferred close also unblocks failure cleanup.
		gate <- struct{}{}
		synctest.Wait()
		if completion := <-b.Completions(); completion.Err == nil {
			t.Fatal("live failure did not reach completion")
		}
		want.Pending, want.Eligible, want.RunningWorkers = 0, 0, 0
		want.OldestPendingAgeNS, want.OldestRunningAgeNS = 0, 0
		want.WaitingWorkers, want.Registrations, want.Completed = 1, 1, 2
		want.ProcessingDurationNS = 3_000_000
		want.ProcessingDurationBucketCounts = [6]uint64{1, 1}
		want.Errors, want.ErrorsAfterCancellation = 1, 1 // Live failure and cancelled return are disjoint.
		check(want)
		q.Close()
		if q.Register() != nil || b.Submit(5, compute) {
			t.Fatal("closed scheduler accepted work")
		}
		want.Closed, want.WaitingWorkers, want.Registrations = true, 0, 0
		want.RefusedClosed, want.RejectedSubmissions = 1, 2
		check(want) // Closed still reports enabled and retains lifetime counters.
	})
}

func TestDevMetricsAcquiredConns(t *testing.T) {
	pool := pgtest.NewEmpty(t)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	w := httptest.NewRecorder()
	devMetrics{queries: platform.NewQueryCounter(), pool: pool, streams: fakeStreams(0)}.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	var got metricsSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Pool.AcquiredConns != 1 || got.Pool.TotalConns != got.Pool.IdleConns+got.Pool.AcquiredConns || got.Pool.MaxConns != pgtest.MaxConns {
		t.Fatalf("pool JSON = %+v; want acquired_conns=1, total=idle+acquired, max=%d", got.Pool, pgtest.MaxConns)
	}
}

// lazyPool never connects: pgxpool opens connections on first use, and these
// tests only read its statistics or build handlers.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://ribbitto@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestDevMetricsDelivery(t *testing.T) {
	m := devMetrics{queries: platform.NewQueryCounter(), pool: lazyPool(t), streams: fakeStreams(0)}
	if got := m.snapshot().Delivery; got != (deliveryMetrics{}) {
		t.Fatalf("disabled delivery: %+v", got)
	}
	m.delivery = &realtime.RecountObserver{}
	got := m.snapshot().Delivery
	if !got.Enabled || got.RecountDeliveryStats != m.delivery.Stats() || got.BucketUpperBoundsNS != [5]int64{1e6, 1e7, 1e8, 1e9, 1e10} {
		t.Fatalf("enabled delivery: %+v", got)
	}
}
