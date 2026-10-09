package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Development-only metrics and profiles for load tests (#218, #695). They
// are off unless RIBBITTO_DEV_METRICS_ADDR is set, then served on their own listener,
// never on the application's mux, so nothing about them is reachable from
// the public address.

// devMetricsSetup reads RIBBITTO_DEV_METRICS_ADDR. Empty disables metrics:
// it returns no address and no query counter, so serve starts no listener
// and installs no tracer or mutex sampling. Otherwise the address must be a
// loopback IP literal with a fixed port (1–65535); a wildcard, a host name (its
// resolution could change), port 0 (the kernel would pick one the load test
// cannot know) or any other address is refused before the database opens.
func devMetricsSetup(value string) (addr string, queries *platform.QueryCounter, err error) {
	if value == "" {
		return "", nil, nil
	}
	ap, err := netip.ParseAddrPort(value)
	if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 {
		return "", nil, fmt.Errorf("RIBBITTO_DEV_METRICS_ADDR must be a loopback IP address and a non-zero port, such as 127.0.0.1:9090")
	}
	runtime.SetMutexProfileFraction(devMutexProfileFraction)
	return value, platform.NewQueryCounter(), nil
}

// Sample one in five mutex contention events to limit profiling overhead
// while retaining enough samples for the disposable load-test server.
const devMutexProfileFraction = 5

// devMetrics gathers the snapshot. Every source is read-only.
type devMetrics struct {
	queries *platform.QueryCounter
	pool    *pgxpool.Pool
	streams interface{ Connections() int }
}

// metricsSnapshot holds counters only: no SQL, arguments, tokens, IDs,
// names or message content. Counters are cumulative since the process
// started and never reset, so a load test takes one snapshot before and one
// after each step and compares them; a restart starts them again.
type metricsSnapshot struct {
	Database struct {
		Queries   int64 `json:"queries"`
		Begins    int64 `json:"transactions_begun"`
		Commits   int64 `json:"transactions_committed"`
		Rollbacks int64 `json:"transactions_rolled_back"`
	} `json:"database"`
	// Pool uses pgx v5's pgxpool.Stat meanings: acquire_duration covers
	// every successful acquisition, empty_acquire_wait only those that had
	// to wait for a connection.
	Pool struct {
		AcquireCount         int64 `json:"acquire_count"`
		AcquireDurationNS    int64 `json:"acquire_duration_ns"`
		EmptyAcquireCount    int64 `json:"empty_acquire_count"`
		EmptyAcquireWaitNS   int64 `json:"empty_acquire_wait_ns"`
		CanceledAcquireCount int64 `json:"canceled_acquire_count"`
		IdleConns            int32 `json:"idle_conns"`
		TotalConns           int32 `json:"total_conns"`
		MaxConns             int32 `json:"max_conns"`
	} `json:"pool"`
	Runtime struct {
		Goroutines     int    `json:"goroutines"`
		HeapInuseBytes uint64 `json:"heap_inuse_bytes"`
	} `json:"runtime"`
	Streams struct {
		Open int `json:"open"`
	} `json:"streams"`
}

func (m devMetrics) snapshot() metricsSnapshot {
	var s metricsSnapshot
	counts := m.queries.Counts()
	s.Database.Queries, s.Database.Begins, s.Database.Commits, s.Database.Rollbacks = counts.Queries, counts.Begins, counts.Commits, counts.Rollbacks
	stat := m.pool.Stat()
	s.Pool.AcquireCount = stat.AcquireCount()
	s.Pool.AcquireDurationNS = stat.AcquireDuration().Nanoseconds()
	s.Pool.EmptyAcquireCount = stat.EmptyAcquireCount()
	s.Pool.EmptyAcquireWaitNS = stat.EmptyAcquireWaitTime().Nanoseconds()
	s.Pool.CanceledAcquireCount = stat.CanceledAcquireCount()
	s.Pool.IdleConns, s.Pool.TotalConns, s.Pool.MaxConns = stat.IdleConns(), stat.TotalConns(), stat.MaxConns()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	s.Runtime.Goroutines, s.Runtime.HeapInuseBytes = runtime.NumGoroutine(), mem.HeapInuse
	s.Streams.Open = m.streams.Connections()
	return s
}

// handler serves the snapshot and only the three requested profiles on its
// own mux. Importing pprof also registers on DefaultServeMux in init; neither
// server may use that mux, which exposes additional debugging endpoints.
func (m devMetrics) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(m.snapshot())
	})
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.Handle("GET /debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("GET /debug/pprof/heap", pprof.Handler("heap"))
	return mux
}

// newMetricsServer is the metrics listener's server, with the same bounded
// read/idle timeouts as the application's. pprof extends the 10-second write
// deadline by the requested capture duration, allowing a 30-second profile.
func newMetricsServer(addr string, m devMetrics) *http.Server {
	return newServer(addr, m.handler(), serverTimeouts{
		readHeader: readHeaderTimeout, read: readTimeout, idle: idleTimeout, write: 10 * time.Second,
	})
}
