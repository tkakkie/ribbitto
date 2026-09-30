package realtime_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// TestStreamCost measures what the delivery loop costs per post as the
// number of open streams grows (#215). It is a measurement, not a check: it
// runs only with RIBBITTO_STREAM_COST=1, never in CI, and reports through
// t.Log (run with -v). Tunables, all optional:
//
//	RIBBITTO_STREAM_COST_STEPS     streams per step, default 1,10,100,1000,5000
//	RIBBITTO_STREAM_COST_RATE      posts per second, default 10
//	RIBBITTO_STREAM_COST_DURATION  posting time per step, default 10s
//	RIBBITTO_STREAM_COST_POOL      pool size, default pgxpool's max(4, CPUs)
//
// Every stream belongs to the same member of one organisation and follows
// the same channel. The renderer reads the message with its authors (#206)
// but renders no HTML: this measures database cost.
func TestStreamCost(t *testing.T) {
	if os.Getenv("RIBBITTO_STREAM_COST") != "1" {
		t.Skip("set RIBBITTO_STREAM_COST=1 to measure the stream's cost")
	}
	// Checked before anything connects. pgtest then creates a database of
	// its own on that server and drops it afterwards; nothing else on the
	// server is touched. A loopback address does not prove the server is
	// disposable: it could be a tunnel.
	if err := requireLoopback(os.Getenv("RIBBITTO_TEST_DATABASE_URL")); err != nil {
		t.Fatal(err)
	}
	steps := envInts(t, "RIBBITTO_STREAM_COST_STEPS", []int{1, 10, 100, 1000, 5000})
	rate := envInts(t, "RIBBITTO_STREAM_COST_RATE", []int{10})[0]
	duration := envDuration(t, "RIBBITTO_STREAM_COST_DURATION", 10*time.Second)

	// NewEmpty creates a database from template0 and drops only that one
	// afterwards (pgtest.New would also clear other runs' unfinished
	// templates); the benchmark migrates it itself.
	fixturePool := pgtest.NewEmpty(t)
	db := stdlib.OpenDBFromPool(fixturePool)
	if err := postgres.Migrate(t.Context(), db, "up", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	fixture := pgtest.OrganizationWithOwner(t, fixturePool, "acme", "general")
	// Config returns a copy, pointing at pgtest's database.
	config := fixturePool.Config()
	if size := os.Getenv("RIBBITTO_STREAM_COST_POOL"); size != "" {
		n, err := strconv.Atoi(size)
		if err != nil || n < 1 {
			t.Fatalf("RIBBITTO_STREAM_COST_POOL=%q", size)
		}
		config.MaxConns = int32(n)
	}
	queries := postgres.NewQueryCounter()
	config.ConnConfig.Tracer = queries
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	m := authz.Membership{Organization: domain.Organization{ID: fixture.OrganizationID, Slug: "acme"}, Member: domain.Member{ID: fixture.MemberID}}
	hub := realtime.NewHub()
	posting := message.NewWithNotifier(postgres.NewPostingStore(pool), hub)
	events := &countingReader{inner: postgres.NewEventReader(pool)}
	stream := realtime.Stream{
		Hub: hub, Events: events, Authorizer: authz.New(postgres.NewAuthzStore(pool)),
		Renderer: readingRenderer{messages: postgres.MessageReader{Pool: pool}, membership: m},
	}
	sub := realtime.Subscription{Organization: fixture.OrganizationID, OrganizationSlug: "acme", Account: fixture.AccountID, Channel: fixture.Channel.ID}

	t.Logf("%s/%s, %d CPUs, %s; pool max %d; batch %d; %d posts/s for %s per step",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version(), pool.Config().MaxConns, realtime.DefaultBatchSize, rate, duration)
	t.Log("| streams | posts scheduled/completed/missed | deliveries | queries/post | tx statements/post | empty reads/post | queries/delivery | tx statements/delivery | empty reads/delivery | mean empty-acquire wait | p50 | p95 | result |")
	t.Log("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	highest := 0
	for _, n := range steps {
		r := measureStep(t, stream, posting, pool, queries, events, m, sub, n, rate, duration)
		verdict := "pass"
		switch {
		case r.missed > 0 || r.completed < r.scheduled:
			verdict = fmt.Sprintf("fail: underloaded, %d of %d posts completed", r.completed, r.scheduled)
		case r.missing > 0:
			verdict = fmt.Sprintf("fail: %d deliveries missing after the drain", r.missing)
		case r.p95 > time.Second:
			verdict = "fail: p95 over 1 s"
		case r.meanWait > r.p95/2:
			verdict = "fail: mean empty-acquire wait over half of p95"
		}
		t.Logf("| %d | %d/%d/%d | %d | %.1f | %.1f | %.1f | %.2f | %.2f | %.2f | %s | %s | %s | %s |", n, r.scheduled, r.completed, r.missed, r.deliveries,
			per(r.queries, r.completed), per(r.txStatements, r.completed), per(r.emptyReads, r.completed),
			per(r.queries, r.deliveries), per(r.txStatements, r.deliveries), per(r.emptyReads, r.deliveries),
			r.meanWait.Round(time.Microsecond), r.p50.Round(time.Microsecond), r.p95.Round(time.Microsecond), verdict)
		if verdict != "pass" {
			t.Logf("highest passing step: %d streams; first failing step: %d streams (%s)", highest, n, verdict)
			return
		}
		highest = n
	}
	t.Logf("every step passed: the ceiling is above %d streams (a lower bound)", highest)
}

type stepResult struct {
	scheduled, completed, missed      int
	deliveries, missing               int
	queries, txStatements, emptyReads int64
	meanWait, p50, p95                time.Duration
}

func per(n int64, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// measureStep opens n streams from the current sequence, posts at rate for
// duration, waits for every delivery or a drain deadline, and closes them.
// Counts cover the posting window and the drain, not the streams' start.
func measureStep(t *testing.T, stream realtime.Stream, posting *message.Service, pool *pgxpool.Pool, queries *postgres.QueryCounter,
	events *countingReader, m authz.Membership, sub realtime.Subscription, n, rate int, duration time.Duration) stepResult {
	t.Helper()
	var cursor int64
	if err := pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id = $1", sub.Organization).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sink := &collector{}
	readsBefore := events.calls.Load()
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { _, _ = stream.Run(ctx, sub, cursor, sink) })
	}
	// Every stream has read once (and found nothing) before posting starts.
	for deadline := time.Now().Add(time.Minute); events.calls.Load()-readsBefore < int64(n); {
		if time.Now().After(deadline) {
			t.Fatalf("%d streams did not all start within a minute", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	q0, s0, empty0 := queries.Counts(), pool.Stat(), events.empty.Load()
	// Posts are scheduled at the fixed rate whatever their latency (an open
	// loop), with at most one second's worth in flight: a post that would
	// exceed it is counted as missed, and the step as underloaded, rather
	// than silently lowering the offered load.
	var (
		mu       sync.Mutex
		returned = map[int64]time.Time{}
		failed   error
		posters  sync.WaitGroup
	)
	inFlight := make(chan struct{}, rate)
	scheduled := int(duration.Seconds() * float64(rate))
	interval := time.Second / time.Duration(rate)
	missed := 0
	start := time.Now()
	for i := range scheduled {
		time.Sleep(time.Until(start.Add(time.Duration(i) * interval)))
		select {
		case inFlight <- struct{}{}:
		default:
			missed++
			continue
		}
		posters.Go(func() {
			defer func() { <-inFlight }()
			posted, err := posting.Post(ctx, m, sub.Channel, "cost")
			at := time.Now()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = err
				return
			}
			returned[posted.EventSeq] = at
		})
	}
	posters.Wait()
	if failed != nil {
		t.Fatal(failed)
	}
	want := n * len(returned)
	for drain := time.Now().Add(30 * time.Second); sink.count() < want && time.Now().Before(drain); {
		time.Sleep(10 * time.Millisecond)
	}
	q1, s1, empty1 := queries.Counts(), pool.Stat(), events.empty.Load()
	cancel()
	wg.Wait()

	r := stepResult{scheduled: scheduled, completed: len(returned), missed: missed, deliveries: sink.count()}
	r.missing = max(want-r.deliveries, 0)
	r.queries = q1.Queries - q0.Queries
	r.txStatements = (q1.Begins + q1.Commits + q1.Rollbacks) - (q0.Begins + q0.Commits + q0.Rollbacks)
	r.emptyReads = empty1 - empty0
	// Only acquisitions that found the pool empty had to wait; averaging
	// over all of them would dilute the wait.
	if waited := s1.EmptyAcquireCount() - s0.EmptyAcquireCount(); waited > 0 {
		r.meanWait = (s1.EmptyAcquireWaitTime() - s0.EmptyAcquireWaitTime()) / time.Duration(waited)
	}
	// A delivery that lands before Post returns is a negative sample.
	var latencies []time.Duration
	for _, d := range sink.all() {
		if at, ok := returned[d.seq]; ok {
			latencies = append(latencies, d.at.Sub(at))
		}
	}
	slices.Sort(latencies)
	if len(latencies) > 0 {
		r.p50 = latencies[len(latencies)*50/100]
		r.p95 = latencies[min(len(latencies)*95/100, len(latencies)-1)]
	}
	return r
}

// countingReader counts reads, and reads that returned nothing.
type countingReader struct {
	inner        realtime.EventReader
	calls, empty atomic.Int64
}

func (c *countingReader) EventsAfter(ctx context.Context, org domain.ID, after int64, limit int) ([]domain.Event, error) {
	events, err := c.inner.EventsAfter(ctx, org, after, limit)
	c.calls.Add(1)
	if err == nil && len(events) == 0 {
		c.empty.Add(1)
	}
	return events, err
}

// readingRenderer reads the message as the web renderer does, without HTML.
type readingRenderer struct {
	messages   postgres.MessageReader
	membership authz.Membership
}

func (r readingRenderer) Render(ctx context.Context, _ realtime.Subscription, e domain.Event) (realtime.Outgoing, error) {
	if _, err := r.messages.One(ctx, r.membership, e.ChannelID, e.Seq); err != nil {
		return realtime.Outgoing{}, err
	}
	return realtime.Outgoing{ID: e.Seq, Name: "message"}, nil
}

type delivery struct {
	seq int64
	at  time.Time
}

// collector is every stream's sender: it records when each event arrived.
type collector struct {
	mu         sync.Mutex
	deliveries []delivery
}

func (c *collector) Send(_ context.Context, out realtime.Outgoing) error {
	at := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deliveries = append(c.deliveries, delivery{out.ID, at})
	return nil
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deliveries)
}

func (c *collector) all() []delivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.deliveries)
}

