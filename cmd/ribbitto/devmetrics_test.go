package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
			addr, queries, err := devMetricsSetup(tt.value)
			if tt.ok && (err != nil || addr != tt.value || queries == nil) {
				t.Fatalf("devMetricsSetup(%q) = %q, %v, %v; want it enabled", tt.value, addr, queries, err)
			}
			if !tt.ok && (err == nil || addr != "" || queries != nil) {
				t.Fatalf("devMetricsSetup(%q) = %q, %v, %v; want a refusal", tt.value, addr, queries, err)
			}
		})
	}
}

// Without the opt-in, serve gets no address (so it starts no listener) and
// no counter (so OpenPool installs no tracer; its own test covers that).
func TestDevMetricsOffByDefault(t *testing.T) {
	addr, queries, err := devMetricsSetup("")
	if addr != "" || queries != nil || err != nil {
		t.Fatalf("devMetricsSetup(\"\") = %q, %v, %v; want everything off", addr, queries, err)
	}
	// The application's handler never serves metrics.
	pool := lazyPool(t)
	handler, _, err := buildHandler(pool, handlerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("public GET /metrics = %d, want 404", w.Code)
	}
}

type fakeStreams int

func (f fakeStreams) Connections() int { return int(f) }

func TestDevMetricsSnapshot(t *testing.T) {
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
		"database": {"queries", "transactions_begun", "transactions_committed", "transactions_rolled_back"},
		"pool":     {"acquire_count", "acquire_duration_ns", "canceled_acquire_count", "empty_acquire_count", "empty_acquire_wait_ns", "idle_conns", "max_conns", "total_conns"},
		"runtime":  {"goroutines", "heap_inuse_bytes"},
		"streams":  {"open"},
	}
	if len(got) != len(want) {
		t.Fatalf("sections = %v", got)
	}
	for section, keys := range want {
		var have []string
		for key, value := range got[section] {
			if _, ok := value.(float64); !ok {
				t.Errorf("%s.%s = %v, want a number", section, key, value)
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
