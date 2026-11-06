package postgres_test

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// TestUnreadListBench is Stage 1 of #2191. It needs separate maintainer
// approval of the exact SHA and start. Never use a shared server. After that
// approval, the operator runs this entire recipe from this worktree (retain
// verbose output outside the container). The outer clock covers container
// preparation, build, Stage 0, initialization, measurement and removal:
//
//	perl -MPOSIX -e '
//	  $p=fork; defined $p or die $!;
//	  if (!$p) { POSIX::setsid()>0 or die $!; exec @ARGV; die $!; }
//	  $SIG{ALRM}=sub { kill "KILL", -$p; die "INCONCLUSIVE: outer two-hour cap; remove recorded container ID\n"; };
//	  alarm 7200; waitpid($p,0);
//	  exit(($? & 127) ? 128+($? & 127) : $? >> 8);
//	' sh -eu -c '
//	  cid=$(docker run -d --read-only --log-driver=none \
//	    --tmpfs /var/lib/postgresql:rw,size=3000000000 \
//	    --tmpfs /var/run/postgresql:rw,size=16m --tmpfs /tmp:rw,size=16m \
//	    -e PGDATA=/var/lib/postgresql/18/docker -e POSTGRES_HOST_AUTH_METHOD=trust \
//	    -p 127.0.0.1:55439:5432 postgres:18 \
//	    -c max_connections=20 -c max_wal_size=256MB -c min_wal_size=64MB \
//	    -c wal_keep_size=0 -c temp_file_limit=64MB -c logging_collector=off)
//	  printf "disposable container ID=%s\n" "$cid"
//	  trap "docker rm -f \"$cid\" >/dev/null" EXIT
//	  until docker exec "$cid" pg_isready -h 127.0.0.1 -U postgres -d postgres >/dev/null; do sleep 1; done
//	  export RIBBITTO_TEST_DATABASE_URL="host=127.0.0.1 port=55439 user=postgres dbname=postgres sslmode=disable"
//	  RIBBITTO_REQUIRE_DB=1 RIBBITTO_UNREAD_LIST_DISPOSABLE=1 RIBBITTO_UNREAD_LIST_BENCH=1 \
//	    go test -count=1 -run "^TestUnreadListBench$" -v -timeout 120m ./internal/conversation/internal/postgres
//	'
//
// In another terminal, set cid to the printed ID and monitor the whole data
// volume, including WAL, temp files, every database and filesystem overhead:
//
//	while docker inspect "$cid" >/dev/null 2>&1; do docker exec "$cid" sh -c 'du -sk /var/lib/postgresql; df -k /var/lib/postgresql'; sleep 5; done
//
// The tmpfs is a hard 3,000,000,000-byte data limit. WAL settings are soft
// checkpoint/retention targets and temp_file_limit is per backend; neither
// replaces that volume limit. No external tablespaces or WAL mounts. At ENOSPC
// PostgreSQL can fail statements or stop; the result is INCONCLUSIVE. The
// harness retains raw samples and cluster usage/high-water logs, stops at
// 2,744,000,000 bytes and checks all databases + pg_ls_waldir + pg_ls_tmpdir
// for every tablespace + other PGDATA files (global, transaction status, etc.).
// An unreadable component, including missing admin privileges, stops the run;
// there is no partial-accounting fallback. A five-second admin observer also
// checks during preparation, Stage 0 and long statements; timed A/B uses one
// physical connection, serially. The hard volume covers polling gaps.
//
// The test's clock starts before opt-in checks: work ends at 115 minutes,
// reserving five minutes for pool closure and pgtest's bounded database drops
// inside the two-hour job. Stage 0 shares that context; pgtest initialization
// is independently bounded to 45 seconds and fixture statements to 30 seconds.
// The outer alarm can fire sooner because it also counts container/build time.
// go test -timeout panics and bypasses t.Cleanup; the outer alarm kills only
// its own process group and can bypass the shell trap too. On either hard stop,
// retain output, inspect the recorded container ID, then docker rm -f "$cid"
// to remove the entire dedicated server, clones, template, WAL and tmpfs.
// Never remove an unverified ID. A cap stop is INCONCLUSIVE even without samples.
//
// Both opt-ins and the exact selector are required; ordinary test commands
// skip before any SQL even if the benchmark variable was inherited. No
// parameters reduce the scenarios, warm-ups, repetitions or approved budgets.
func TestUnreadListBench(t *testing.T) {
	started := time.Now()
	const cap = 2 * time.Hour
	const cleanupReserve = 5 * time.Minute
	job, finish := context.WithDeadline(t.Context(), started.Add(cap))
	ctx, cancel := context.WithDeadline(job, started.Add(cap-cleanupReserve))
	t.Cleanup(finish)
	t.Cleanup(cancel)
	if os.Getenv("RIBBITTO_UNREAD_LIST_BENCH") != "1" {
		t.Skip("set RIBBITTO_UNREAD_LIST_BENCH=1 outside CI with the exact selector and disposable opt-in")
	}
	if os.Getenv("CI") != "" || flag.Lookup("test.run").Value.String() != "^TestUnreadListBench$" {
		t.SkipNow()
	}
	unreadListBenchLoopback(t)
	observer, err := pgxpool.New(ctx, pgtest.AdminURL(t))
	if err != nil {
		t.Fatal("INCONCLUSIVE: invalid disposable admin configuration (details suppressed)")
	}
	t.Cleanup(observer.Close)
	g := unreadListBenchGuard{t: t, ctx: ctx, observe: observer, cancel: cancel}
	t.Cleanup(func() {
		t.Logf("whole job including preparation/Stage 0/cleanup elapsed=%s; cluster high-water=%d bytes; partial output retained if inconclusive", time.Since(started), g.highWater)
	})
	g.check()
	monitor, stop := context.WithCancel(job)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-monitor.Done():
				return
			case <-ticker.C:
				if g.usage(monitor) != nil {
					return
				}
			}
		}
	}()
	defer func() { stop(); <-done }()
	// Stage 0 must finish successfully before any stress data or timing.
	if !t.Run("correctness", func(t *testing.T) { unreadListCorrectness(ctx, t) }) ||
		!t.Run("page-statements", func(t *testing.T) { unreadListPageStatements(ctx, t) }) {
		t.Fatal("INCONCLUSIVE: Stage 0 failed; no timing taken")
	}
	g.check()
	base := pgtest.New(t)
	g.check()
	trace := &unreadListBenchTrace{unreadListTrace: &unreadListTrace{QueryCounter: platform.NewQueryCounter()}}
	wire := &unreadListBenchWire{}
	trace.wire = wire
	config := base.Config()
	// One physical connection remains open for setup and every snapshot. Refuse
	// a replacement backend instead of silently changing the session/cache.
	config.MaxConns, config.MinConns = 1, 1
	config.MaxConnLifetime, config.MaxConnIdleTime = 0, 0
	config.ConnConfig.Tracer = trace
	config.ConnConfig.RuntimeParams["statement_timeout"] = "30000ms"
	config.ConnConfig.RuntimeParams["temp_file_limit"] = "65536" // kB, per backend; also bounded by the volume.
	config.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("connecting disposable benchmark server failed")
		}
		return &unreadListBenchConn{Conn: conn, wire: wire}, nil
	}
	config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		if trace.conn != nil {
			return fmt.Errorf("benchmark connection changed; results inconclusive")
		}
		trace.conn = conn
		element, _ := conn.TypeMap().TypeForOID(pgtype.Int8multirangeOID)
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "_int8multirange", OID: pgtype.Int8multirangeArrayOID, Codec: &pgtype.ArrayCodec{ElementType: element}})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	requireNoError(t, err)
	t.Cleanup(pool.Close)
	base.Close()
	g.pool = pool
	g.check()
	var version string
	requireNoError(t, pool.QueryRow(ctx, "SELECT version()").Scan(&version))
	t.Logf("%s; Go=%s %s/%s CPUs=%d; one physical connection, serial; 3 warm-ups then 20 samples per scenario/form; nearest-rank p50=10th p95=19th", version, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	t.Log("30s session default and 60s SET LOCAL are PER-STATEMENT limits, never page limits; BEGIN/SET LOCAL/COMMIT reported separately; wire counts include protocol overhead")
	scenarios := []unreadListBenchScenario{
		g.seed("normal", 10, 50, false),
		g.seed("channels-100", 100, 60, false),
		g.seed("largest", 500, 60, true),
	}
	caughtUp := scenarios[0]
	caughtUp.name, caughtUp.f.member = "caught-up", caughtUp.caughtUp
	scenarios = []unreadListBenchScenario{scenarios[0], scenarios[1], caughtUp, scenarios[2]}
	for _, table := range []string{"organization", "channel", "topic", "message", "channel_read", "read_range", "topic_read_floor"} {
		g.exec("ANALYZE " + table)
	}
	t.Log("ANALYZE complete; settings and indexes (outside intervals):")
	g.lines(`SELECT name||'='||setting||coalesce(unit,'') FROM pg_settings WHERE name IN ('shared_buffers','work_mem','effective_cache_size','jit','max_parallel_workers_per_gather','random_page_cost','statement_timeout','temp_file_limit','plan_cache_mode') ORDER BY name`)
	g.lines(`SELECT indexdef FROM pg_indexes WHERE tablename IN ('message','read_range','topic_read_floor') ORDER BY indexname`)
	// Validate every scenario before even the first warm-up takes an interval.
	for _, scenario := range scenarios {
		g.check()
		g.require(unreadListVerifyData(ctx, pool, scenario.f, unreadListBenchExpected(scenario.name)))
		t.Logf("independent data sanity passed: scenario=%s expected=%+v", scenario.name, unreadListBenchExpected(scenario.name))
		expected := 51 // Includes the lookahead row beyond the 50 displayed rows.
		switch scenario.name {
		case "normal":
			expected = 50
		case "caught-up":
			expected = 0
		}
		g.require(platform.InSnapshotWithStatementTimeout(ctx, pool, time.Minute, func(s platform.Snapshot) error {
			return unreadListSanity(ctx, s, scenario.f, expected)
		}))
		g.check()
	}
	for _, form := range []string{"V0", "V1"} {
		results := make([][]unreadListBenchSample, len(scenarios))
		// Cycling pairs normal with stressed data, without confounding order
		// with scenario: each scenario sees ten A/B and ten B/A samples.
		for rep := -3; rep < 20; rep++ {
			for i, scenario := range scenarios {
				g.check()
				sample := g.sample(scenario, form, rep)
				if rep >= 0 {
					results[i] = append(results[i], sample)
					t.Logf("raw scenario=%s form=%s repetition=%d order=%s A=%+v B=%+v T=%s shared=%s slowest-statement=%s names=%v per-statement=%+v", scenario.name, form, rep+1, sample.order, sample.a, sample.b, sample.total, sample.shared, sample.slowest, sample.names, sample.statements)
				}
				g.check()
			}
		}
		t.Logf("cumulative prepared plan counts after timed samples form=%s (before this form's diagnostics; V1 includes earlier V0 diagnostics):", form)
		g.lines(`SELECT name||' generic_plans='||generic_plans||' custom_plans='||custom_plans||' statement='||split_part(statement,chr(10),1) FROM pg_prepared_statements ORDER BY name`)
		for i, scenario := range scenarios {
			unreadListBenchReport(t, scenario.name, form, results[i])
			g.diagnostics(scenario, form)
			g.pageTrace(scenario, form)
		}
	}
}

