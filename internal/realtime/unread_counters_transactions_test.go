package realtime_test

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// A single test row persists the existing arithmetic, not a proposed schema.
// Encoding preserves all raw totals and complete materialised bundles.
type counterStored struct {
	Totals map[kernel.ID]map[kernel.ID]int64
	Reads  map[kernel.ID]map[kernel.ID]int64
}

func counterEncode(t *testing.T, p counterPrototype) []byte {
	t.Helper()
	s := counterStored{p.totals, map[kernel.ID]map[kernel.ID]int64{}}
	for k, b := range p.bundles {
		s.Reads[k.member] = b.counts
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func counterDecode(t *testing.T, data []byte, channel kernel.ID) counterPrototype {
	t.Helper()
	var s counterStored
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		t.Fatal(err)
	}
	p := counterPrototype{totals: s.Totals, bundles: map[counterKey]*counterBundle{}, suspended: map[kernel.ID]bool{}}
	for member, counts := range s.Reads {
		p.bundles[counterKey{member, channel}] = &counterBundle{true, counts}
	}
	return p
}

type counterOperation struct {
	pid                 uint32
	ctx                 context.Context
	err                 error
	kind                string
	begun, enter, ready chan bool
	finish              chan bool
	done                chan error
	member              org.Membership
	cursor, seq         int64
	joined              kernel.ID
	truth               counterTruth
	prototype           counterPrototype
}

type counterOperationKey struct{}
type counterBeginKey struct{}

// The public pgx tracer exposes the production transaction's connection without
// importing private stores or unwrapping platform.Tx. Only BEGIN/COMMIT hooks
// run SQL; their nested ordinary queries cannot re-enter these hooks.
type counterTransactionAdapter struct {
	t           *testing.T
	h           *counterMatrix
	destination kernel.ID
	moved       int64
	moves       []int64
	sql         bool
	intervals   bool
	newTopic    bool
}

func (a *counterTransactionAdapter) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if a.sql {
		counterSQLIntervalGuard(a.t, ctx, data.SQL)
		if data.SQL != "commit" {
			ctx = (counterSQLAdapter{}).TraceQueryStart(ctx, conn, data)
		}
	}
	ctx = context.WithValue(ctx, counterBeginKey{}, data.SQL == "begin")
	op, _ := ctx.Value(counterOperationKey{}).(*counterOperation)
	if op == nil || data.SQL != "commit" {
		return ctx
	}
	if a.sql {
		w := ctx.Value(counterSQLKey{}).(*counterSQLWrite)
		w.seq = op.seq
		ctx = (counterSQLAdapter{}).TraceQueryStart(ctx, conn, data)
		op.err = w.err
	} else {
		h, p := a.h, &op.prototype
		switch op.kind {
		case "read":
			p.advance(op.truth, counterKey{op.member.Member.ID, h.channel}, h.base, op.cursor)
		case "post":
			p.post(op.truth, counterMessage{channel: h.channel, topic: h.base, seq: op.seq}, op.member.Member.ID, kernel.ID{}, op.cursor)
		case "move":
			p.move(op.truth, h.channel, h.base, a.destination, a.moved)
			p.post(op.truth, counterMessage{channel: h.channel, topic: h.base, seq: op.seq}, op.member.Member.ID, kernel.ID{}, 0)
		case "join":
			truth := op.truth
			truth.members = append(truth.members, counterMember{op.joined, op.seq})
			p.materialize(truth, counterKey{op.joined, h.channel})
		}
		if _, err := conn.Exec(ctx, `UPDATE counter_transaction_state SET data=$1`, counterEncode(a.t, *p)); err != nil {
			op.err = err
			return ctx
		}
	}
	op.ready <- true
	select {
	case commit := <-op.finish:
		if !commit {
			// A server-confirmed abort at COMMIT, after both source and counter
			// writes, exercises PostgreSQL rollback rather than memory undo.
			_, err := conn.Exec(ctx, `SELECT 1/0`)
			if err == nil {
				op.err = fmt.Errorf("rollback injection did not abort transaction")
			}
		}
	case <-op.ctx.Done():
		_, _ = conn.Exec(ctx, `SELECT 1/0`)
	}
	return ctx
}

