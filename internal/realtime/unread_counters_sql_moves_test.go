package realtime_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// The workload supplies selected original sequences; the production branch
// validates their source under the organisation lock. Classification
// ignores floors; the notice union cannot cover any older selected message.
func counterSQLTransfer(ctx context.Context, conn *pgx.Conn, w *counterSQLWrite) error {
	if err := counterSQLExec(ctx, conn, `WITH source AS (
 UPDATE counter_total SET n=n-cardinality($3::bigint[])
 WHERE organization_id=$1 AND channel_id=$2 AND scope_id=$4
 ), delta AS (
 SELECT topic_id AS scope_id,count(*) AS n FROM message
 WHERE organization_id=$1 AND channel_id=$2 AND event_seq=ANY($3) GROUP BY topic_id
 ) INSERT INTO counter_total SELECT $1,$2,scope_id,n FROM delta
 ON CONFLICT(organization_id,channel_id,scope_id) DO UPDATE SET n=counter_total.n+excluded.n`,
		w.organization, w.channel, w.moves, w.moveFrom); err != nil {
		return err
	}
	// Source decrements must UPDATE existing nonnegative rows: inserting a
	// negative candidate would fail CHECK before ON CONFLICT can add it.
	return counterSQLExec(ctx, conn, `WITH moved AS (
 SELECT m.topic_id,m.event_seq FROM message m
 WHERE m.organization_id=$1 AND m.channel_id=$2 AND m.event_seq=ANY($3)
 ), reads AS (
 SELECT b.member_id,m.topic_id,count(*) AS n FROM counter_bundle b
 JOIN member u ON u.organization_id=b.organization_id AND u.id=b.member_id
 CROSS JOIN moved m WHERE b.organization_id=$1 AND b.channel_id=$2 AND b.valid
 AND (m.event_seq<=u.joined_event_seq OR EXISTS(SELECT 1 FROM read_range r
 WHERE r.organization_id=$1 AND r.channel_id=$2 AND r.member_id=b.member_id
 AND r.lo<=m.event_seq AND m.event_seq<r.hi)) GROUP BY b.member_id,m.topic_id
 ), source AS (
 UPDATE counter_topic_read r SET n=r.n-d.n FROM
 (SELECT member_id,sum(n)::bigint AS n FROM reads GROUP BY member_id) d
 WHERE r.organization_id=$1 AND r.channel_id=$2 AND r.topic_id=$4 AND r.member_id=d.member_id
 ) INSERT INTO counter_topic_read SELECT $1,$2,member_id,topic_id,n FROM reads
 ON CONFLICT(organization_id,channel_id,member_id,topic_id) DO UPDATE SET n=counter_topic_read.n+excluded.n`,
		w.organization, w.channel, w.moves, w.moveFrom)
}

func TestUnreadCountersSQLTransactions(t *testing.T) {
	counterTransactions(t, true)
}

// Both passes use the caller's pinned snapshot, including fallback and lists.
func counterSQLCheckpoint(t *testing.T, tx pgx.Tx, h *counterMatrix, pinned map[kernel.ID]sidebarOracleModel, pass int) {
	t.Helper()
	truth := counterLoad(t, tx, h.members[0].Organization.ID)
	selected := h.base
	if err := tx.QueryRow(t.Context(), `SELECT id FROM topic WHERE organization_id=$1 AND channel_id=$2 AND name='ZDestination'`, h.members[0].Organization.ID, h.channel).Scan(&selected); err != nil && err != pgx.ErrNoRows {
		t.Fatal(err)
	}
	for _, member := range truth.members {
		m := org.Membership{Organization: h.members[0].Organization, Member: org.Member{ID: member.ID}}
		got := counterSQLModel(t, tx, counterView{m, h.channel, selected}, truth)
		want, err := sidebarOracleIn(t.Context(), tx, m.Organization.ID, member.ID, h.channel, &selected)
		if err != nil {
			t.Fatal(err)
		}
		if err := sidebarOracleCompare(got, want); err != nil {
			t.Fatal("SQL transaction differs from oracle: ", err)
		}
		if pass == 1 {
			if err := sidebarOracleCompare(got, pinned[member.ID]); err != nil {
				t.Fatal("SQL snapshot changed across outcome: ", err)
			}
		}
		pinned[member.ID] = got
	}
}