func unreadListSanity(ctx context.Context, s platform.Snapshot, f unreadListFixture, expected int) error {
	v0, err := unreadListPage(ctx, s, f.member, f.channels, nil, "V0", "")
	if err != nil {
		return fmt.Errorf("checking V0 before timing: %w", err)
	}
	v1, err := unreadListPage(ctx, s, f.member, f.channels, nil, "V1", "")
	if err != nil {
		return fmt.Errorf("checking V1 before timing: %w", err)
	}
	return unreadListSanityRows(v0, v1, expected)
}

func unreadListSanityRows(v0, v1 []unreadListRow, expected int) error {
	if len(v0) != expected || len(v1) != expected {
		return fmt.Errorf("before timing: V0 rows=%d V1 rows=%d, want %d", len(v0), len(v1), expected)
	}
	if !slices.Equal(v0, v1) {
		return fmt.Errorf("before timing: V0 and V1 differ")
	}
	return nil
}

func unreadListBenchLoopback(t *testing.T) {
	t.Helper()
	if os.Getenv("RIBBITTO_UNREAD_LIST_DISPOSABLE") != "1" {
		t.Fatal("refusing benchmark without explicit disposable-server opt-in")
	}
	config, err := pgxpool.ParseConfig(pgtest.AdminURL(t))
	if err != nil {
		t.Fatal("invalid test database configuration (details suppressed to protect credentials)")
	}
	if config.ConnConfig.User != "postgres" || config.ConnConfig.Database != "postgres" {
		t.Fatal("benchmark requires the disposable postgres/admin connection specified in docs/database-tests.md")
	}
	hosts := []string{config.ConnConfig.Host}
	for _, fallback := range config.ConnConfig.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	for _, host := range hosts {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			t.Fatal("refusing non-numeric-loopback test database, including fallbacks and Unix sockets")
		}
	}
}

