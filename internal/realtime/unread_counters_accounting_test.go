package realtime_test

import (
	"context"
	"errors"
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
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type counterAccountKey struct{}
type counterAccountQueryKey struct{}
type counterWaitQueryKey struct{}
type counterMoveLockKey struct{}
type counterLockStartKey struct{}

// An account belongs to one operation, used by sequential attempts only.
// Concurrent operations get separate accounts; scaffolding gets no account.
type counterAccount struct {
	statements, controls, rows, failures, deadlocks, retries int64
	transactions                                             []time.Duration
	move                                                     bool
	moveLocks                                                [3]int64
	lockTime, moveMemberLockTime                             time.Duration
	lockWaits, moveLockWaits, query, current, observed       atomic.Uint64
	observationStatements                                    int64
	now                                                      func() time.Time
	started                                                  time.Time
	active, deadlocked                                       bool
}

func counterAccountContext(ctx context.Context, now func() time.Time) (context.Context, *counterAccount) {
	a := &counterAccount{now: now}
	if w, _ := ctx.Value(counterSQLKey{}).(*counterSQLWrite); w != nil {
		a.move = len(w.moves) > 0
	}
	return context.WithValue(ctx, counterAccountKey{}, a), a
}

func counterAccountIn(ctx context.Context) *counterAccount {
	a, _ := ctx.Value(counterAccountKey{}).(*counterAccount)
	return a
}

// Call once after the operation returns, including failures before BEGIN.
// Retry marks an actual repeated attempt, never permission to replay a write.
func (a *counterAccount) finish(err error, retry bool) {
	if err != nil {
		a.failures++
	}
	var p *pgconn.PgError
	if a.deadlocked || errors.As(err, &p) && p.Code == "40P01" {
		a.deadlocks++
	}
	if retry {
		a.retries++
	}
	a.deadlocked = false
}

func (a *counterAccount) latency() [3]time.Duration {
	return percentiles(append([]time.Duration(nil), a.transactions...))
}

type counterAccountingTracer struct{ inner pgx.QueryTracer }

type counterControl uint8

const (
	counterApplication counterControl = iota
	counterBegin
	counterEnd
	counterSavepoint
)

func counterClassifyControl(words []string) counterControl {
	if len(words) == 0 {
		return counterApplication
	}
	switch words[0] {
	case "begin":
		return counterBegin
	case "start":
		if len(words) > 1 && words[1] == "transaction" {
			return counterBegin
		}
	case "commit", "end", "abort":
		return counterEnd
	case "rollback":
		// WORK/TRANSACTION may precede TO; the ROLLBACK command tag alone
		// cannot distinguish a savepoint rollback from the outer boundary.
		rest := words[1:]
		if len(rest) > 0 && (rest[0] == "work" || rest[0] == "transaction") {
			rest = rest[1:]
		}
		if len(rest) > 0 && rest[0] == "to" {
			return counterSavepoint
		}
		return counterEnd
	case "savepoint", "release":
		return counterSavepoint
	}
	return counterApplication
}

func counterClassifyLock(words []string) bool {
	sql := " " + strings.Join(words, " ") + " "
	for _, clause := range []string{" for update ", " for no key update ", " for share ", " for key share "} {
		if strings.Contains(sql, clause) {
			return true
		}
	}
	return false
}

func (a counterAccountingTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	words := strings.Fields(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(data.SQL), ";")))
	control := counterClassifyControl(words)
	var lockStart time.Time
	if n := counterAccountIn(ctx); n != nil {
		if counterClassifyLock(words) {
			lockStart = n.now()
		}
		id := n.query.Add(1)
		n.current.Store(id)
		ctx = context.WithValue(ctx, counterWaitQueryKey{}, id)
		switch control {
		case counterBegin:
			n.started, n.active = n.now(), true
			n.controls++
		case counterEnd, counterSavepoint:
			n.controls++
		default:
			if strings.Contains(data.SQL, "range_agg") || strings.Contains(data.SQL, "$4::int8multirange - $5::int8multirange") {
				panic("accounting correctness-only counter maintenance")
			}
			n.statements++
		}
	}
	// Count before delegation: COMMIT's hook executes nested maintenance SQL.
	ctx = context.WithValue(ctx, counterAccountQueryKey{}, control)
	ctx = context.WithValue(ctx, counterLockStartKey{}, lockStart)
	if a.inner != nil {
		ctx = a.inner.TraceQueryStart(ctx, conn, data)
	}
	return ctx
}

