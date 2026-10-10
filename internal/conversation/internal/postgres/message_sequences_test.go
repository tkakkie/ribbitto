package postgres_test

import (
	"testing"

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
		{other.OrganizationID, f.Channel.ID, other.Channel.DefaultTopicID, other.MemberID, 4},
		{f.OrganizationID, sibling.ID, sibling.DefaultTopicID, f.MemberID, 5},
	} {
		_, err := writer.InsertMessage(t.Context(), m.org, m.channel, m.topic, m.member, "bounds", m.seq)
		requireNoError(t, err)
	}
	requireNoError(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		bounds := postgres.MessageSequencesIn(tx)
		for _, tc := range []struct{ cursor, want int64 }{{3, 8}, {8, 0}} {
			got, err := bounds.FirstMessageAfter(t.Context(), f.OrganizationID, f.Channel.ID, tc.cursor)
			requireNoError(t, err)
			if got != tc.want {
				t.Fatalf("after %d=%d, want %d", tc.cursor, got, tc.want)
			}
		}
		return nil
	}))
}
