package realtime_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func TestUnreadCountersLockTime(t *testing.T) {
	for _, sql := range []string{"SELECT 1 FOR UPDATE", "SELECT 1 FOR NO KEY UPDATE", "SELECT 1 FOR SHARE", "SELECT 1 FOR KEY SHARE", "SELECT 1", "SELECT for_update, before_update, update_for FROM t", "SELECT 1 for update_suffix"} {
		for _, query := range []string{sql, strings.ToLower(sql), " \t" + strings.ReplaceAll(sql, " ", "\n") + "; "} {
			clock := time.Unix(0, 0)
			ctx, n := counterAccountContext(t.Context(), func() time.Time { return clock })
			ctx = context.WithValue(ctx, counterMoveLockKey{}, 0)
			tracer := counterAccountingTracer{}
			q := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: query})
			clock = clock.Add(time.Millisecond)
			tracer.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 1")})
			want := time.Duration(0)
			if strings.Contains(sql, " FOR ") {
				want = time.Millisecond
			}
			if n.lockTime != want || n.moveMemberLockTime != want {
				t.Fatalf("lock-statement time for %q: total=%v member=%v want=%v", query, n.lockTime, n.moveMemberLockTime, want)
			}
		}
	}
}

func counterObservationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, counterAccountKey{}, (*counterAccount)(nil))
}

// The observer has its own connection, never the operation's transaction.
// Polls count distinct statements seen waiting, not polling frequency or time.
// Brief waits between polls can be missed; this is an observed lower bound.
func (a *counterAccount) observe(ctx context.Context, pool *pgxpool.Pool, pid uint32) (bool, error) {
	id := a.current.Load()
	var waiting bool
	a.observationStatements++
	err := pool.QueryRow(counterObservationContext(ctx), `SELECT EXISTS (
 SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock'
 AND cardinality(pg_blocking_pids(pid))>0)`, pid).Scan(&waiting)
	if err != nil {
		return false, fmt.Errorf("observing counter lock wait: %w", err)
	}
	if waiting && id != 0 && a.current.Load() == id && a.observed.Swap(id) != id {
		a.lockWaits.Add(1)
		if a.move {
			a.moveLockWaits.Add(1)
		}
	}
	return waiting, nil
}

func TestUnreadCountersObservationExcluded(t *testing.T) {
	clock := time.Unix(0, 0)
	ctx, n := counterAccountContext(t.Context(), func() time.Time { return clock })
	tracer := counterAccountingTracer{}
	query := func(ctx context.Context, sql, tag string) {
		q := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
		clock = clock.Add(time.Millisecond)
		tracer.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag(tag)})
	}
	// Even a mutation tag and transaction boundary on an observer are excluded.
	observer := counterObservationContext(ctx)
	query(observer, "begin", "BEGIN")
	query(observer, "SELECT 1 FOR UPDATE", "UPDATE 999")
	query(observer, "commit", "COMMIT")
	query(ctx, "begin", "BEGIN")
	query(ctx, "application", "UPDATE 2")
	query(ctx, "commit", "COMMIT")
	if n.lockTime != 0 || n.statements != 1 || n.rows != 2 || n.controls != 2 || len(n.transactions) != 1 || n.transactions[0] != 3*time.Millisecond {
		t.Fatalf("observation exclusion: statements=%d rows=%d controls=%d samples=%v", n.statements, n.rows, n.controls, n.transactions)
	}
}

func TestUnreadCountersObservationSQLExcluded(t *testing.T) {
	h := newCounterMatrix(t)
	config := h.pool.Config()
	config.ConnConfig.Tracer = counterAccountingTracer{}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, n := counterAccountContext(t.Context(), func() time.Time { return time.Unix(0, 0) })
	// Supply the account deliberately, even though this is a separate connection.
	waiting, err := n.observe(ctx, pool, 0)
	if err != nil || waiting {
		t.Fatalf("idle observation: waiting=%t error=%v", waiting, err)
	}
	if n.observationStatements != 1 || n.statements != 0 || n.rows != 0 || n.controls != 0 || len(n.transactions) != 0 {
		t.Fatalf("observation SQL exclusion: observer=%d statements=%d rows=%d controls=%d samples=%v", n.observationStatements, n.statements, n.rows, n.controls, n.transactions)
	}
}

type counterContentionGate struct {
	inner          pgx.QueryTracer
	clock          *atomic.Int64
	begun          chan uint32
	ready, release chan bool
}

