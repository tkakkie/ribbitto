package postgres_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestListRepliesReadsOneLookahead(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := newFixtures(t, pool)
	author := orgtest.Member(t, pool, f.acme, identitytest.Account(t, pool, "replier@example.org", "Replier"), org.RoleMember, "replier", 1)
	var target kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), `INSERT INTO message
		(organization_id, channel_id, topic_id, member_id, body, event_seq)
		VALUES ($1, $2, $3, $4, 'target', 1) RETURNING id`,
		f.acme, f.general, f.generalTopic, f.alice).Scan(&target))
	inserted, err := pool.Exec(t.Context(), `INSERT INTO message
		(organization_id, channel_id, topic_id, member_id, body, event_seq, reply_to_message_id, reply_to_member_id)
		SELECT $1, $2, $3, $4, 'reply', seq, $5, $6 FROM generate_series(2, 102) AS seq`,
		f.acme, f.general, f.generalTopic, author, target, f.alice)
	requireNoError(t, err)
	if inserted.RowsAffected() != 101 {
		t.Fatalf("inserted %d replies, want 101", inserted.RowsAffected())
	}
	err = platform.InSnapshot(t.Context(), pool, func(snapshot platform.Snapshot) error {
		messages, err := postgres.ReadStoreIn(snapshot).ListReplies(t.Context(), f.acme, f.alice, []kernel.ID{f.general}, nil)
		if err != nil {
			return err
		}
		if len(messages) != conversation.PageSize+1 {
			t.Fatalf("raw store read %d rows, want PageSize+1 (%d)", len(messages), conversation.PageSize+1)
		}
		return nil
	})
	requireNoError(t, err)
}
