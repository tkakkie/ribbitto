package realtime_test

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Both designs use the same fixture pool configuration. All application work
// shares 35 leases; the observer has one reserved lease within the 36 budget.
// Fixture/oracle connections are outside the workload, never used by a runner.
func counterWorkPools(t *testing.T, fixture *pgxpool.Pool, counters bool) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	config := fixture.Config()
	tracer := counterAccountingTracer{}
	if counters {
		tracer.inner = counterIntervalAdapter{t: t}
	}
	config.MaxConns, config.ConnConfig.Tracer = 35, tracer
	work, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(work.Close)
	config = config.Copy()
	config.MaxConns = 1
	observer, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	return work, observer
}

type counterRecountKey struct{}
type counterStandaloneKey struct{}
type counterRecountState struct {
	conn     *pgx.Conn
	fallback *counterAccount
}
type counterPageCounts struct {
	costPageCounter
	counters bool
}

func (c counterPageCounts) ReadCounts(ctx context.Context, m org.Membership, channel kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	if !c.counters {
		return c.costPageCounter.ReadCounts(ctx, m, channel, channels, topics, selected)
	}
	r := ctx.Value(counterRecountKey{}).(*counterRecountState)
	result := conversation.PageCounts{ChannelCounts: map[kernel.ID]int64{}, TopicCounts: map[kernel.ID]int64{}}
	if selected != nil {
		topics = append(append([]kernel.ID(nil), topics...), *selected)
	}
	rows, err := r.conn.Query(ctx, `WITH scopes AS (
 SELECT c AS channel,c AS id,false AS topic FROM unnest($3::uuid[]) c
 UNION SELECT $4,t,true FROM unnest($5::uuid[]) t
 ) SELECT s.id,s.topic,coalesce(b.valid,false),least(100,t.n-
 CASE WHEN s.topic THEN coalesce(r.n,0) ELSE b.n END)
 FROM scopes s LEFT JOIN counter_total t ON t.organization_id=$1 AND t.channel_id=s.channel AND t.scope_id=s.id
 LEFT JOIN counter_bundle b ON b.organization_id=$1 AND b.channel_id=s.channel AND b.member_id=$2
 LEFT JOIN counter_topic_read r ON r.organization_id=$1 AND r.channel_id=s.channel AND r.member_id=$2 AND r.topic_id=s.id`,
		m.Organization.ID, m.Member.ID, channels, channel, topics)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var missingChannels, missingTopics []kernel.ID
	for rows.Next() {
		var id kernel.ID
		var topic, valid bool
		var n *int64
		if err := rows.Scan(&id, &topic, &valid, &n); err != nil {
			return result, err
		}
		if !valid || n == nil {
			if topic {
				missingTopics = append(missingTopics, id)
			} else {
				missingChannels = append(missingChannels, id)
			}
		} else if topic {
			result.TopicCounts[id] = *n
		} else {
			result.ChannelCounts[id] = *n
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(missingChannels)+len(missingTopics) > 0 {
		// Four production capped statements, in this snapshot. Their separate
		// account has no transaction sample: the parent owns BEGIN/COMMIT.
		fallbackCtx := context.WithValue(ctx, counterAccountKey{}, r.fallback)
		counts, err := c.costPageCounter.ReadCounts(fallbackCtx, m, channel, missingChannels, missingTopics, nil)
		r.fallback.finish(err, false)
		if err != nil {
			return result, err
		}
		maps.Copy(result.ChannelCounts, counts.ChannelCounts)
		maps.Copy(result.TopicCounts, counts.TopicCounts)
	}
	return result, nil
}

func counterSidebar(ctx context.Context, pool *pgxpool.Pool, counters bool, v counterView, now func() time.Time) (conversation.SidebarSnapshot, *counterAccount, *counterAccount, error) {
	ctx = context.WithValue(ctx, counterStandaloneKey{}, true)
	ctx, account := counterAccountContext(ctx, now)
	_, fallback := counterAccountContext(context.Background(), now)
	ctx = context.WithValue(ctx, counterRecountKey{}, &counterRecountState{fallback: fallback})
	reader := conversationpg.NewReader(pool, nil, nil, nil, func(s platform.Snapshot) conversation.PageCounter {
		return counterPageCounts{costPageCounter{s}, counters}
	})
	model, err := reader.Sidebar(ctx, v.member, v.channel, &v.selected)
	account.finish(err, false)
	return model, account, fallback, err
}

// This exhaustive scan is application drift/repair work, not an oracle or
// write-maintenance substitute. Check T and R independently, including off-list
// topics; equal errors in both must not hide behind an unchanged unread count.
const counterRebuildCounts = `WITH topics AS (
 SELECT t.id,count(m.id)::bigint AS total,count(m.id) FILTER (WHERE
 m.event_seq<=(SELECT joined_event_seq FROM member WHERE organization_id=$1 AND id=$3)
 OR EXISTS(SELECT 1 FROM read_range r WHERE r.organization_id=$1 AND r.channel_id=$2
 AND r.member_id=$3 AND r.lo<=m.event_seq AND m.event_seq<r.hi))::bigint AS read
 FROM topic t LEFT JOIN message m ON m.organization_id=t.organization_id AND m.channel_id=t.channel_id AND m.topic_id=t.id
 WHERE t.organization_id=$1 AND t.channel_id=$2 GROUP BY t.id
 ), scopes AS (SELECT * FROM topics UNION ALL SELECT $2,coalesce(sum(total),0)::bigint,coalesce(sum(read),0)::bigint FROM topics)
 `

func counterDetect(ctx context.Context, pool *pgxpool.Pool, w *counterSQLWrite) (bool, error) {
	ctx = context.WithValue(ctx, counterStandaloneKey{}, true)
	var drift bool
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, counterRebuildCounts+`SELECT coalesce(bool_or(t.n IS DISTINCT FROM s.total OR
 (b.member_id IS NOT NULL AND (NOT b.valid OR CASE WHEN s.id=$2 THEN b.n ELSE coalesce(r.n,0) END<>s.read))),false)
 FROM scopes s LEFT JOIN counter_total t ON t.organization_id=$1 AND t.channel_id=$2 AND t.scope_id=s.id
 LEFT JOIN counter_bundle b ON b.organization_id=$1 AND b.channel_id=$2 AND b.member_id=$3
 LEFT JOIN counter_topic_read r ON r.organization_id=$1 AND r.channel_id=$2 AND r.member_id=$3 AND r.topic_id=s.id`,
			w.organization, w.channel, w.member).Scan(&drift)
	})
	return drift, err
}