func (a *counterTransactionAdapter) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, end pgx.TraceQueryEndData) {
	if a.sql && ctx.Value(counterBeginKey{}) != true {
		(counterSQLAdapter{}).TraceQueryEnd(ctx, conn, end)
	}
	op, _ := ctx.Value(counterOperationKey{}).(*counterOperation)
	if op == nil || ctx.Value(counterBeginKey{}) != true {
		return
	}
	op.pid = conn.PgConn().PID()
	op.begun <- true
	select {
	case <-op.enter:
	case <-ctx.Done():
		return
	}
	if a.sql {
		(counterSQLAdapter{}).TraceQueryEnd(context.WithValue(ctx, counterSQLBegin{}, true), conn, end)
		return
	}
	if _, err := conn.Exec(ctx, `SELECT id FROM organization WHERE id=$1 FOR NO KEY UPDATE`, a.h.members[0].Organization.ID); err != nil {
		op.err = err
		return
	}
	op.truth = counterLoad(a.t, conn, a.h.members[0].Organization.ID)
	var data []byte
	if err := conn.QueryRow(ctx, `SELECT data FROM counter_transaction_state`).Scan(&data); err != nil {
		op.err = err
		return
	}
	op.prototype = counterDecode(a.t, data, a.h.channel)
}

type counterTransactionEvents struct {
	appender conversation.EventAppender
	op       *counterOperation
}

func (w counterTransactionEvents) Append(ctx context.Context, organization kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, data []byte) error {
	if err := w.appender.Append(ctx, organization, seq, kind, audience, data); err != nil {
		return err
	}
	w.op.seq = seq
	if kind == org.KindJoined {
		joined, err := org.DecodeJoined(data)
		if err != nil {
			return err
		}
		w.op.joined = joined.MemberID
	}
	return nil
}

func (a *counterTransactionAdapter) start(ctx context.Context, pool *pgxpool.Pool, kind string, m org.Membership, cursor int64) *counterOperation {
	if a.sql {
		var w *counterSQLWrite
		ctx, w = counterSQLContext(ctx, m, a.h.channel)
		if a.intervals {
			counterSQLUseIntervals(w)
		}
		w.join = kind == "join"
		if kind == "move" {
			w.moveFrom, w.moves = a.h.base, a.moves
		}
	}
	op := &counterOperation{ctx: ctx, kind: kind, member: m, cursor: cursor, begun: make(chan bool, 1), enter: make(chan bool, 1), ready: make(chan bool, 1), finish: make(chan bool, 1), done: make(chan error, 1)}
	ctx = context.WithValue(ctx, counterOperationKey{}, op)
	go func() {
		h := a.h
		events := func(tx platform.Tx) conversation.EventAppender {
			return counterTransactionEvents{realtimepg.AppenderIn(tx), op}
		}
		var err error
		switch kind {
		case "post":
			p := conversationpg.NewPagePosting(pool, postingSequence, events, func(tx platform.Tx) conversation.PostReadWriter { return counterPostReads{tx} }, nil)
			_, err = p.PostFromPage(ctx, m, h.channel, nil, "competing post", cursor)
		case "move":
			var notice int64
			b := conversationpg.NewBrancher(pool, postingSequence, events, func(tx platform.Tx) conversation.ReadRangeWriter { return counterBranchReads{tx, &notice} }, nil)
			branch := conversation.Branch{From: h.base, To: &a.destination, Messages: []kernel.ID{h.posted[a.moved]}}
			if a.sql {
				branch.Messages = nil
				for _, seq := range a.moves {
					branch.Messages = append(branch.Messages, h.posted[seq])
				}
				if a.newTopic {
					branch.To, branch.NewName = nil, "ZDestination"
				}
			}
			_, err = b.Branch(ctx, m, h.channel, branch, func(conversation.Topic) string { return "branch notice" })
		case "read":
			r := unreadpg.NewReading(pool, counterFeedWriter(), counterTopicWriter(nil), nil)
			err = r.Topic(ctx, unread.TopicScope{Scope: unread.Scope{OrganizationID: m.Organization.ID, MemberID: m.Member.ID, ChannelID: h.channel}, TopicID: h.base}, m.Member.JoinedEventSeq, cursor)
		case "join":
			var hasher *identity.Hasher
			hasher, err = identity.NewHasher()
			if err == nil {
				s := orgpg.NewSignUp(pool, hasher, true, func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }, func(tx platform.Tx) org.EventAppender { return counterTransactionEvents{realtimepg.AppenderIn(tx), op} })
				_, err = s.SignUp(ctx, "Late", "late", "late@example.org", "long enough password")
			}
		}
		if op.err != nil {
			err = op.err
		}
		op.done <- err
	}()
	return op
}

func counterGate(t *testing.T, ctx context.Context, gate <-chan bool) {
	t.Helper()
	select {
	case <-gate:
	case <-ctx.Done():
		t.Fatal("transaction gate: ", ctx.Err())
	}
}