type unreadListBenchWire struct{ sent, received atomic.Uint64 }
type unreadListBenchConn struct {
	net.Conn
	wire *unreadListBenchWire
}

func (c *unreadListBenchConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.wire.received.Add(uint64(n))
	return n, err
}

func (c *unreadListBenchConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.wire.sent.Add(uint64(n))
	return n, err
}

type unreadListBenchQuery struct {
	sql  string
	args []any
}

type unreadListBenchTrace struct {
	*unreadListTrace
	conn           *pgx.Conn
	wire           *unreadListBenchWire
	start          time.Time
	slowest        time.Duration
	rows           int64
	sent, received uint64
	capture        bool
	queries        []unreadListBenchQuery
	statements     []unreadListBenchMetric
}

func (p *unreadListBenchTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx = p.unreadListTrace.TraceQueryStart(ctx, conn, data)
	if p.capture && strings.HasPrefix(data.SQL, "-- name:") {
		p.queries = append(p.queries, unreadListBenchQuery{data.SQL, slices.Clone(data.Args)})
	}
	p.start = time.Now()
	p.sent, p.received = p.wire.sent.Load(), p.wire.received.Load()
	return ctx
}

func (p *unreadListBenchTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	elapsed := time.Since(p.start)
	p.slowest = max(p.slowest, elapsed)
	if data.Err == nil && data.CommandTag.Select() {
		p.rows += data.CommandTag.RowsAffected()
	}
	p.statements = append(p.statements, unreadListBenchMetric{elapsed, p.wire.sent.Load() - p.sent, p.wire.received.Load() - p.received, data.CommandTag.RowsAffected()})
}