func counterRepair(ctx context.Context, pool *pgxpool.Pool, w *counterSQLWrite) error {
	ctx = context.WithValue(ctx, counterStandaloneKey{}, true)
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := counterSQLLock(ctx, tx.Conn(), w); err != nil {
			return err
		}
		return counterSQLCountExec(ctx, tx.Conn(), counterRebuildCounts+`, totals AS (
 INSERT INTO counter_total SELECT $1,$2,id,total FROM scopes
 ON CONFLICT(organization_id,channel_id,scope_id) DO UPDATE SET n=excluded.n RETURNING 1
 ), bundle AS (INSERT INTO counter_bundle SELECT $1,$2,$3,read,true,0 FROM scopes WHERE id=$2
 ON CONFLICT(organization_id,channel_id,member_id) DO UPDATE SET n=excluded.n,valid=true,revision=counter_bundle.revision+1 RETURNING 1
 ), reads AS (INSERT INTO counter_topic_read SELECT $1,$2,$3,id,read FROM topics CROSS JOIN bundle
 ON CONFLICT(organization_id,channel_id,member_id,topic_id) DO UPDATE SET n=excluded.n RETURNING 1)
 SELECT (SELECT count(*) FROM totals)+(SELECT count(*) FROM bundle)+(SELECT count(*) FROM reads)`,
			w.organization, w.channel, w.member)
	})
}

