package realtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/unread"
)

// Measurement-intended (unmeasured): effects belong to the locked source write,
// never a before/after scan of the member's entire coverage.
type counterIntervalWrite struct {
	covered  []unread.Range
	topics   map[kernel.ID]int64
	changed  bool
	captured bool
}

func counterIntervalIn(ctx context.Context) *counterIntervalWrite {
	w, _ := ctx.Value(counterSQLKey{}).(*counterSQLWrite)
	if w == nil {
		return nil
	}
	return w.interval
}

func counterSQLUseIntervals(w *counterSQLWrite) {
	w.interval = &counterIntervalWrite{topics: map[kernel.ID]int64{}}
}

// A structural guard keeps correctness scaffolding out of this path even when
// both maintenance shapes produce identical counts. It collects no telemetry.
func counterSQLIntervalGuard(t *testing.T, ctx context.Context, sql string) {
	t.Helper()
	if counterIntervalIn(ctx) != nil && (strings.Contains(sql, "range_agg") || strings.Contains(sql, "$4::int8multirange - $5::int8multirange")) {
		t.Error("measurement-intended shape executed correctness-only SQL")
	}
}

type counterIntervalAdapter struct {
	counterSQLAdapter
	t *testing.T
}

func (a counterIntervalAdapter) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	counterSQLIntervalGuard(a.t, ctx, data.SQL)
	return a.counterSQLAdapter.TraceQueryStart(ctx, conn, data)
}

type counterIntervalRanges struct{ unread.RangeWriter }

func (r counterIntervalRanges) Merge(ctx context.Context, s unread.Scope, joined int64, added unread.Range) error {
	_, err := r.MergeCovered(ctx, s, joined, added)
	return err
}

func (r counterIntervalRanges) MergeCovered(ctx context.Context, s unread.Scope, joined int64, added unread.Range) ([]unread.Range, error) {
	covered, err := r.RangeWriter.MergeCovered(ctx, s, joined, added)
	if w := counterIntervalIn(ctx); w != nil && err == nil {
		w.covered = append(w.covered, covered...)
		w.changed = w.changed || len(covered) > 0
		w.captured = true
	}
	return covered, err
}

type counterIntervalPrepared struct{ unread.PreparedTopic }

func (p counterIntervalPrepared) Add(ctx context.Context, los, his []int64, floor int64) error {
	if err := p.PreparedTopic.Add(ctx, los, his, floor); err != nil {
		return err
	}
	if w := counterIntervalIn(ctx); w != nil {
		w.changed = w.changed || len(los) > 0
		w.captured = true
	}
	return nil
}

func counterSQLIntervalMaintain(ctx context.Context, conn *pgx.Conn, w *counterSQLWrite) error {
	if !w.interval.captured {
		return errors.New("interval maintenance without write-owned capture")
	}
	los, his := []int64{}, []int64{}
	for _, r := range w.interval.covered {
		los, his = append(los, r.Lo), append(his, r.Hi)
	}
	topics, counts := []kernel.ID{}, []int64{}
	for topic, n := range w.interval.topics {
		topics, counts = append(topics, topic), append(counts, n)
	}
	// Each lateral aggregate has equality on the index's scope keys and a
	// half-open event_seq range. Sequence holes contribute no messages.
	return counterSQLExec(ctx, conn, `WITH intervals AS (
 SELECT lo,hi FROM unnest($4::bigint[],$5::bigint[]) b(lo,hi)
 ), effects AS (
 SELECT d.topic_id,d.n FROM intervals i CROSS JOIN LATERAL (
 SELECT topic_id,count(*) AS n FROM message WHERE organization_id=$1 AND channel_id=$2
 AND event_seq>=i.lo AND event_seq<i.hi GROUP BY topic_id) d
 UNION ALL SELECT topic_id,n FROM unnest($6::uuid[],$7::bigint[]) t(topic_id,n)
 ), delta AS (SELECT topic_id,sum(n)::bigint AS n FROM effects GROUP BY topic_id HAVING sum(n)>0), topics AS (
 INSERT INTO counter_topic_read SELECT $1,$2,$3,d.topic_id,d.n FROM delta d
 WHERE EXISTS (SELECT 1 FROM counter_bundle WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND valid)
 ON CONFLICT (organization_id,channel_id,member_id,topic_id) DO UPDATE SET n=counter_topic_read.n+excluded.n
 ) UPDATE counter_bundle SET n=n+CASE WHEN valid THEN (SELECT coalesce(sum(n),0) FROM delta) ELSE 0 END,
 revision=revision+CASE WHEN $8 THEN 1 ELSE 0 END
 WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3
 AND ($8 OR EXISTS(SELECT 1 FROM delta))`,
		w.organization, w.channel, w.member, los, his, topics, counts, w.interval.changed)
}

func TestUnreadCountersIntervalCore(t *testing.T) {
	counterSQLCore(t, true)
}

func TestUnreadCountersIntervalTransactions(t *testing.T) {
	counterTransactions(t, true, true)
}
