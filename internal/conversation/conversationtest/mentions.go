package conversationtest

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// AssertMentions verifies the exact members and copied sequence against the
// persisted message, for both posting and historical backfill tests.
func AssertMentions(t *testing.T, pool *pgxpool.Pool, organizationID, messageID kernel.ID, eventSeq int64, members []kernel.ID) {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT r.member_id, r.event_seq, m.event_seq
		FROM message_mention r LEFT JOIN message m ON m.organization_id = r.organization_id AND m.id = r.message_id
		WHERE r.organization_id = $1 AND r.message_id = $2`, organizationID, messageID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := make(map[kernel.ID]bool, len(members))
	for _, id := range members {
		want[id] = true
	}
	var count int
	for rows.Next() {
		var id kernel.ID
		var mentionSeq, messageSeq int64
		if err := rows.Scan(&id, &mentionSeq, &messageSeq); err != nil {
			t.Fatal(err)
		}
		if !want[id] || mentionSeq != eventSeq || messageSeq != eventSeq {
			t.Fatalf("mention %v: sequences=%d/%d, want member=%t sequence=%d", id, mentionSeq, messageSeq, want[id], eventSeq)
		}
		delete(want, id)
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 || count != len(members) {
		t.Fatalf("mentions=%d, want %d; missing=%v", count, len(members), want)
	}
}