func TestUnreadCountersMatchedBudget(t *testing.T) {
	h := newCounterMatrix(t)
	for _, counters := range []bool{false, true} {
		t.Run(fmt.Sprint(counters), func(t *testing.T) {
			work, observer := counterWorkPools(t, h.pool, counters)
			for range 35 {
				conn, err := work.Acquire(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(conn.Release)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if conn, err := work.Acquire(ctx); err == nil {
				conn.Release()
				t.Fatal("application exceeded shared budget")
			}
			conn, err := observer.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			if work.Stat().AcquiredConns() != 35 || work.Config().MaxConns != 35 || observer.Config().MaxConns != 1 || observer.Stat().AcquiredConns() != 1 {
				t.Fatal("matched 35 application + 1 observer budget lost")
			}
		})
	}
}

func TestUnreadCountersRecountsAndRepair(t *testing.T) {
	h := newCounterMatrix(t)
	owner, missing := h.members[0], h.join(t, "missing")
	var selected kernel.ID
	for i := range 50 {
		selected = conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, fmt.Sprintf("Topic%02d", i)).ID
	}
	if _, err := h.pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,CASE WHEN s<200 THEN $3::uuid ELSE $5::uuid END,$4,'fixture',s
 FROM (SELECT generate_series(1,2) AS s UNION ALL SELECT generate_series(10,48) UNION ALL SELECT generate_series(50,115) UNION ALL SELECT generate_series(200,201)) seed`, owner.Organization.ID, h.channel, h.base, missing.Member.ID, selected); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(t.Context(), `WITH lock AS (
 INSERT INTO channel_read VALUES ($1,$2,$3) RETURNING member_id)
 INSERT INTO read_range SELECT $1,$2,member_id,lo,hi FROM lock CROSS JOIN (VALUES (0,12),(50,52)) bounds(lo,hi)`, owner.Organization.ID, h.channel, owner.Member.ID); err != nil {
		t.Fatal(err)
	}
	counterSQLTables(t, h.pool)
	_, key := counterSQLContext(t.Context(), owner, h.channel)
	channels := counterQuery(t, h.pool, `SELECT id FROM channel WHERE organization_id=$1`, owner.Organization.ID)
	ids, err := pgx.CollectRows(channels, pgx.RowTo[kernel.ID])
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		_, w := counterSQLContext(t.Context(), owner, id)
		counterSQLMaterialize(t, h.pool, w)
	}
	now := func() time.Time { return time.Unix(0, 0) }
	current, _ := counterWorkPools(t, h.pool, false)
	counters, _ := counterWorkPools(t, h.pool, true)
	check := func(m org.Membership, fallbackStatements, baseCount int64) {
		t.Helper()
		v := counterView{m, h.channel, selected}
		want, err := sidebarOracle(t.Context(), h.pool, m.Organization.ID, m.Member.ID, h.channel, &selected)
		if err != nil {
			t.Fatal(err)
		}
		ch, topics := map[kernel.ID]int64{}, map[kernel.ID]int64{}
		for _, e := range want.channels {
			ch[e.id] = e.count.capped
		}
		for _, e := range append(want.topics, want.selection...) {
			topics[e.id] = e.count.capped
		}
		for design, pool := range []*pgxpool.Pool{current, counters} {
			got, n, fallback, err := counterSidebar(t.Context(), pool, design == 1, v, now)
			if err != nil || !maps.Equal(got.ChannelCounts, ch) || !maps.Equal(got.TopicCounts, topics) || len(got.Topics) != 50 || got.Topic == nil || got.Topic.ID != selected {
				t.Fatalf("design %d recount vs oracle: %v; channel=%d/%d base=%d/%d selected=%d/%d", design, err, got.ChannelCounts[h.channel], ch[h.channel], got.TopicCounts[h.base], topics[h.base], got.TopicCounts[selected], topics[selected])
			}
			// Four list/selection + four current count statements, or one
			// counter lookup; fallback uses four production count statements.
			statements, extra := int64(8), int64(0)
			if design == 1 {
				statements, extra = 5, fallbackStatements
			}
			if n.statements != statements || n.controls != 2 || n.rows != 0 || len(n.transactions) != 1 || fallback.statements != extra || fallback.controls != 0 || fallback.rows != 0 || len(fallback.transactions) != 0 {
				t.Fatalf("recount accounting design=%d: main=%d/%d/%d/%d fallback=%d/%d/%d/%d", design, n.statements, n.controls, n.rows, len(n.transactions), fallback.statements, fallback.controls, fallback.rows, len(fallback.transactions))
			}
			if got.ChannelCounts[h.channel] != min(baseCount+2, 100) || got.TopicCounts[h.base] != baseCount || got.TopicCounts[selected] != 2 {
				t.Fatalf("fallback cap/selection: %v/%v", got.ChannelCounts, got.TopicCounts)
			}
		}
	}
	check(owner, 0, 100)
	check(missing, 4, 100)
	for i, corruption := range []string{
		`UPDATE counter_total SET n=n+1 WHERE organization_id=$1 AND channel_id=$2 AND scope_id=$2`,
		`UPDATE counter_bundle SET n=n+1 WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`,
		`UPDATE counter_topic_read SET n=n+1 WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND topic_id=$4`,
		`UPDATE counter_bundle SET valid=false WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`,
		`UPDATE read_range SET hi=CASE WHEN lo=0 THEN 49 ELSE 116 END WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`,
	} {
		detect := func(want bool) {
			t.Helper()
			ctx, n := counterAccountContext(t.Context(), now)
			drift, err := counterDetect(ctx, counters, key)
			n.finish(err, false)
			if err != nil || drift != want {
				t.Fatalf("drift detected/repaired: got %t want %t: %v", drift, want, err)
			}
			if n.statements != 1 || n.controls != 2 || n.rows != 0 || len(n.transactions) != 1 {
				t.Fatalf("detection accounting: %d/%d/%d/%d", n.statements, n.controls, n.rows, len(n.transactions))
			}
		}
		detect(false)
		if _, err := h.pool.Exec(t.Context(), corruption, []any{key.organization, key.channel, key.member, selected}[:[]int{2, 3, 4, 3, 3}[i]]...); err != nil {
			t.Fatal(err)
		}
		detect(true)
		if i == 3 {
			check(owner, 4, 100)
		}
		ctx, n := counterAccountContext(t.Context(), now)
		err := counterRepair(ctx, counters, key)
		n.finish(err, false)
		// Six locking statements (channel_read already exists), then one
		// CTE: 52 totals + one bundle + all 51 topic reads, including zeros.
		if err != nil || n.statements != 7 || n.controls != 2 || n.rows != 104 || len(n.transactions) != 1 {
			t.Fatalf("repair accounting: %d/%d/%d/%d: %v", n.statements, n.controls, n.rows, len(n.transactions), err)
		}
		detect(false)
		baseCount := int64(100)
		if i == 4 {
			baseCount = 0
		}
		check(owner, 0, baseCount)
		check(missing, 4, 100)
	}
	// Uncapped R is 2 pre-join + 39 + 66 fixture messages; the sequence
	// hole at 49 keeps the two read ranges disjoint and non-adjacent.
	var read, revision int64
	if err := h.pool.QueryRow(t.Context(), `SELECT r.n,b.revision FROM counter_topic_read r JOIN counter_bundle b USING(organization_id,channel_id,member_id) WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND topic_id=$4`, key.organization, key.channel, key.member, h.base).Scan(&read, &revision); err != nil || read != 107 || revision != 5 {
		t.Fatalf("uncapped repair: read=%d revision=%d want 107/5: %v", read, revision, err)
	}
	_, lazy := counterSQLContext(t.Context(), missing, h.channel)
	ctx, n := counterAccountContext(t.Context(), now)
	err = counterRepair(ctx, counters, lazy)
	n.finish(err, false)
	// The same 104 CTE writes plus the missing member's new channel_read row.
	if err != nil || n.statements != 7 || n.controls != 2 || n.rows != 105 || len(n.transactions) != 1 {
		t.Fatalf("lazy repair accounting: %d/%d/%d/%d: %v", n.statements, n.controls, n.rows, len(n.transactions), err)
	}
	if err := h.pool.QueryRow(t.Context(), `SELECT n FROM counter_bundle WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`, lazy.organization, lazy.channel, lazy.member).Scan(&read); err != nil || read != 2 {
		t.Fatalf("lazy repair join prefix: read=%d want 2: %v", read, err)
	}
	check(missing, 4, 100) // Only the other channel still falls back.
}
