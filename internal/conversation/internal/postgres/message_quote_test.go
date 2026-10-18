package postgres_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// The quote columns hold only decision 38's three states, under the body's
// checks and organisation-scoped keys; deleting the original nulls only its ID.
func TestMessageQuoteSchema(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	globex := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
	writer, reads := postgres.NewWriterForTest(pool), postgres.NewReadStoreForTest(pool)
	insert := func(f conversationtest.OrganizationFixture, seq int64, quote *conversation.Quote) (conversation.Message, error) {
		return writer.InsertMessage(ctx, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, "body", seq, nil, conversation.TypedBodyProof{}, quote)
	}
	original, err := insert(acme, 10, nil)
	requireNoError(t, err)
	foreign, err := insert(globex, 10, nil)
	requireNoError(t, err)
	quote := &conversation.Quote{MessageID: &original.ID, MemberID: acme.MemberID, Text: "body"}
	quoting, err := insert(acme, 11, quote)
	if err != nil || !reflect.DeepEqual(quoting.Quote, quote) {
		t.Fatalf("quote round trip: %+v, %v", quoting.Quote, err)
	}
	// Another organisation's original fails its key, as one deleted after
	// posting's read would.
	if _, err := insert(acme, 12, &conversation.Quote{MessageID: &foreign.ID, MemberID: acme.MemberID, Text: "body"}); !errors.Is(err, conversation.ErrQuoteNotFound) {
		t.Fatalf("another organisation's original: %v", err)
	}
	for _, tc := range []struct{ name, set, code string }{
		{"no quote", "quoted_message_id = NULL, quoted_member_id = NULL, quote_text = NULL", ""},
		{"original deleted", "quoted_message_id = NULL", ""},
		{"message ID alone", "quoted_member_id = NULL, quote_text = NULL", "23514"},
		{"author without text", "quote_text = NULL", "23514"},
		{"text without author", "quoted_message_id = NULL, quoted_member_id = NULL", "23514"},
		{"empty text", "quote_text = ''", "23514"},
		{"long text", "quote_text = repeat('界', 4001)", "23514"},
		{"untrimmed text", "quote_text = 'body '", "23514"},
		{"override character", "quote_text = 'a' || U&'\\202E' || 'b'", "23514"},
		{"another organisation's message", "quoted_message_id = $2", "23503"},
		{"another organisation's member", "quoted_member_id = $3", "23503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			requireNoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = tx.Exec(ctx, "WITH fixture AS (SELECT $2::uuid, $3::uuid) UPDATE message SET "+tc.set+" WHERE id = $1", quoting.ID, foreign.ID, globex.MemberID)
			var pgErr *pgconn.PgError
			if tc.code == "" && err != nil || tc.code != "" && (!errors.As(err, &pgErr) || pgErr.Code != tc.code) {
				t.Fatalf("want SQLSTATE %q, got %v", tc.code, err)
			}
		})
	}
	if _, err := pool.Exec(ctx, "DELETE FROM message WHERE id = $1", original.ID); err != nil {
		t.Fatal(err)
	}
	got, err := reads.GetMessages(ctx, acme.OrganizationID, acme.Channel.ID, []kernel.ID{quoting.ID})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Quote, &conversation.Quote{MemberID: acme.MemberID, Text: "body"}) {
		t.Fatalf("after deleting the original: %+v, %v", got, err)
	}
}
