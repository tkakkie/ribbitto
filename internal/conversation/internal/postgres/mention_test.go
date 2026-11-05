package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestInsertMentionsMapsOnlyMemberForeignKey(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := newFixtures(t, pool)
	var messageID kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), `INSERT INTO message
		(organization_id, channel_id, topic_id, member_id, body, event_seq)
		VALUES ($1, $2, $3, $4, 'message', 1) RETURNING id`, f.acme, f.general, f.generalTopic, f.alice).Scan(&messageID))
	for _, tt := range []struct {
		name    string
		message kernel.ID
		members []kernel.ID
		invalid bool
	}{
		{"unknown member", messageID, []kernel.ID{{0xee}}, true},
		{"foreign member", messageID, []kernel.ID{f.bob}, true},
		{"unknown message", kernel.ID{0xee}, []kernel.ID{f.alice}, false},
	} {
		err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return postgres.WriterIn(tx).InsertMentions(t.Context(), f.acme, tt.message, 1, tt.members)
		})
		if err == nil || errors.Is(err, conversation.ErrInvalidBody) != tt.invalid || errors.Is(err, conversation.ErrUnknownReference) != tt.invalid {
			t.Fatalf("%s: %v, want invalid=%t", tt.name, err, tt.invalid)
		}
		assertMentionMembers(t, pool, f.acme, messageID, nil)
	}
}

func assertMentionMembers(t *testing.T, pool *pgxpool.Pool, organizationID, messageID kernel.ID, members []kernel.ID) {
	t.Helper()
	var total, matched int
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE member_id = ANY($3::uuid[]))
		FROM message_mention WHERE organization_id = $1 AND message_id = $2`, organizationID, messageID, members).Scan(&total, &matched))
	if total != len(members) || matched != len(members) {
		t.Fatalf("mentions=%d matched=%d, want %d", total, matched, len(members))
	}
}