func (g counterContentionGate) TraceQueryStart(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == "begin" {
		g.begun <- conn.PgConn().PID()
	}
	if g.inner != nil {
		ctx = g.inner.TraceQueryStart(ctx, conn, q)
	}
	if q.SQL == "commit" {
		g.ready <- true
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (g counterContentionGate) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryEndData) {
	if g.inner != nil {
		g.inner.TraceQueryEnd(ctx, conn, q)
	}
	if start, _ := ctx.Value(counterLockStartKey{}).(time.Time); !start.IsZero() {
		g.clock.Add(int64(time.Millisecond))
	}
}

func TestUnreadCountersContention(t *testing.T) {
	for _, counters := range []bool{false, true} {
		for _, order := range []string{"move-read", "read-move", "control"} {
			t.Run(fmt.Sprintf("counters=%t/%s", counters, order), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				h := newCounterMatrix(t)
				var clock atomic.Int64
				owner, writer := h.members[0], h.join(t, "writer")
				seq := h.post(t, "seed", writer, nil, nil)
				destination := conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, "Destination").ID
				counterSQLTables(t, h.pool)
				for _, m := range h.members {
					_, w := counterSQLContext(ctx, m, h.channel)
					counterSQLMaterialize(t, h.pool, w)
				}
				start := func(move bool) (*counterAccount, counterContentionGate, <-chan error) {
					opctx, w := counterSQLContext(ctx, owner, h.channel)
					counterSQLUseIntervals(w)
					if move {
						w.moveFrom, w.moves = h.base, []int64{seq}
					}
					opctx, n := counterAccountContext(opctx, func() time.Time { return time.Unix(0, clock.Load()) })
					g := counterContentionGate{clock: &clock, begun: make(chan uint32, 1), ready: make(chan bool, 1), release: make(chan bool, 1)}
					if counters {
						g.inner = counterIntervalAdapter{t: t}
					}
					config := h.pool.Config()
					config.ConnConfig.Tracer = counterAccountingTracer{inner: g}
					pool, err := pgxpool.NewWithConfig(ctx, config)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { cancel(); pool.Close() })
					done := make(chan error, 1)
					go func() {
						var err error
						if move {
							var notice int64
							b := conversationpg.NewBrancher(pool, postingSequence, postingEvents, func(tx platform.Tx) conversation.ReadRangeWriter { return counterBranchReads{tx, &notice} }, nil)
							_, err = b.Branch(opctx, owner, h.channel, conversation.Branch{From: h.base, To: &destination, Messages: []kernel.ID{h.posted[seq]}}, func(conversation.Topic) string { return "notice" })
						} else {
							r := unreadpg.NewReading(pool, counterFeedWriter(), counterTopicWriter(nil), nil)
							err = r.Feed(opctx, unread.Scope{OrganizationID: owner.Organization.ID, ChannelID: h.channel, MemberID: owner.Member.ID}, owner.Member.JoinedEventSeq, seq)
						}
						if err == nil {
							err = w.err
						}
						n.finish(err, false)
						done <- err
					}()
					return n, g, done
				}
				pid := func(g counterContentionGate) uint32 {
					select {
					case pid := <-g.begun:
						return pid
					case <-ctx.Done():
						t.Fatal(ctx.Err())
						return 0
					}
				}
				observe := func(n *counterAccount, pid uint32) bool {
					waiting, err := n.observe(ctx, h.pool, pid)
					if err != nil {
						t.Fatal(err)
					}
					return waiting
				}
				finish := func(g counterContentionGate, done <-chan error) {
					g.release <- true
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				first, g1, done1 := start(order != "read-move")
				pid1 := pid(g1)
				counterGate(t, ctx, g1.ready)
				if observe(first, pid1) {
					t.Fatal("uncontended holder is waiting")
				}
				if order == "control" {
					finish(g1, done1)
				}
				second, g2, done2 := start(order == "read-move")
				pid2 := pid(g2)
				if order != "control" {
					for !observe(second, pid2) {
					}
					if !observe(second, pid2) {
						t.Fatal("gated wait disappeared")
					}
					clock.Add(int64(100 * time.Millisecond))
					finish(g1, done1)
				}
				counterGate(t, ctx, g2.ready)
				if observe(second, pid2) {
					t.Fatal("released waiter is still waiting")
				}
				finish(g2, done2)
				want := uint64(1)
				if order == "control" {
					want = 0
				}
				if first.lockWaits.Load() != 0 || second.lockWaits.Load() != want {
					t.Fatalf("wait attribution: holder=%d waiter=%d want=0/%d", first.lockWaits.Load(), second.lockWaits.Load(), want)
				}
				move, read := first, second
				if order == "read-move" {
					move, read = second, first
				}
				moveWait := uint64(0)
				if order == "read-move" {
					moveWait = 1
				}
				if read.moveLockWaits.Load() != 0 || read.moveLocks != [3]int64{} {
					t.Fatalf("read attributed move work: waits=%d locks=%v", read.moveLockWaits.Load(), read.moveLocks)
				}
				if move.moveLockWaits.Load() != moveWait {
					t.Fatalf("move waits=%d want=%d", move.moveLockWaits.Load(), moveWait)
				}
				locks := [3]int64{}
				if counters {
					locks = [3]int64{2, 2, 4}
				} // Two members, each with two materialised topics.
				if move.moveLocks != locks {
					t.Fatalf("move member locks=%v want=%v", move.moveLocks, locks)
				}
				// Production locks channel_read once; the adapter adds five
				// locks to a read, and eight (including three member scans) to a move.
				readTime, moveTime, memberTime := time.Millisecond, time.Millisecond, time.Duration(0)
				if counters {
					readTime, moveTime, memberTime = 6*time.Millisecond, 9*time.Millisecond, 3*time.Millisecond
				}
				switch order {
				case "move-read":
					readTime += 100 * time.Millisecond
				case "read-move":
					moveTime += 100 * time.Millisecond
				}
				if read.lockTime != readTime || move.lockTime != moveTime || move.moveMemberLockTime != memberTime || read.moveMemberLockTime != 0 {
					t.Fatalf("lock-statement time: read=%v/%v move=%v/%v member=%v/%v", read.lockTime, readTime, move.lockTime, moveTime, move.moveMemberLockTime, memberTime)
				}
				t.Logf("observed-wait lower bound: read=%d move=%d; lock-statement time upper bound: read=%v move=%v (member=%v other=%v)", read.lockWaits.Load(), move.moveLockWaits.Load(), read.lockTime, move.lockTime, move.moveMemberLockTime, move.lockTime-move.moveMemberLockTime)
				if first.observationStatements < 1 || second.observationStatements < 1 {
					t.Fatal("missing observation SQL")
				}
			})
		}
	}
}