type unreadListBenchMetric struct {
	Elapsed        time.Duration
	Sent, Received uint64
	Decoded        int64
}

type unreadListBenchSample struct {
	a, b                   unreadListBenchMetric
	total, shared, slowest time.Duration
	order                  string
	names                  []string
	statements             []unreadListBenchMetric
}

func (g *unreadListBenchGuard) sample(scenario unreadListBenchScenario, form string, rep int) unreadListBenchSample {
	g.t.Helper()
	trace := g.pool.Config().ConnConfig.Tracer.(*unreadListBenchTrace)
	wire := trace.wire
	runtime.GC() // Outside all three intervals, never between A and B.
	trace.names, trace.slowest = nil, 0
	trace.statements = nil
	var sample unreadListBenchSample
	start := time.Now()
	err := platform.InSnapshotWithStatementTimeout(g.ctx, g.pool, time.Minute, func(s platform.Snapshot) error {
		sample.shared = time.Since(start)
		// The sidebar's readable channels and displayed topics are inputs;
		// their loading belongs to the page trace, not the four-statement B.
		a := func() error {
			_, err := unreadListPage(g.ctx, s, scenario.f.member, scenario.f.channels, nil, form, "")
			return err
		}
		b := func() error {
			_, err := compose.PageCountsIn(s).(conversation.SidebarCounter).ReadCounts(g.ctx, scenario.f.member, scenario.f.channels[0], scenario.f.channels, scenario.topics, nil)
			return err
		}
		first, second := a, b
		left, right := &sample.a, &sample.b
		sample.order = "A/B"
		if rep%2 != 0 {
			first, second, left, right, sample.order = b, a, &sample.b, &sample.a, "B/A"
		}
		body := time.Now()
		if err := unreadListBenchMeasure(trace, wire, left, first); err != nil {
			return err
		}
		if err := unreadListBenchMeasure(trace, wire, right, second); err != nil {
			return err
		}
		sample.total = time.Since(body)
		start = time.Now()
		return nil
	})
	sample.shared += time.Since(start)
	g.require(err)
	sample.names, sample.slowest = slices.Clone(trace.names), trace.slowest
	sample.statements = slices.Clone(trace.statements)
	added := 1
	if form == "V1" {
		added = 2
	}
	if len(trace.names) != 4+added+3 { // BEGIN, SET LOCAL, COMMIT.
		g.t.Fatalf("unexpected snapshot statements: %v", trace.names)
	}
	if rep == 0 {
		g.t.Logf("unread path form=%s statements=%d budget<=6 pass=%t; B=4 A=%d, none per row", form, 4+added, 4+added <= 6, added)
	}
	return sample
}

