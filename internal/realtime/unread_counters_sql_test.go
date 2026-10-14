package realtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// The tracer exposes the production transaction without unwrapping platform.Tx.
// Only BEGIN/COMMIT hooks issue SQL; ordinary queries cannot re-enter them.
type counterSQLAdapter struct{}
type counterSQLKey struct{}
type counterSQLBegin struct{}
type counterSQLFloorWrite struct{}
type counterSQLWrite struct {
	organization, channel, member kernel.ID
	before, floors                string
	seq                           int64
	moveFrom                      kernel.ID
	moves                         []int64
	join                          bool
	interval                      *counterIntervalWrite
	err                           error
}

func counterSQLExec(ctx context.Context, conn *pgx.Conn, sql string, args ...any) error {
	_, err := conn.Exec(ctx, sql, args...)
	return err
}

func counterSQLLock(ctx context.Context, conn *pgx.Conn, w *counterSQLWrite) error {
	for _, q := range []struct {
		sql  string
		keys int
	}{
		{`SELECT id FROM organization WHERE id=$1 FOR NO KEY UPDATE`, 1},
		{`SELECT scope_id FROM counter_total WHERE organization_id=$1 AND channel_id=$2 ORDER BY (scope_id<>channel_id),scope_id FOR UPDATE`, 2},
		{`INSERT INTO channel_read VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, 3},
		{`SELECT member_id FROM channel_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 FOR UPDATE`, 3},
		{`SELECT member_id FROM counter_bundle WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 FOR UPDATE`, 3},
		{`SELECT topic_id FROM counter_topic_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 ORDER BY topic_id FOR UPDATE`, 3},
	} {
		if w.join && q.keys > 1 {
			return nil
		}
		if err := counterSQLExec(ctx, conn, q.sql, []any{w.organization, w.channel, w.member}[:q.keys]...); err != nil {
			return err
		}
		if q.keys == 2 && len(w.moves) > 0 {
			for i, sql := range []string{
				`INSERT INTO channel_read SELECT organization_id,channel_id,member_id FROM counter_bundle WHERE organization_id=$1 AND channel_id=$2 AND valid ON CONFLICT DO NOTHING`,
				`SELECT member_id FROM channel_read WHERE organization_id=$1 AND channel_id=$2 ORDER BY member_id FOR UPDATE`,
				`SELECT member_id FROM counter_bundle WHERE organization_id=$1 AND channel_id=$2 ORDER BY member_id FOR UPDATE`,
				`SELECT topic_id FROM counter_topic_read WHERE organization_id=$1 AND channel_id=$2 ORDER BY member_id,topic_id FOR UPDATE`,
			} {
				lockCtx := ctx
				if i > 0 {
					lockCtx = context.WithValue(ctx, counterMoveLockKey{}, i-1)
				}
				if err := counterSQLExec(lockCtx, conn, sql, w.organization, w.channel); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func counterSQLState(ctx context.Context, conn *pgx.Conn, w *counterSQLWrite) (string, string, error) {
	var coverage, floors string
	err := conn.QueryRow(ctx, `SELECT
 (coalesce((SELECT range_agg(int8range(lo,hi)) FROM read_range
 WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3),'{}'::int8multirange)
 + int8multirange(int8range(0,(SELECT joined_event_seq+1 FROM member WHERE organization_id=$1 AND id=$3))))::text,
 coalesce((SELECT jsonb_agg(jsonb_build_array(topic_id,floor_seq) ORDER BY topic_id)
 FROM topic_read_floor WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3),'[]')::text`,
		w.organization, w.channel, w.member).Scan(&coverage, &floors)
	return coverage, floors, err
}

func (counterSQLAdapter) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	w, _ := ctx.Value(counterSQLKey{}).(*counterSQLWrite)
	if w != nil && w.interval != nil && ctx.Value(counterSQLFloorWrite{}) == true && data.Err == nil && data.CommandTag.RowsAffected() > 0 {
		w.interval.changed = true
	}
	if w == nil || ctx.Value(counterSQLBegin{}) != true || data.Err != nil {
		return
	}
	w.err = counterSQLLock(ctx, conn, w)
	if w.err == nil && !w.join && w.interval == nil {
		w.before, w.floors, w.err = counterSQLState(ctx, conn, w)
	}
	if w.err != nil {
		_, _ = conn.Exec(ctx, `SELECT 1/0`)
	}
}

func (counterSQLAdapter) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx = context.WithValue(ctx, counterSQLBegin{}, data.SQL == "begin")
	ctx = context.WithValue(ctx, counterSQLFloorWrite{}, strings.HasPrefix(data.SQL, "-- name: InsertTopicReadFloor ") || strings.HasPrefix(data.SQL, "-- name: RaiseTopicReadFloor "))
	w, _ := ctx.Value(counterSQLKey{}).(*counterSQLWrite)
	if w == nil && data.SQL == "commit" && ctx.Value(counterStandaloneKey{}) != true {
		// Only explicit recount/detection/repair calls bypass write tracking.
		// A production write without a tracker
		// would commit without counter maintenance, so abort it instead.
		_, _ = conn.Exec(ctx, `SELECT 1/0`)
		return ctx
	}
	if w == nil || data.SQL != "commit" || w.err != nil || w.join {
		return ctx
	}
	var after, floors string
	var err error
	if w.interval == nil {
		// Correctness-only: whole-coverage diff and channel scan, never evidence.
		after, floors, err = counterSQLState(ctx, conn, w)
	}
	if err == nil && len(w.moves) > 0 {
		err = counterSQLTransfer(ctx, conn, w)
	}
	if err == nil && w.seq != 0 {
		err = counterSQLExec(ctx, conn, `INSERT INTO counter_total
 SELECT $1,$2,s,1 FROM message m CROSS JOIN LATERAL unnest(ARRAY[m.channel_id,m.topic_id]) s
 WHERE m.organization_id=$1 AND m.channel_id=$2 AND m.event_seq=$3
 ON CONFLICT (organization_id,channel_id,scope_id) DO UPDATE SET n=counter_total.n+1`, w.organization, w.channel, w.seq)
	}
	if err == nil && w.interval != nil {
		err = counterSQLIntervalMaintain(ctx, conn, w)
	} else if err == nil {
		// Count messages in the exact new coverage, never interval widths or a
		// capped recount. The own post belongs to this difference exactly once.
		err = counterSQLExec(ctx, conn, `WITH delta AS (
 SELECT topic_id,count(*) AS n FROM message WHERE organization_id=$1 AND channel_id=$2
 AND ($4::int8multirange - $5::int8multirange) @> event_seq GROUP BY topic_id
 ), topics AS (
 INSERT INTO counter_topic_read SELECT $1,$2,$3,d.topic_id,d.n FROM delta d
 WHERE EXISTS (SELECT 1 FROM counter_bundle WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND valid)
 ON CONFLICT (organization_id,channel_id,member_id,topic_id) DO UPDATE SET n=counter_topic_read.n+excluded.n
 ) UPDATE counter_bundle SET n=n+CASE WHEN valid THEN (SELECT coalesce(sum(n),0) FROM delta) ELSE 0 END,
 revision=revision+CASE WHEN $6 THEN 1 ELSE 0 END WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`,
			w.organization, w.channel, w.member, after, w.before, after != w.before || floors != w.floors)
	}
	if err != nil {
		w.err = err
		_, _ = conn.Exec(ctx, `SELECT 1/0`)
	}
	return ctx
}

type counterSQLPostReads struct{ tx platform.Tx }

func (r counterSQLPostReads) Read(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, cursor *int64, lo, hi, floor int64) error {
	if err := counterPostReads(r).Read(ctx, m, channel, topic, cursor, lo, hi, floor); err != nil {
		return err
	}
	w, ok := ctx.Value(counterSQLKey{}).(*counterSQLWrite)
	if !ok || w == nil {
		return errors.New("counter SQL post without a write tracker")
	}
	w.seq = hi - 1
	return nil
}

func counterSQLTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `CREATE TABLE counter_total (
 organization_id uuid NOT NULL,channel_id uuid NOT NULL,scope_id uuid NOT NULL,n bigint NOT NULL CHECK(n>=0),
 PRIMARY KEY(organization_id,channel_id,scope_id), FOREIGN KEY(organization_id,channel_id) REFERENCES channel(organization_id,id));
 CREATE TABLE counter_bundle (
 organization_id uuid NOT NULL,channel_id uuid NOT NULL,member_id uuid NOT NULL,n bigint NOT NULL CHECK(n>=0),
 valid boolean NOT NULL,revision bigint NOT NULL CHECK(revision>=0), PRIMARY KEY(organization_id,channel_id,member_id),
 FOREIGN KEY(organization_id,channel_id) REFERENCES channel(organization_id,id),
 FOREIGN KEY(organization_id,member_id) REFERENCES member(organization_id,id));
 CREATE TABLE counter_topic_read (
 organization_id uuid NOT NULL,channel_id uuid NOT NULL,member_id uuid NOT NULL,topic_id uuid NOT NULL,n bigint NOT NULL CHECK(n>=0),
 PRIMARY KEY(organization_id,channel_id,member_id,topic_id),
 FOREIGN KEY(organization_id,channel_id,member_id) REFERENCES counter_bundle,
 FOREIGN KEY(organization_id,channel_id,topic_id) REFERENCES topic(organization_id,channel_id,id));
 INSERT INTO counter_total SELECT c.organization_id,c.id,c.id,count(m.id) FROM channel c
 LEFT JOIN message m ON m.organization_id=c.organization_id AND m.channel_id=c.id GROUP BY c.organization_id,c.id;
 INSERT INTO counter_total SELECT t.organization_id,t.channel_id,t.id,count(m.id) FROM topic t
 LEFT JOIN message m ON m.organization_id=t.organization_id AND m.channel_id=t.channel_id AND m.topic_id=t.id GROUP BY t.organization_id,t.channel_id,t.id;`)
	if err != nil {
		t.Fatal(err)
	}
}

func counterSQLMaterialize(t *testing.T, pool *pgxpool.Pool, w *counterSQLWrite) {
	t.Helper()
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if err := counterSQLLock(t.Context(), tx.Conn(), w); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `WITH reads AS (
 SELECT t.id,count(m.id) AS n FROM topic t LEFT JOIN message m
 ON m.organization_id=t.organization_id AND m.channel_id=t.channel_id AND m.topic_id=t.id
 AND (m.event_seq <= (SELECT joined_event_seq FROM member WHERE organization_id=$1 AND id=$3)
 OR EXISTS(SELECT 1 FROM read_range r WHERE r.organization_id=$1 AND r.channel_id=$2 AND r.member_id=$3 AND r.lo<=m.event_seq AND m.event_seq<r.hi))
 WHERE t.organization_id=$1 AND t.channel_id=$2 GROUP BY t.id
 ), bundle AS (
 INSERT INTO counter_bundle VALUES ($1,$2,$3,(SELECT coalesce(sum(n),0) FROM reads),true,0)
 ON CONFLICT(organization_id,channel_id,member_id) DO UPDATE SET n=excluded.n,valid=true,revision=counter_bundle.revision+1
 RETURNING member_id
 ) INSERT INTO counter_topic_read SELECT $1,$2,b.member_id,r.id,r.n FROM reads r CROSS JOIN bundle b
 ON CONFLICT(organization_id,channel_id,member_id,topic_id) DO UPDATE SET n=excluded.n`, w.organization, w.channel, w.member)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Verification reads T/R separately as well as U: equal errors must not cancel.
// Missing/invalid bundles execute the range-based SQL fallback in this snapshot.
func counterSQLModel(t *testing.T, tx pgx.Tx, v counterView, truth counterTruth) sidebarOracleModel {
	t.Helper()
	organization, member := v.member.Organization.ID, v.member.Member.ID
	model := sidebarOracleModel{organization: organization, member: member, current: v.channel}
	rows := counterQuery(t, tx, `WITH entries AS (
 SELECT 0 AS kind,id,name,id AS channel,false AS is_default FROM channel WHERE organization_id=$1
 UNION ALL SELECT 1,id,coalesce(name,''),channel_id,is_default FROM topic WHERE organization_id=$1 AND channel_id=$3
 ) SELECT e.kind,e.id,e.name,e.channel,coalesce(b.valid,false),coalesce(t.n,0),
 CASE WHEN e.kind=0 THEN coalesce(b.n,0) ELSE coalesce(r.n,0) END,
 CASE WHEN b.valid THEN coalesce(t.n,0)-CASE WHEN e.kind=0 THEN b.n ELSE coalesce(r.n,0) END
 ELSE (SELECT count(*) FROM message m WHERE m.organization_id=$1 AND m.channel_id=e.channel
 AND (e.kind=0 OR m.topic_id=e.id)
 AND m.event_seq>(SELECT joined_event_seq FROM member WHERE organization_id=$1 AND id=$2)
 AND NOT EXISTS(SELECT 1 FROM read_range rr WHERE rr.organization_id=$1 AND rr.channel_id=e.channel
 AND rr.member_id=$2 AND rr.lo<=m.event_seq AND m.event_seq<rr.hi)) END
 FROM entries e
 LEFT JOIN counter_total t ON t.organization_id=$1 AND t.channel_id=e.channel AND t.scope_id=e.id
 LEFT JOIN counter_bundle b ON b.organization_id=$1 AND b.channel_id=e.channel AND b.member_id=$2
 LEFT JOIN counter_topic_read r ON r.organization_id=$1 AND r.channel_id=e.channel AND r.member_id=$2 AND r.topic_id=e.id
 ORDER BY e.kind,e.is_default DESC,CASE WHEN e.kind=0 THEN e.name ELSE lower(e.name) END,e.id`, organization, member, v.channel)
	defer rows.Close()
	for rows.Next() {
		var entry sidebarOracleEntry
		var kind int
		var channel kernel.ID
		var valid bool
		var total, read, n int64
		if err := rows.Scan(&kind, &entry.id, &entry.name, &channel, &valid, &total, &read, &n); err != nil {
			t.Fatal(err)
		}
		wantTotal := counterRecount(truth, channel, kernel.ID{}, false)[entry.id]
		wantRead := counterRecount(truth, channel, member, true)[entry.id]
		if n != wantTotal-wantRead {
			t.Fatalf("raw SQL unread differs from truth: scope=%x unread=%d/%d", entry.id, n, wantTotal-wantRead)
		}
		if total != wantTotal || valid && read != wantRead {
			t.Fatalf("persisted totals/read counts differ from truth: scope=%x total=%d/%d read=%d/%d", entry.id, total, wantTotal, read, wantRead)
		}
		entry.count = counterDisplay(n)
		if kind == 0 {
			model.channels = append(model.channels, entry)
		} else {
			if len(model.topics) < 50 {
				model.topics = append(model.topics, entry)
			}
			if entry.id == v.selected {
				model.selection = []sidebarOracleEntry{entry}
			}
		}
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	return model
}

func counterSQLContext(ctx context.Context, m org.Membership, channel kernel.ID) (context.Context, *counterSQLWrite) {
	w := &counterSQLWrite{organization: m.Organization.ID, channel: channel, member: m.Member.ID}
	return context.WithValue(ctx, counterSQLKey{}, w), w
}