func counterOutcome(t *testing.T, ctx context.Context, h *counterMatrix, op *counterOperation, commit bool, sql bool) {
	t.Helper()
	state := func() string {
		var result string
		counters := `(SELECT encode(data,'hex') FROM counter_transaction_state)`
		if sql {
			counters = `jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(c) ORDER BY channel_id,scope_id) FROM counter_total c),
 (SELECT jsonb_agg(to_jsonb(b) ORDER BY channel_id,member_id) FROM counter_bundle b),
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY channel_id,member_id,topic_id) FROM counter_topic_read r))`
		}
		err := h.pool.QueryRow(ctx, `SELECT jsonb_build_array(
(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM message m),
(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM member m),
(SELECT jsonb_agg(to_jsonb(r) ORDER BY member_id,lo) FROM read_range r),
(SELECT jsonb_agg(to_jsonb(c) ORDER BY channel_id,member_id) FROM channel_read c),
(SELECT jsonb_agg(to_jsonb(f) ORDER BY member_id,topic_id) FROM topic_read_floor f),
(SELECT event_seq FROM organization WHERE id=$1),
(SELECT jsonb_agg(to_jsonb(e) ORDER BY organization_id,seq) FROM event_log e),
(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM account a),
(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM topic t),`+counters+`)::text`, h.members[0].Organization.ID).Scan(&result)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := state()
	op.finish <- commit
	select {
	case err := <-op.done:
		if !commit && !errors.Is(err, pgx.ErrTxCommitRollback) {
			t.Fatalf("rollback was not confirmed by PostgreSQL: %v", err)
		}
		if (err == nil) != commit {
			t.Fatalf("transaction outcome: commit=%t error=%v", commit, err)
		}
	case <-ctx.Done():
		t.Fatal("transaction outcome: ", ctx.Err())
	}
	if !commit && state() != before {
		t.Fatal("rollback changed source or persisted counters")
	}
}

func TestUnreadCountersTransactions(t *testing.T) {
	counterTransactions(t, false, false)
}

func counterTransactions(t *testing.T, sql, intervals bool) {
	pairs := [][2]string{{"read", "move"}, {"post", "read"}, {"join", "post"}}
	orders := [][2]bool{{false, false}, {true, false}}
	if sql {
		pairs = append(pairs, [2]string{"join", "move"}, [2]string{"move", "post"})
		orders = append(orders, [2]bool{false, true}, [2]bool{true, true})
	}
	for _, pair := range pairs {
		for _, order := range orders {
			reverse, newTopic := order[0], order[1]
			for _, abort := range []int{-1, 0, 1} {
				t.Run(fmt.Sprintf("%s-%s/new=%t/reverse=%t/abort=%d", pair[0], pair[1], newTopic, reverse, abort), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel() // Deadline bounds broken gates; ordering uses no clock.
					h := newCounterMatrix(t)
					owner, writer := h.members[0], h.join(t, "writer")
					var cursor int64
					var sequences []int64
					var reader org.Membership
					seeds := 100
					if sql {
						seeds = 103
					}
					for i := 0; i < seeds; i++ {
						cursor = h.post(t, fmt.Sprintf("seed-%d", i), writer, nil, nil)
						sequences = append(sequences, cursor)
						if sql && i == 0 {
							reader = h.join(t, "reader")
						}
					}
					h.expect(t, owner, int64(seeds))
					h.prototype.materialize(h.truth, counterKey{writer.Member.ID, h.channel})
					a := &counterTransactionAdapter{t: t, h: h, moved: cursor, sql: sql, intervals: intervals, newTopic: newTopic}
					if sql {
						h.read(t, "partial-source-read", owner, &h.base, sequences[1])
						a.moves = []int64{sequences[0], sequences[1], cursor}
						for i := 0; i < 49; i++ {
							conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, fmt.Sprintf("Listed%02d", i))
						}
					}
					if !a.newTopic {
						a.destination = conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, "ZDestination").ID
					}
					if sql {
						if !a.newTopic {
							h.read(t, "destination-floor", owner, &a.destination, cursor)
						}
						counterSQLTables(t, h.pool)
						for _, m := range []org.Membership{owner, reader} {
							_, w := counterSQLContext(ctx, m, h.channel)
							counterSQLMaterialize(t, h.pool, w)
						}
					} else {
						if _, err := h.pool.Exec(ctx, `CREATE TABLE counter_transaction_state (data bytea NOT NULL);`); err != nil {
							t.Fatal(err)
						}
						if _, err := h.pool.Exec(ctx, `INSERT INTO counter_transaction_state VALUES ($1)`, counterEncode(t, h.prototype)); err != nil {
							t.Fatal(err)
						}
					}
					config := h.pool.Config()
					config.ConnConfig.Tracer = a
					pool, err := pgxpool.NewWithConfig(ctx, config)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { cancel(); pool.Close() }()
					check := func(name string, outcome func()) {
						t.Helper()
						tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = tx.Rollback(ctx) }()
						pinned := map[kernel.ID]sidebarOracleModel{}
						for pass := 0; pass < 2; pass++ {
							if sql {
								counterSQLCheckpoint(t, tx, h, pinned, pass)
								if pass == 0 && outcome != nil {
									outcome()
								}
								continue
							}
							var data []byte
							if err := tx.QueryRow(ctx, `SELECT data FROM counter_transaction_state`).Scan(&data); err != nil {
								t.Fatal(err)
							}
							p, truth := counterDecode(t, data, h.channel), counterLoad(t, tx, owner.Organization.ID)
							if !p.verify(truth, h.channel) {
								t.Fatalf("%s: persisted raw counters differ from truth", name)
							}
							for _, member := range truth.members {
								if p.bundles[counterKey{member.ID, h.channel}] == nil {
									t.Fatal("materialised transaction bundle missing")
								}
								want, err := sidebarOracleIn(ctx, tx, owner.Organization.ID, member.ID, h.channel, &a.destination)
								if err != nil {
									t.Fatal(err)
								}
								if pass == 1 {
									if err := sidebarOracleCompare(want, pinned[member.ID]); err != nil {
										t.Fatal("snapshot changed across outcome: ", err)
									}
								}
								pinned[member.ID] = want
								if err := sidebarOracleCompare(p.model(t, tx, truth, owner.Organization.ID, member.ID, h.channel, &a.destination), want); err != nil {
									t.Fatalf("%s: transactional full model differs from oracle: %v", name, err)
								}
							}
							if pass == 0 && outcome != nil {
								outcome()
							}
						}
					}
					order := pair
					if reverse {
						order[0], order[1] = order[1], order[0]
					}
					actor := func(kind string) org.Membership {
						if kind == "read" || kind == "post" && pair[1] == "read" || sql && kind == "move" && reverse {
							return owner
						}
						return writer
					}
					first := a.start(ctx, pool, order[0], actor(order[0]), cursor)
					second := a.start(ctx, pool, order[1], actor(order[1]), cursor)
					// Both real transactions exist before either source write.
					// Observe the actual PostgreSQL lock wait before releasing commit.
					counterGate(t, ctx, first.begun)
					counterGate(t, ctx, second.begun)
					first.enter <- true
					counterGate(t, ctx, first.ready)
					second.enter <- true
					for {
						var blocked bool
						if err := h.pool.QueryRow(ctx, `SELECT $1 = ANY(pg_blocking_pids($2))`, first.pid, second.pid).Scan(&blocked); err != nil {
							t.Fatal(err)
						}
						if blocked {
							break
						}
					}
					check("pinned-first", func() { counterOutcome(t, ctx, h, first, abort != 0, sql) })
					check("first-outcome", nil)
					counterGate(t, ctx, second.ready)
					check("pinned-second", func() { counterOutcome(t, ctx, h, second, abort != 1, sql) })
					check("second-outcome", nil)
					if abort >= 0 {
						retry := a.start(ctx, pool, order[abort], actor(order[abort]), cursor)
						counterGate(t, ctx, retry.begun)
						retry.enter <- true
						counterGate(t, ctx, retry.ready)
						check("pinned-retry", func() { counterOutcome(t, ctx, h, retry, true, sql) })
						check("retry-outcome", nil)
					}
					if sql {
						var n int
						if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM counter_bundle WHERE member_id NOT IN ($1,$2)`, owner.Member.ID, reader.Member.ID).Scan(&n); err != nil {
							t.Fatal(err)
						}
						if n != 0 {
							t.Fatal("missing bundles or lazy join were eagerly materialised")
						}
						if pair[0] == "join" {
							var joined kernel.ID
							if err := h.pool.QueryRow(ctx, `SELECT id FROM member WHERE handle='late'`).Scan(&joined); err != nil {
								t.Fatal(err)
							}
							check("pinned-materialisation", func() {
								counterSQLMaterialize(t, h.pool, &counterSQLWrite{organization: owner.Organization.ID, channel: h.channel, member: joined})
							})
							check("join-prefix-materialisation", nil)
						}
					}
				})
			}
		}
	}
}
