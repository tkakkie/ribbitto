package postgres_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestFirstMessageAfterScope(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "bounds", "general")
	other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
	sibling := conversationtest.Channel(t, pool, f.OrganizationID, "sibling", false)
	// A channel ID is globally unique. Relax only these FKs in the disposable
	// database to isolate the organisation predicate while keeping channel equal.
	_, err := pool.Exec(t.Context(), "ALTER TABLE message DROP CONSTRAINT message_organization_id_channel_id_fkey, DROP CONSTRAINT message_topic_fkey")
	requireNoError(t, err)
	writer := postgres.NewWriterForTest(pool)
	for _, m := range []struct {
		org, channel, topic, member kernel.ID
		seq                         int64
	}{
		{f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, 3},
		{f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, 8},
		{other.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, other.MemberID, 4},
		{f.OrganizationID, sibling.ID, f.Channel.DefaultTopicID, f.MemberID, 5},
	} {
		_, err := writer.InsertMessage(t.Context(), m.org, m.channel, m.topic, m.member, "bounds", m.seq, nil, conversation.TypedBodyProof{}, nil)
		requireNoError(t, err)
	}
	traced, trace := hasMessagesPool(t, pool)
	requireNoError(t, platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
		bounds := postgres.MessageSequencesIn(tx)
		for _, tc := range []struct{ cursor, want int64 }{{3, 8}, {8, 0}} {
			got, err := bounds.FirstMessageAfter(t.Context(), f.OrganizationID, f.Channel.ID, tc.cursor)
			requireNoError(t, err)
			if got != tc.want {
				t.Fatalf("after %d=%d, want %d", tc.cursor, got, tc.want)
			}
		}
		for _, tc := range []struct {
			name     string
			los, his []int64
			want     bool
		}{
			{"T-a1 gaps", []int64{2, 9}, []int64{3, 10}, false},
			{"T-a2 unsorted", []int64{9, 3}, []int64{10, 4}, true},
			{"T-a3 empty", nil, nil, false},
			{"T-a4 lowest", []int64{3}, []int64{4}, true},
			{"T-a4 highest minus one", []int64{8}, []int64{9}, true},
			{"T-a4 highest excluded", []int64{4}, []int64{8}, false},
			{"T-a5 scoped decoys", []int64{4}, []int64{6}, false},
			{"above last", []int64{9}, []int64{10}, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := trace.Counts().Queries
				got, err := bounds.HasMessages(t.Context(), f.OrganizationID, f.Channel.ID, tc.los, tc.his)
				requireNoError(t, err)
				if got != tc.want {
					t.Fatalf("messages in %v/%v=%t, want %t", tc.los, tc.his, got, tc.want)
				}
				wantStatements := int64(1)
				if len(tc.los) == 0 {
					wantStatements = 0
				}
				if got := trace.Counts().Queries - before; got != wantStatements {
					t.Fatalf("statements=%d, want %d", got, wantStatements)
				}
			})
		}
		got, err := bounds.HasMessages(t.Context(), f.OrganizationID, f.Channel.ID, []int64{4, 8}, []int64{6, 9})
		requireNoError(t, err)
		if !got {
			t.Fatal("second interval's message was missed")
		}
		if _, err := bounds.HasMessages(t.Context(), f.OrganizationID, f.Channel.ID, []int64{3}, nil); err == nil {
			t.Fatal("unequal bounds accepted")
		}
		return nil
	}))
}

type hasMessagesTrace struct {
	*platform.QueryCounter
	sql  string
	args []any
}

func (c *hasMessagesTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.QueryCounter.TraceQueryStart(ctx, conn, d)
	if strings.HasPrefix(d.SQL, "-- name: ChannelHasMessages :one") {
		c.sql, c.args = d.SQL, append([]any(nil), d.Args...)
	}
	return ctx
}

func hasMessagesPool(t *testing.T, pool *pgxpool.Pool) (*pgxpool.Pool, *hasMessagesTrace) {
	t.Helper()
	trace := &hasMessagesTrace{QueryCounter: platform.NewQueryCounter()}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	requireNoError(t, err)
	t.Cleanup(traced.Close)
	return traced, trace
}