func (a counterAccountingTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if a.inner != nil {
		a.inner.TraceQueryEnd(ctx, conn, data)
	}
	if n := counterAccountIn(ctx); n != nil {
		if start, _ := ctx.Value(counterLockStartKey{}).(time.Time); !start.IsZero() {
			// Includes execution as well as blocking, so this is an upper bound.
			duration := n.now().Sub(start)
			n.lockTime += duration
			if _, memberLock := ctx.Value(counterMoveLockKey{}).(int); memberLock {
				n.moveMemberLockTime += duration
			}
		}
		id, _ := ctx.Value(counterWaitQueryKey{}).(uint64)
		n.current.CompareAndSwap(id, 0)
		if table, ok := ctx.Value(counterMoveLockKey{}).(int); ok && data.Err == nil {
			n.moveLocks[table] += data.CommandTag.RowsAffected()
		}
		if data.Err == nil && (data.CommandTag.Insert() || data.CommandTag.Update() || data.CommandTag.Delete()) {
			n.rows += data.CommandTag.RowsAffected()
		}
		var p *pgconn.PgError
		n.deadlocked = n.deadlocked || errors.As(data.Err, &p) && p.Code == "40P01"
		control, _ := ctx.Value(counterAccountQueryKey{}).(counterControl)
		if n.active && (control == counterEnd || control == counterBegin && data.Err != nil) {
			n.transactions = append(n.transactions, n.now().Sub(n.started))
			n.active = false
		}
	}
}

// A command tag omits data-modifying CTEs. These statements return the sum of
// each write's RETURNING rows instead, counted once and in the same round trip.
func counterSQLCountExec(ctx context.Context, conn *pgx.Conn, sql string, args ...any) error {
	var rows int64
	err := conn.QueryRow(ctx, sql, args...).Scan(&rows)
	if n := counterAccountIn(ctx); n != nil && err == nil {
		n.rows += rows
	}
	return err
}

type counterAccountingHook struct{ commit func(context.Context) }

func (h counterAccountingHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == "commit" {
		h.commit(ctx)
	}
	return ctx
}

func (counterAccountingHook) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestUnreadCountersAccountingControls(t *testing.T) {
	for _, tc := range []struct {
		sql     string
		control counterControl
	}{
		{"begin", counterBegin},
		{"begin isolation level serializable read only deferrable", counterBegin},
		{"begin isolation level repeatable read read write not deferrable", counterBegin},
		{"begin isolation level read committed", counterBegin},
		{"begin isolation level read uncommitted", counterBegin},
		{" \tBeGiN\n TRANSACTION ;\r\n", counterBegin},
		{"BEGIN WORK", counterBegin},
		{" START\t TRANSACTION\nREAD ONLY; ", counterBegin},
		{"commit", counterEnd},
		{" \nCOMMIT\t; ", counterEnd},
		{"COMMIT WORK", counterEnd},
		{"COMMIT TRANSACTION", counterEnd},
		{" END\tWORK; ", counterEnd},
		{"END TRANSACTION", counterEnd},
		{"END", counterEnd},
		{"rollback", counterEnd},
		{" \nROLLBACK\t; ", counterEnd},
		{"ROLLBACK WORK", counterEnd},
		{"ROLLBACK TRANSACTION", counterEnd},
		{" ABORT\tWORK; ", counterEnd},
		{"ABORT TRANSACTION", counterEnd},
		{"ABORT", counterEnd},
		{"savepoint sp_1", counterSavepoint},
		{" SAVEPOINT\tsp_1; ", counterSavepoint},
		{"release savepoint sp_1", counterSavepoint},
		{" RELEASE\nSAVEPOINT\tsp_1; ", counterSavepoint},
		{"RELEASE sp_1", counterSavepoint},
		{"rollback to savepoint sp_1", counterSavepoint},
		{" ROLLBACK\nTO\tSAVEPOINT sp_1; ", counterSavepoint},
		{"ROLLBACK TO sp_1", counterSavepoint},
		{"ROLLBACK WORK TO SAVEPOINT sp_1", counterSavepoint},
		{"ROLLBACK TRANSACTION TO sp_1", counterSavepoint},
		{"SELECT 1", counterApplication},
		{"rollbackish", counterApplication},
		{"beginning", counterApplication},
		{"committed", counterApplication},
		{"savepoints", counterApplication},
		{"release_notes", counterApplication},
		{"", counterApplication},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			for _, failed := range []bool{false, true} {
				clock := time.Unix(0, 0)
				ctx, n := counterAccountContext(t.Context(), func() time.Time { return clock })
				// Non-BEGIN cases start inside a transaction so a premature
				// savepoint boundary cannot hide behind an inactive account.
				n.started, n.active = clock, tc.control != counterBegin
				tracer := counterAccountingTracer{}
				q := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: tc.sql})
				clock = clock.Add(time.Millisecond)
				var err error
				if failed {
					err = errors.New("query failed")
				}
				// Savepoint rollbacks also return ROLLBACK. Classification
				// must survive that tag and an error without ending the sample.
				tracer.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("ROLLBACK"), Err: err})
				var statements, controls int64
				if tc.control == counterApplication {
					statements = 1
				} else {
					controls = 1
				}
				ends := tc.control == counterEnd || tc.control == counterBegin && failed
				samples := 0
				if ends {
					samples = 1
				}
				if n.statements != statements || n.controls != controls || n.rows != 0 || n.active == ends || len(n.transactions) != samples {
					t.Fatalf("failed=%t: statements=%d controls=%d rows=%d active=%t samples=%v; want statements=%d controls=%d active=%t samples=%d", failed, n.statements, n.controls, n.rows, n.active, n.transactions, statements, controls, !ends, samples)
				}
				if !ends {
					q = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "COMMIT"})
					clock = clock.Add(time.Millisecond)
					tracer.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("COMMIT")})
				}
				want := time.Duration(2-samples) * time.Millisecond
				if len(n.transactions) != 1 || n.transactions[0] != want || n.active {
					t.Fatalf("outer boundary: active=%t samples=%v; want [%v]", n.active, n.transactions, want)
				}
			}
		})
	}
}

