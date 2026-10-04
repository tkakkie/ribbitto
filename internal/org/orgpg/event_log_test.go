package orgpg_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

func assertEventLog(t *testing.T, pool *pgxpool.Pool, organizationID kernel.ID, wantSeq int64) {
	t.Helper()
	var seq, boundary, count, valid int64
	requireNoError(t, pool.QueryRow(t.Context(), `
		SELECT o.event_seq, o.event_log_boundary_seq, count(e.seq),
		 count(e.seq) FILTER (WHERE e.seq > o.event_log_boundary_seq AND e.seq <= o.event_seq
		  AND e.audience_member_id IS NULL AND e.created_at IS NOT NULL
		  AND e.kind = 'member.joined' AND EXISTS (SELECT FROM member m
		   WHERE m.organization_id = o.id AND m.joined_event_seq = e.seq
		   AND e.data = jsonb_build_object('member_id', m.id)))
		FROM organization o LEFT JOIN event_log e ON e.organization_id = o.id
		WHERE o.id = $1 GROUP BY o.id`, organizationID).Scan(&seq, &boundary, &count, &valid))
	// These fresh organisations have no retention boundary; the primary key
	// makes the expected row count prove there are no gaps.
	if seq != wantSeq || boundary != 0 || count != wantSeq || valid != count {
		t.Fatalf("event log: seq=%d boundary=%d rows=%d valid=%d; want seq=%d", seq, boundary, count, valid, wantSeq)
	}
}
