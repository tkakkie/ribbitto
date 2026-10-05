package conversationpg_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestPosting(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	query := func(sql string, args []any, dest ...any) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), sql, args...).Scan(dest...); err != nil {
			t.Fatal(err)
		}
	}
	organizationID := fixtureOrganization(t, pool, "acme")
	fixtureDefaultChannel(t, pool, organizationID)
	var memberID, channelID kernel.ID
	query(`WITH account AS (INSERT INTO account (email, display_name, password_hash)
		VALUES ('alice@example.org', 'Alice', '$argon2id$x') RETURNING id)
		INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle)
		SELECT $1, id, 'member', 1, 'alice' FROM account RETURNING id`, []any{organizationID}, &memberID)
	query("SELECT id FROM channel WHERE organization_id = $1", []any{organizationID}, &channelID)
	posting := conversationpg.NewPosting(pool,
		func(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) },
		func(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }, nil)
	m := org.Membership{Organization: org.Organization{ID: organizationID}, Member: org.Member{ID: memberID}}
	for next := int64(1); next <= 2; next++ {
		posted, err := posting.Post(t.Context(), m, channelID, "  hello\r\nworld  ")
		if err != nil {
			t.Fatal(err)
		}
		var messageSeq, eventSeq, organizationSeq int64
		var body, kind string
		var data []byte
		query(`SELECT m.event_seq, e.seq, o.event_seq, m.body, e.kind, e.data
			FROM message m JOIN event_log e ON e.organization_id = m.organization_id AND e.seq = m.event_seq
			JOIN organization o ON o.id = m.organization_id WHERE m.organization_id = $1 AND m.id = $2`,
			[]any{organizationID, posted.ID}, &messageSeq, &eventSeq, &organizationSeq, &body, &kind, &data)
		payload, err := conversation.DecodePosted(data)
		if err != nil || messageSeq != next || eventSeq != next || organizationSeq != next || posted.EventSeq != next || body != "hello\nworld" || kind != string(conversation.KindPosted) || payload.ChannelID != channelID || payload.MessageID != posted.ID || payload.TopicID == nil || *payload.TopicID != posted.TopicID {
			t.Fatalf("post/event = %+v, %d/%d/%d, %q, %q, %+v, %v", posted, messageSeq, eventSeq, organizationSeq, body, kind, payload, err)
		}
	}
}