func TestUnreadCountersAccounting(t *testing.T) {
	clock := time.Unix(0, 0)
	ctx, n := counterAccountContext(t.Context(), func() time.Time { return clock })
	tracer := counterAccountingTracer{}
	query := func(sql, tag string, err error) {
		q := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
		clock = clock.Add(time.Millisecond)
		tracer.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag(tag), Err: err})
	}
	tracer.inner = counterAccountingHook{commit: func(ctx context.Context) {
		q := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "nested maintenance"})
		clock = clock.Add(10 * time.Millisecond)
		tracer.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("UPDATE 1")})
	}}
	for i := 0; i < 100; i++ {
		query("begin", "BEGIN", nil)
		query("select", "SELECT 9", nil)
		query("insert", "INSERT 0 2", nil)
		query("delete returning", "DELETE 3", nil)
		query("update", "UPDATE 1", nil)
		clock = clock.Add(time.Duration(i) * time.Millisecond)
		query("commit", "COMMIT", nil)
		n.finish(nil, false)
	}
	deadlock := &pgconn.PgError{Code: "40P01"}
	query("begin", "BEGIN", nil)
	query("savepoint sp_1", "SAVEPOINT", nil)
	query("rollback to savepoint sp_1", "ROLLBACK", nil)
	query("release savepoint sp_1", "RELEASE", nil)
	query("failed update", "UPDATE 7", deadlock)
	query("rollback", "ROLLBACK", nil)
	n.finish(errors.New("wrapped operation failure"), false)
	// A failure to begin still has latency; a pre-BEGIN failure has none.
	query("begin", "", errors.New("connection closed"))
	n.finish(errors.New("begin failed"), true)
	n.finish(errors.New("pool acquisition failed"), false)
	q := tracer.TraceQueryStart(t.Context(), nil, pgx.TraceQueryStartData{SQL: "oracle"})
	tracer.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("UPDATE 999")})
	if n.statements != 501 || n.controls != 206 || n.rows != 700 || n.failures != 3 || n.deadlocks != 1 || n.retries != 1 {
		t.Fatalf("attempt accounting: statements=%d controls=%d rows=%d failures=%d deadlocks=%d retries=%d", n.statements, n.controls, n.rows, n.failures, n.deadlocks, n.retries)
	}
	if len(n.transactions) != 102 || n.transactions[100] != 6*time.Millisecond || n.transactions[101] != time.Millisecond || n.latency() != [3]time.Duration{65 * time.Millisecond, 110 * time.Millisecond, 114 * time.Millisecond} {
		t.Fatalf("transaction percentiles: samples=%d percentiles=%v", len(n.transactions), n.latency())
	}
}