// requireLoopback refuses a database URL unless every host it may connect
// to, fallbacks included, is a loopback name, as cmd/seed does.
func requireLoopback(url string) error {
	if url == "" {
		return fmt.Errorf("RIBBITTO_TEST_DATABASE_URL is unset")
	}
	config, err := pgconn.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("parsing RIBBITTO_TEST_DATABASE_URL: %w", err)
	}
	hosts := []string{config.Host}
	for _, f := range config.Fallbacks {
		hosts = append(hosts, f.Host)
	}
	for _, host := range hosts {
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("the stream cost benchmark runs only against a loopback PostgreSQL server, not %q", host)
		}
	}
	return nil
}

func TestStreamCostRefusesANonLoopbackServer(t *testing.T) {
	for url, ok := range map[string]bool{
		"postgres://u:p@127.0.0.1:5432/postgres":           true,
		"postgres://u:p@localhost/postgres":                true,
		"postgres://u:p@[::1]:5432/postgres":               true,
		"postgres://u:p@db.example.org/postgres":           false,
		"postgres://u:p@192.0.2.1/postgres":                false,
		"postgres://u:p@localhost,192.0.2.1/postgres":      false,
		"postgres://u:p@127.0.0.1/postgres?host=192.0.2.1": false,
		"": false,
	} {
		if err := requireLoopback(url); (err == nil) != ok {
			t.Errorf("requireLoopback(%q) = %v, want ok=%t", url, err, ok)
		}
	}
}

func envInts(t *testing.T, name string, fallback []int) []int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	var out []int
	for _, field := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || n < 1 {
			t.Fatalf("%s=%q: want positive integers", name, raw)
		}
		out = append(out, n)
	}
	return out
}

func envDuration(t *testing.T, name string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		t.Fatalf("%s=%q: want a positive duration", name, raw)
	}
	return d
}
