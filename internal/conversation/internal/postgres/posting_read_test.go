package postgres_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestPostingTopicGuard(t *testing.T) {
	for _, dimension := range []string{"organization", "channel", "topic", "post", "move", "at cursor", "at post"} {
		t.Run(dimension, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "guard", "general")
			other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
			org, channel, topic, member := f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID
			// Unique IDs otherwise conceal missing scope predicates. Relax only the
			// two message FKs that prevent a decoy differing in exactly one scope.
			_, err := pool.Exec(t.Context(), "ALTER TABLE message DROP CONSTRAINT message_organization_id_channel_id_fkey, DROP CONSTRAINT message_topic_fkey")
			requireNoError(t, err)
			seq, moved := int64(5), int64(0)
			switch dimension {
			case "organization":
				org, member = other.OrganizationID, other.MemberID
			case "channel":
				channel = other.Channel.ID
			case "topic":
				topic = conversationtest.Topic(t, pool, org, channel, "other").ID
			case "move":
				seq, moved = 2, 5
			case "at cursor":
				seq, moved = 3, 3
			case "at post":
				seq, moved = 7, 7
			}
			_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq,moved_event_seq) VALUES ($1,$2,$3,$4,'decoy',$5,nullif($6,0))", org, channel, topic, member, seq, moved)
			requireNoError(t, err)
			got, err := postgres.NewWriterForTest(pool).TopicChangedBetween(t.Context(), f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, 3, 7)
			requireNoError(t, err)
			if want := dimension == "post" || dimension == "move"; got != want {
				t.Fatalf("changed=%t, want %t", got, want)
			}
		})
	}
}
