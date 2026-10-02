package postgres_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
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
	payload := func() map[string]any {
		return map[string]any{
			"channel_id": uuid(1), "from_topic_id": uuid(2), "to_topic_id": uuid(3),
			"message_ids": []string{uuid(4)},
		}
	}
	reader := postgres.NewEventReader(pool)
	for _, tt := range []struct {
		name  string
		field string
		value any
	}{
		{"missing channel", "channel_id", nil},
		{"malformed channel", "channel_id", "invalid"},
		{"missing source", "from_topic_id", nil},
		{"malformed source", "from_topic_id", "invalid"},
		{"missing destination", "to_topic_id", nil},
		{"malformed destination", "to_topic_id", "invalid"},
		{"same topic", "to_topic_id", uuid(2)},
		{"missing messages", "message_ids", nil},
		{"null messages", "message_ids", json.RawMessage(`null`)},
		{"empty messages", "message_ids", []string{}},
		{"repeated message", "message_ids", []string{uuid(4), uuid(4)}},
		{"malformed message", "message_ids", []string{uuid(4), "invalid"}},
		{"noncanonical message", "message_ids", []string{"00000000x0000x0000x0000x000000000004"}},
		{"null message", "message_ids", []any{nil}},
		{"wrong message type", "message_ids", []any{false}},
		{"wrong list type", "message_ids", uuid(4)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := payload()
			if tt.value == nil {
				delete(data, tt.field)
			} else {
				data[tt.field] = tt.value
			}
			encoded, err := json.Marshal(data)
			requireNoError(t, err)
			_, err = pool.Exec(t.Context(), `INSERT INTO event_log (organization_id, seq, kind, data)
				VALUES ($1, 2, 'messages.moved', $2) ON CONFLICT (organization_id, seq) DO UPDATE SET data = EXCLUDED.data`, f.OrganizationID, encoded)
			requireNoError(t, err)
			_, err = pool.Exec(t.Context(), "UPDATE organization SET event_seq = 2 WHERE id = $1", f.OrganizationID)
			requireNoError(t, err)
			// The valid preceding join must not escape as a partial batch.
			if events, err := reader.EventsAfter(t.Context(), f.OrganizationID, 0, 10); err == nil || len(events) != 0 {
				t.Fatalf("malformed move: %+v, %v; want error without events", events, err)
			}
		})
	}
	data := payload()
	// A lower write-side limit must not make already-committed moves unreadable.
	ids := make([]string, topic.MaxBranchMessages+1)
	for i := range ids {
		ids[i] = uuid(i + 4)
	}
	data["message_ids"] = ids
	encoded, err := json.Marshal(data)
	requireNoError(t, err)
	_, err = pool.Exec(t.Context(), "UPDATE event_log SET data = $2, audience_member_id = $3 WHERE organization_id = $1 AND seq = 2", f.OrganizationID, encoded, f.MemberID)
	requireNoError(t, err)
	events, err := reader.EventsAfter(t.Context(), f.OrganizationID, 1, 1)
	requireNoError(t, err)
	if len(events) != 1 || events[0].Kind != domain.EventMessagesMoved || len(events[0].MessageIDs) != len(ids) ||
		events[0].AudienceMemberID == nil || *events[0].AudienceMemberID != f.MemberID {
		t.Fatalf("targeted move larger than the write-side limit: %+v", events)
	}
	for i, id := range events[0].MessageIDs {
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
