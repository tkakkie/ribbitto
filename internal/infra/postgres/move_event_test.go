package postgres_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestMoveEventPayload(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := pgtest.OrganizationWithOwner(t, pool, "move-event", "general")
	other := pgtest.OrganizationWithOwner(t, pool, "other-event", "general")
	_, err := pool.Exec(t.Context(), `INSERT INTO event_log (organization_id, seq, kind, data)
		VALUES ($1, 1, 'member.joined', jsonb_build_object('member_id', $2::uuid))`, f.OrganizationID, f.MemberID)
	requireNoError(t, err)
	uuid := func(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012x", n) }
	reader := realtimepg.NewReader(pool, postgres.EventBoundsIn, postgres.EventKinds())
	// A lower write-side limit must not make already-committed moves unreadable.
	ids := make([]string, topic.MaxBranchMessages+1)
	for i := range ids {
		ids[i] = uuid(i + 4)
	}
	encoded, err := json.Marshal(map[string]any{
		"channel_id": uuid(1), "from_topic_id": uuid(2), "to_topic_id": uuid(3), "message_ids": ids,
	})
	requireNoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
		VALUES ($1, 2, 'messages.moved', $3, $2)`, f.OrganizationID, encoded, f.MemberID)
	requireNoError(t, err)
	_, err = pool.Exec(t.Context(), "UPDATE organization SET event_seq = 2 WHERE id = $1", f.OrganizationID)
	requireNoError(t, err)
	events, err := reader.EventsAfter(t.Context(), f.OrganizationID, 1, 1)
	requireNoError(t, err)
	if len(events) != 1 || events[0].Kind != topic.KindMessagesMoved ||
		events[0].AudienceMemberID == nil || *events[0].AudienceMemberID != f.MemberID {
		t.Fatalf("targeted move larger than the write-side limit: %+v", events)
	}
	moved, err := topic.DecodeMoved(events[0].Payload)
	if err != nil || len(moved.MessageIDs) != len(ids) {
		t.Fatalf("move payload: %d IDs, %v; want %d", len(moved.MessageIDs), err, len(ids))
	}
	for i, id := range moved.MessageIDs {
		if got := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]); got != ids[i] {
			t.Fatalf("message %d = %s, want %s", i, got, ids[i])
		}
	}
	events, err = reader.EventsAfter(t.Context(), other.OrganizationID, 1, 10)
	requireNoError(t, err)
	if len(events) != 0 {
		t.Fatalf("another organisation received the move: %+v", events)
	}
}