func TestUnreadCountersAccountingShapeGuard(t *testing.T) {
	ctx, _ := counterAccountContext(t.Context(), func() time.Time { return time.Unix(0, 0) })
	defer func() {
		if recover() != "accounting correctness-only counter maintenance" {
			t.Fatal("accounting accepted correctness-only maintenance")
		}
	}()
	(counterAccountingTracer{}).TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT range_agg(int8range(lo,hi))"})
}

func TestUnreadCountersAccountingSQL(t *testing.T) {
	for design, counters := range []bool{false, true} {
		t.Run(fmt.Sprintf("counters=%t", counters), func(t *testing.T) {
			h := newCounterMatrix(t)
			owner, writer := h.members[0], h.join(t, "writer")
			seq := h.post(t, "seed", writer, nil, nil)
			destination := conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, "Destination").ID
			counterSQLTables(t, h.pool)
			_, key := counterSQLContext(t.Context(), owner, h.channel)
			counterSQLMaterialize(t, h.pool, key)
			config := h.pool.Config()
			tracer := counterAccountingTracer{}
			if counters {
				tracer.inner = counterIntervalAdapter{t: t}
			}
			config.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, func(tx platform.Tx) conversation.PostReadWriter { return counterSQLPostReads{tx} }, nil)
			reading := unreadpg.NewReading(pool, counterFeedWriter(), counterTopicWriter(nil), nil)
			var notice int64
			branching := conversationpg.NewBrancher(pool, postingSequence, postingEvents, func(tx platform.Tx) conversation.ReadRangeWriter { return counterBranchReads{tx, &notice} }, nil)
			hasher, err := identity.NewHasher()
			if err != nil {
				t.Fatal(err)
			}
			signup := orgpg.NewSignUp(pool, hasher, true, func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }, func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) })
			scope := unread.Scope{OrganizationID: owner.Organization.ID, ChannelID: h.channel, MemberID: owner.Member.ID}
			for _, op := range []struct {
				name             string
				run              func(context.Context) error
				statements, rows [2]int64
			}{
				{"feed", func(ctx context.Context) error { return reading.Feed(ctx, scope, owner.Member.JoinedEventSeq, seq) }, [2]int64{10, 17}, [2]int64{3, 5}},
				{"repeat", func(ctx context.Context) error { return reading.Feed(ctx, scope, owner.Member.JoinedEventSeq, seq) }, [2]int64{9, 16}, [2]int64{4, 4}},
				{"topic-floor", func(ctx context.Context) error {
					return reading.Topic(ctx, unread.TopicScope{Scope: scope, TopicID: h.base}, owner.Member.JoinedEventSeq, seq)
				}, [2]int64{12, 19}, [2]int64{3, 4}},
				{"post", func(ctx context.Context) error {
					_, err := posting.PostToTopic(ctx, owner, h.channel, nil, "own")
					return err
				}, [2]int64{12, 20}, [2]int64{7, 11}},
				{"move", func(ctx context.Context) error {
					_, err := branching.Branch(ctx, owner, h.channel, conversation.Branch{From: h.base, To: &destination, Messages: []kernel.ID{h.posted[seq]}}, func(conversation.Topic) string { return "notice" })
					return err
				}, [2]int64{16, 30}, [2]int64{10, 18}},
				{"join", func(ctx context.Context) error {
					_, err := signup.SignUp(ctx, "Late", "late", "late@example.org", "long enough password")
					return err
				}, [2]int64{6, 7}, [2]int64{4, 4}},
			} {
				ctx, w := counterSQLContext(t.Context(), owner, h.channel)
				counterSQLUseIntervals(w)
				w.join = op.name == "join"
				if op.name == "move" {
					w.moveFrom, w.moves = h.base, []int64{seq}
				}
				ctx, n := counterAccountContext(ctx, func() time.Time { return time.Unix(0, 0) })
				err := op.run(ctx)
				n.finish(err, false)
				if err != nil || w.err != nil {
					t.Fatalf("%s: source=%v counters=%v", op.name, err, w.err)
				}
				// Fixtures and oracle queries use the untraced pool and no account.
				truth := counterLoad(t, h.pool, owner.Organization.ID)
				if counters {
					tx, err := h.pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					counterSQLModel(t, tx, counterView{owner, h.channel, destination}, truth)
					if err := tx.Rollback(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				if n.statements != op.statements[design] || n.rows != op.rows[design] || n.controls != 2 || n.failures != 0 || len(n.transactions) != 1 || n.transactions[0] != 0 {
					t.Errorf("%s: statements=%d rows=%d controls=%d failures=%d samples=%v", op.name, n.statements, n.rows, n.controls, n.failures, n.transactions)
				}
			}
		})
	}
}