func unreadListBenchMeasure(trace *unreadListBenchTrace, wire *unreadListBenchWire, metric *unreadListBenchMetric, fn func() error) error {
	sent, received, rows := wire.sent.Load(), wire.received.Load(), trace.rows
	start := time.Now()
	err := fn()
	metric.Elapsed = time.Since(start)
	metric.Sent, metric.Received, metric.Decoded = wire.sent.Load()-sent, wire.received.Load()-received, trace.rows-rows
	return err
}

func unreadListBenchReport(t *testing.T, name, form string, samples []unreadListBenchSample) {
	t.Helper()
	percentile := func(which string, rank int) time.Duration {
		values := make([]time.Duration, 0, 20)
		for _, s := range samples {
			v := s.total
			switch which {
			case "A":
				v = s.a.Elapsed
			case "B":
				v = s.b.Elapsed
			}
			values = append(values, v)
		}
		slices.Sort(values)
		return values[rank-1]
	}
	b50, b95 := percentile("B", 10), percentile("B", 19)
	slowest := time.Duration(0)
	for _, sample := range samples {
		slowest = max(slowest, sample.slowest)
	}
	t.Logf("budget table scenario=%s form=%s: operation | p50/op-to-B <=2 | p95/op-to-B <=4 | normal p95<=50ms | largest p95<=500ms; only A determines pass/fail", name, form)
	for _, op := range []string{"A", "B", "T"} {
		p50, p95 := percentile(op, 10), percentile(op, 19)
		r50, r95 := float64(p50)/float64(b50), float64(p95)/float64(b95)
		normal, largest := "n/a", "n/a"
		if name == "normal" {
			normal = fmt.Sprintf("%t", p95 <= 50*time.Millisecond)
		}
		if name == "largest" {
			largest = fmt.Sprintf("%t", p95 <= 500*time.Millisecond)
		}
		t.Logf("%s | p50=%s ratio=%.3f pass=%t | p95=%s ratio=%.3f pass=%t | %s | %s", op, p50, r50, r50 <= 2, p95, r95, r95 <= 4, normal, largest)
		for _, limit := range []time.Duration{30 * time.Second, time.Minute} {
			t.Logf("%s informational headroom PER-STATEMENT limit=%s / operation-p95=%.1fx; slowest single statement=%s headroom=%.1fx", op, limit, float64(limit)/float64(p95), slowest, float64(limit)/float64(slowest))
		}
	}
	for _, order := range []string{"A/B", "B/A"} {
		var a, b, total []time.Duration
		for _, s := range samples {
			if s.order == order {
				a, b, total = append(a, s.a.Elapsed), append(b, s.b.Elapsed), append(total, s.total)
			}
		}
		t.Logf("order scenario=%s form=%s %s n=%d raw-A=%v raw-B=%v raw-T=%v (overall percentiles use all 20)", name, form, order, len(a), a, b, total)
	}
}

