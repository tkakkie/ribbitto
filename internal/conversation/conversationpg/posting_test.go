package conversationpg_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
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

// completeSetup runs org's setup with conversation's default-channel creator.
func completeSetup(t *testing.T, pool *pgxpool.Pool) (org.SetupResult, error) {
	t.Helper()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	return orgpg.NewSetup(pool, hasher, "secret",
		func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) },
		func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) },
		func(tx platform.Tx) org.DefaultChannelCreator { return conversationpg.DefaultChannelCreatorIn(tx) },
	).Complete(t.Context(), "secret", org.SetupInput{OrganizationName: "Example", Slug: "example", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"})
}

func TestPostingRollback(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	result, err := completeSetup(t, pool)
	requireNoError(t, err)
	var channelID, memberID kernel.ID
	requireNoError(t, pool.QueryRow(ctx, `SELECT c.id, m.id FROM channel c JOIN member m USING (organization_id)
		WHERE c.organization_id = $1`, result.OrganizationID).Scan(&channelID, &memberID))
	// Fail after the event insert, so the message, sequence and log must all roll back.
	_, err = pool.Exec(ctx, `CREATE FUNCTION refuse_event() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse_event AFTER INSERT ON event_log FOR EACH ROW EXECUTE FUNCTION refuse_event()`)
	requireNoError(t, err)
	if _, err := newPosting(pool).Post(ctx, membership(result.OrganizationID, memberID), channelID, "rolled back"); err == nil {
		t.Fatal("post succeeded despite event failure")
	}
	assertEventLog(t, pool, result.OrganizationID, 1)
	var messages int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE organization_id = $1", result.OrganizationID).Scan(&messages))
	if messages != 0 {
		t.Fatalf("%d messages survived rollback", messages)
	}
}
