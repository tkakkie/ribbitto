package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestTopicConclusionMigration(t *testing.T) {
	t.Parallel()
	for _, preexisting := range []bool{false, true} {
		name := "absent shared index"
		if preexisting {
			name = "existing shared index"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			pool := pgtest.NewEmpty(t)
			migrator := pgtest.NewMigrator(t, pool)
			migrator.UpTo(ctx, 21)
			acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
			globex := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
			random := conversationtest.Channel(t, pool, acme.OrganizationID, "random", false)
			topic := conversationtest.Topic(t, pool, acme.OrganizationID, acme.Channel.ID, "design")
			insert := func(orgID, channelID, topicID, memberID kernel.ID, seq int64) kernel.ID {
				t.Helper()
				var id kernel.ID
				fixture(t, pool, `INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq)
					VALUES ($1, $2, $3, $4, 'conclusion', $5) RETURNING id`, []any{orgID, channelID, topicID, memberID, seq}, &id)
				return id
			}
			first := insert(acme.OrganizationID, acme.Channel.ID, topic.ID, acme.MemberID, 2)
			second := insert(acme.OrganizationID, acme.Channel.ID, topic.ID, acme.MemberID, 3)
			otherChannel := insert(acme.OrganizationID, random.ID, random.DefaultTopicID, acme.MemberID, 4)
			otherOrg := insert(globex.OrganizationID, globex.Channel.ID, globex.Channel.DefaultTopicID, globex.MemberID, 2)
			var originalIndex uint32
			if preexisting {
				_, err := pool.Exec(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS message_org_channel_id_key ON message (organization_id, channel_id, id);")
				requireNoError(t, err)
				requireNoError(t, pool.QueryRow(ctx, "SELECT 'message_org_channel_id_key'::regclass::oid").Scan(&originalIndex))
			}
			migrator.UpTo(ctx, 22)
			var total, unresolved int
			requireNoError(t, pool.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE conclusion_message_id IS NULL) FROM topic").Scan(&total, &unresolved))
			if total != 4 || unresolved != total {
				t.Fatalf("upgraded topics = %d, unresolved = %d; want 4, 4", total, unresolved)
			}
			var indexID uint32
			var indexes int
			requireNoError(t, pool.QueryRow(ctx, `SELECT 'message_org_channel_id_key'::regclass::oid,
				(SELECT count(*) FROM pg_index WHERE indrelid = 'message'::regclass AND indisunique
				AND indkey::text = (SELECT string_agg(attnum::text, ' ' ORDER BY position)
				FROM unnest(ARRAY['organization_id', 'channel_id', 'id']) WITH ORDINALITY AS wanted(name, position)
				JOIN pg_attribute ON attrelid = 'message'::regclass AND attname = wanted.name))`).Scan(&indexID, &indexes))
			if indexes != 1 || (preexisting && indexID != originalIndex) {
				t.Fatalf("shared index = %d (original %d), matching indexes = %d", indexID, originalIndex, indexes)
			}
			var deleteAction, updateAction string
			requireNoError(t, pool.QueryRow(ctx, `SELECT confdeltype::text, confupdtype::text FROM pg_constraint
				WHERE conrelid = 'topic'::regclass AND conname = 'topic_conclusion_message_fkey'`).Scan(&deleteAction, &updateAction))
			if deleteAction != "a" || updateAction != "a" {
				t.Fatalf("foreign key actions = delete %q, update %q; want NO ACTION", deleteAction, updateAction)
			}
			setConclusion := func(topicID kernel.ID, messageID any) error {
				_, err := pool.Exec(ctx, `UPDATE topic SET conclusion_message_id = $3
					WHERE organization_id = $1 AND channel_id = $2 AND id = $4`, acme.OrganizationID, acme.Channel.ID, messageID, topicID)
				return err
			}
			for _, tc := range []struct {
				name      string
				topic     kernel.ID
				message   kernel.ID
				code, key string
			}{
				{"other channel", topic.ID, otherChannel, "23503", "topic_conclusion_message_fkey"},
				{"other organization", topic.ID, otherOrg, "23503", "topic_conclusion_message_fkey"},
				{"unknown message", topic.ID, kernel.ID{1}, "23503", "topic_conclusion_message_fkey"},
				{"default topic", acme.Channel.DefaultTopicID, first, "23514", "topic_default_unresolved_check"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := setConclusion(tc.topic, tc.message); !isConstraint(err, tc.code, tc.key) {
						t.Fatalf("setting invalid conclusion: %v", err)
					}
				})
			}
			for _, message := range []any{first, second, nil, first} {
				requireNoError(t, setConclusion(topic.ID, message))
				var matches bool
				requireNoError(t, pool.QueryRow(ctx, "SELECT conclusion_message_id IS NOT DISTINCT FROM $2::uuid FROM topic WHERE id = $1", topic.ID, message).Scan(&matches))
				if !matches {
					t.Fatalf("conclusion did not round trip: %v", message)
				}
			}
			_, err := pool.Exec(ctx, "DELETE FROM message WHERE organization_id = $1 AND id = $2", acme.OrganizationID, first)
			if !isConstraint(err, "23503", "topic_conclusion_message_fkey") {
				t.Fatalf("deleting conclusion: %v", err)
			}
			// Another migration's dependency must survive this migration's Down.
			_, err = pool.Exec(ctx, `ALTER TABLE message ADD CONSTRAINT shared_index_dependency
				FOREIGN KEY (organization_id, channel_id, id) REFERENCES message (organization_id, channel_id, id)`)
			requireNoError(t, err)
			migrator.DownTo(ctx, 21)
			var column, constraints bool
			var remainingIndex uint32
			requireNoError(t, pool.QueryRow(ctx, `SELECT
				EXISTS (SELECT FROM pg_attribute WHERE attrelid = 'topic'::regclass AND attname = 'conclusion_message_id' AND NOT attisdropped),
				EXISTS (SELECT FROM pg_constraint WHERE conrelid = 'topic'::regclass
				AND conname IN ('topic_conclusion_message_fkey', 'topic_default_unresolved_check')),
				'message_org_channel_id_key'::regclass::oid`).Scan(&column, &constraints, &remainingIndex))
			if column || constraints || remainingIndex != indexID {
				t.Fatalf("after down: column %t, constraints %t, index %d; want false, false, %d", column, constraints, remainingIndex, indexID)
			}
			migrator.UpTo(ctx, 22)
			requireNoError(t, setConclusion(topic.ID, first))
		})
	}
}