func TestHasMessagesPlan(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "plan", "general")
	sibling := conversationtest.Channel(t, pool, f.OrganizationID, "sibling", false)
	for i, channel := range []conversationtest.ChannelFixture{f.Channel, sibling} {
		_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,$3,$4,'plan',s FROM generate_series($5::bigint,$5::bigint+19999) s`,
			f.OrganizationID, channel.ID, channel.DefaultTopicID, f.MemberID, int64(2+i*20000))
		requireNoError(t, err)
	}
	_, err := pool.Exec(t.Context(), "ANALYZE message")
	requireNoError(t, err)
	traced, trace := hasMessagesPool(t, pool)
	requireNoError(t, platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
		got, err := postgres.MessageSequencesIn(tx).HasMessages(t.Context(), f.OrganizationID, f.Channel.ID, []int64{2}, []int64{20002})
		if err == nil && !got {
			t.Fatal("populated channel has no messages")
		}
		return err
	}))
	if trace.sql == "" || trace.Counts().Queries != 1 {
		t.Fatalf("expected one captured probe, got %+v", trace.Counts())
	}
	// Session settings must stay on this disposable connection, never a pooled one.
	conn, err := pgx.ConnectConfig(t.Context(), pool.Config().ConnConfig)
	requireNoError(t, err)
	defer func() { requireNoError(t, conn.Close(t.Context())) }()
	_, err = conn.Exec(t.Context(), "SET enable_seqscan = off")
	requireNoError(t, err)
	_, err = conn.Prepare(t.Context(), "has_messages", trace.sql)
	requireNoError(t, err)
	params := make([]string, len(trace.args))
	for i := range params {
		params[i] = fmt.Sprintf("$%d", i+1)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			_, err := conn.Exec(t.Context(), "SET plan_cache_mode = "+mode)
			requireNoError(t, err)
			// EXECUTE requires literals; pgx's simple protocol safely encodes the captured arguments.
			args := append([]any{pgx.QueryExecModeSimpleProtocol}, trace.args...)
			rows, err := conn.Query(t.Context(), "EXPLAIN EXECUTE has_messages("+strings.Join(params, ",")+")", args...)
			requireNoError(t, err)
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			requireNoError(t, err)
			plan := strings.Join(lines, "\n")
			t.Log(plan)
			indexScan, bounded := false, false
			for _, line := range lines {
				if strings.Contains(line, "Index Scan using message_channel_event_seq_idx") || strings.Contains(line, "Index Only Scan using message_channel_event_seq_idx") {
					indexScan = true
				}
				if strings.Contains(line, "Index Cond:") && strings.Contains(line, "event_seq >= ") && strings.Contains(line, "event_seq < ") {
					bounded = true
				}
			}
			if !indexScan || !bounded || strings.Contains(plan, "Seq Scan on message") {
				t.Fatalf("probe must use the channel index with both sequence bounds:\n%s", plan)
			}
		})
	}
}

func TestHasMessagesOracle(t *testing.T) {
	t.Parallel()
	const seed = 1549
	t.Logf("seed=%d", seed)
	rng := rand.New(rand.NewPCG(seed, seed))
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "oracle", "general")
	other := conversationtest.OrganizationWithOwner(t, pool, "decoy", "general")
	sibling := conversationtest.Channel(t, pool, f.OrganizationID, "sibling", false)
	// Keep channel IDs equal for the organisation decoys, as in the scope test.
	_, err := pool.Exec(t.Context(), "ALTER TABLE message DROP CONSTRAINT message_organization_id_channel_id_fkey, DROP CONSTRAINT message_topic_fkey")
	requireNoError(t, err)
	requireNoError(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		bounds := postgres.MessageSequencesIn(tx)
		for n := range 500 {
			_, err := pool.Exec(t.Context(), "DELETE FROM message")
			requireNoError(t, err)
			var messages []int64
			for seq := int64(1); seq <= 60; seq++ {
				orgID, channelID, memberID := f.OrganizationID, f.Channel.ID, f.MemberID
				switch rng.IntN(4) {
				case 0:
					messages = append(messages, seq)
				case 1:
					orgID, memberID = other.OrganizationID, other.MemberID
				case 2:
					channelID = sibling.ID
				case 3:
					continue
				}
				_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 VALUES ($1,$2,$3,$4,'oracle',$5)`, orgID, channelID, f.Channel.DefaultTopicID, memberID, seq)
				requireNoError(t, err)
			}
			var intervals []conversation.SequenceRange
			points := rng.Perm(62)[:2*rng.IntN(7)]
			sort.Ints(points)
			for i := 0; i < len(points); i += 2 {
				intervals = append(intervals, conversation.SequenceRange{Lo: int64(points[i]), Hi: int64(points[i+1])})
			}
			if rng.IntN(2) == 0 {
				rng.Shuffle(len(intervals), func(i, j int) { intervals[i], intervals[j] = intervals[j], intervals[i] })
			}
			var los, his []int64
			want := false
			for _, interval := range intervals {
				los, his = append(los, interval.Lo), append(his, interval.Hi)
				for _, seq := range messages {
					want = want || (interval.Lo <= seq && seq < interval.Hi)
				}
			}
			got, err := bounds.HasMessages(t.Context(), f.OrganizationID, f.Channel.ID, los, his)
			requireNoError(t, err)
			if got != want {
				t.Fatalf("case=%d messages=%v intervals=%v: got %t, want %t", n, messages, intervals, got, want)
			}
		}
		return nil
	}))
}