func (g *unreadListBenchGuard) diagnostics(scenario unreadListBenchScenario, form string) {
	g.t.Helper()
	g.check()
	trace := g.pool.Config().ConnConfig.Tracer.(*unreadListBenchTrace)
	trace.capture, trace.queries = true, nil
	// Allocation/heap diagnostics are a separate pass: ReadMemStats and GC
	// policy must not contaminate the direct A/B/T wall-clock samples.
	g.require(platform.InSnapshotWithStatementTimeout(g.ctx, g.pool, time.Minute, func(s platform.Snapshot) error {
		for _, op := range []string{"A", "B"} {
			runtime.GC()
			previous := debug.SetGCPercent(-1)
			func() {
				defer debug.SetGCPercent(previous)
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				if op == "A" {
					_, err := unreadListPage(g.ctx, s, scenario.f.member, scenario.f.channels, nil, form, "")
					g.require(err)
				} else {
					_, err := compose.PageCountsIn(s).(conversation.SidebarCounter).ReadCounts(g.ctx, scenario.f.member, scenario.f.channels[0], scenario.f.channels, scenario.topics, nil)
					g.require(err)
				}
				runtime.ReadMemStats(&after)
				g.t.Logf("allocation diagnostic scenario=%s form=%s op=%s mallocs=%d allocated-bytes=%d heap-growth=%d peak-heap=%d (GC disabled only for diagnostic; end heap is high-water)", scenario.name, form, op, after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc, after.HeapAlloc-before.HeapAlloc, after.HeapAlloc)
			}()
		}
		return nil
	}))
	trace.capture = false
	for _, query := range trace.queries {
		g.check()
		g.require(platform.InSnapshotWithStatementTimeout(g.ctx, g.pool, time.Minute, func(s platform.Snapshot) error {
			rows, err := pgxbridge.Snapshot(s).Query(g.ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query.sql, query.args...)
			if err != nil {
				return err
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			g.t.Logf("EXPLAIN ONLY scenario=%s form=%s statement=%s (execution time, visited rows/loops/filter removals and buffers; excluded from budgets):\n%s", scenario.name, form, strings.Split(query.sql, "\n")[0], strings.Join(lines, "\n"))
			return err
		}))
		g.check()
	}
}

func (g *unreadListBenchGuard) pageTrace(scenario unreadListBenchScenario, form string) {
	trace := g.pool.Config().ConnConfig.Tracer.(*unreadListBenchTrace)
	for _, selected := range []*kernel.ID{nil, &scenario.topics[len(scenario.topics)-1]} {
		g.check()
		trace.names = nil
		before := trace.Counts().Queries
		factory := func(s platform.Snapshot) conversation.PageCounter {
			return unreadListPageCounter{compose.PageCountsIn(s), s, form}
		}
		_, err := conversationpg.NewReader(g.pool, nil, nil, compose.EventCursor, factory).Sidebar(g.ctx, scenario.f.member, scenario.f.channels[0], selected)
		g.require(err)
		g.t.Logf("page trace scenario=%s form=%s selected=%t whole-page=%d (includes SET LOCAL; excludes BEGIN/COMMIT) names=%v", scenario.name, form, selected != nil, trace.Counts().Queries-before, trace.names)
	}
	g.check()
}

// Keep the real sidebar's list order/limit; preparation inside B is performed
// by compose.PageCountsIn, including channel/topic deduplication and arrays.
func unreadListBenchTopics(ctx context.Context, s platform.Snapshot, f unreadListFixture) ([]kernel.ID, error) {
	topics, err := postgres.ReadStoreIn(s).ListTopics(ctx, f.member.Organization.ID, f.channels[0], 50)
	ids := make([]kernel.ID, len(topics))
	for i, topic := range topics {
		ids[i] = topic.ID
	}
	return ids, err
}
