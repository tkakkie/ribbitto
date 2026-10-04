package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

var writerIn conversation.WriterIn = conversationpg.WriterIn

func fixture(t *testing.T, pool *pgxpool.Pool, sql string, args []any, dest ...any) {
	t.Helper()
	requireNoError(t, pool.QueryRow(t.Context(), sql, args...).Scan(dest...))
}

// channelSQL inserts a channel and its default topic in one statement: the
// channel's foreign key to its topic is deferred to commit.
const channelSQL = `WITH channel AS (INSERT INTO channel (organization_id, name) VALUES ($1, $2) RETURNING organization_id, id, default_topic_id),
	topic AS (INSERT INTO topic (organization_id, channel_id, id, is_default) SELECT organization_id, id, default_topic_id, true FROM channel)
	SELECT id, default_topic_id FROM channel`

// fixtures is acme with alice and a channel, and globex with bob and one.
type fixtures struct {
	acme, globex, alice, bob, general, foreign kernel.ID
	generalTopic, foreignTopic                 kernel.ID // the channels' default topics
}

func newFixtures(t *testing.T, pool *pgxpool.Pool) (f fixtures) {
	t.Helper()
	f.acme, f.globex = fixtureOrganization(t, pool, "acme"), fixtureOrganization(t, pool, "globex")
	member := `WITH account AS (INSERT INTO account (email, display_name, password_hash) VALUES ($2::text || '@example.org', $2, '$argon2id$x') RETURNING id)
		INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) SELECT $1, id, 'member', 1, $2 FROM account RETURNING id`
	fixture(t, pool, member, []any{f.acme, "alice"}, &f.alice)
	fixture(t, pool, member, []any{f.globex, "bob"}, &f.bob)
	fixture(t, pool, channelSQL, []any{f.acme, "general"}, &f.general, &f.generalTopic)
	fixture(t, pool, channelSQL, []any{f.globex, "general"}, &f.foreign, &f.foreignTopic)
	return f
}

// messages counts the rows posting's insert adds.
func messages(t *testing.T, pool *pgxpool.Pool) (n int) {
	t.Helper()
	fixture(t, pool, "SELECT count(*) FROM message", nil, &n)
	return n
}

// Each method runs on the caller's transaction: it sees that transaction's
// rows, and what it writes is visible there and gone after the runner rolls
// back, which returns fn's error as is.
func TestWriterIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	rollback := errors.New("caller rolls back")
	err := conversationpg.NewTxRunner(pool).InTx(ctx, func(tx platform.Tx) error {
		writer, q := writerIn(tx), pgxbridge.Tx(tx)
		// Uncommitted rows, so only a read on the caller's transaction finds them.
		var random, randomTopic kernel.ID
		requireNoError(t, q.QueryRow(ctx, channelSQL, f.acme, "random").Scan(&random, &randomTopic))
		if got, err := writer.GetDefaultTopic(ctx, f.acme, random); err != nil || got.ID != randomTopic || !got.IsDefault || got.ChannelID != random || got.OrganizationID != f.acme {
			t.Fatalf("GetDefaultTopic = %+v, %v; want %v", got, err, randomTopic)
		}
		var planning kernel.ID
		requireNoError(t, q.QueryRow(ctx, "INSERT INTO topic (organization_id, channel_id, name, is_default) VALUES ($1, $2, 'Planning', false) RETURNING id", f.acme, f.general).Scan(&planning))
		if got, err := writer.GetTopic(ctx, f.acme, f.general, planning); err != nil || got.ID != planning || got.Name != "Planning" || got.IsDefault {
			t.Fatalf("GetTopic = %+v, %v; want Planning", got, err)
		}
		posted, err := writer.InsertMessage(ctx, f.acme, f.general, planning, f.alice, "hello", 1)
		if err != nil || posted.ID == (kernel.ID{}) || posted.OrganizationID != f.acme || posted.ChannelID != f.general || posted.TopicID != planning ||
			posted.MemberID != f.alice || posted.Body != "hello" || posted.EventSeq != 1 || posted.CreatedAt.IsZero() {
			t.Fatalf("InsertMessage = %+v, %v", posted, err)
		}
		var body string
		requireNoError(t, q.QueryRow(ctx, "SELECT body FROM message WHERE id = $1 AND topic_id = $2", posted.ID, planning).Scan(&body))
		if body != "hello" {
			t.Fatalf("message in the caller's transaction = %q", body)
		}
		return rollback
	})
	if err != rollback {
		t.Fatalf("caller rollback: %v, want it as is", err)
	}
	var topics int
	if fixture(t, pool, "SELECT count(*) FROM topic WHERE name = 'Planning' OR channel_id NOT IN ($1, $2)", []any{f.general, f.foreign}, &topics); topics != 0 || messages(t, pool) != 0 {
		t.Fatalf("after rollback: %d new topics, %d messages; want none", topics, messages(t, pool))
	}
}

func TestWriterErrors(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	insert := func(channelID, topicID, memberID kernel.ID, body string) func(conversation.Writer) error {
		return func(w conversation.Writer) error {
			_, err := w.InsertMessage(ctx, f.acme, channelID, topicID, memberID, body, 1)
			return err
		}
	}
	for _, tc := range []struct {
		name       string
		write      func(conversation.Writer) error
		want       error  // nil: untranslated, a wrapped PostgreSQL error
		constraint string // the PostgreSQL error the result keeps, if any
	}{
		{"no default topic", func(w conversation.Writer) error { _, err := w.GetDefaultTopic(ctx, f.acme, f.foreign); return err }, conversation.ErrTopicNotFound, ""},
		{"message_organization_id_channel_id_fkey", insert(f.foreign, f.foreignTopic, f.alice, "hello"), conversation.ErrChannelNotFound, ""},
		{"message_organization_id_member_id_fkey", insert(f.general, f.generalTopic, f.bob, "hello"), org.ErrNotFound, ""},
		{"body CHECK", insert(f.general, f.generalTopic, f.alice, " hello"), nil, "message_body_check2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := conversationpg.NewTxRunner(pool).InTx(ctx, func(tx platform.Tx) error { return tc.write(writerIn(tx)) })
			var pgErr *pgconn.PgError
			if tc.want != nil && !errors.Is(err, tc.want) || tc.constraint != "" && (!errors.As(err, &pgErr) || pgErr.ConstraintName != tc.constraint) {
				t.Fatalf("error = %v, want %v with the PostgreSQL error of %q", err, tc.want, tc.constraint)
			}
			if tc.want == nil && err == error(pgErr) {
				t.Fatalf("error = %v, want it wrapped, not the bare PostgreSQL error", err)
			}
			// An untranslated failure must not become an error web answers with 404.
			for _, mapped := range []error{conversation.ErrChannelNotFound, org.ErrNotFound, conversation.ErrTopicNotFound} {
				if tc.want == nil && errors.Is(err, mapped) {
					t.Fatalf("error = %v, mapped to %v", err, mapped)
				}
			}
			if n := messages(t, pool); n != 0 {
				t.Fatalf("%d messages after the failed write, want 0", n)
			}
		})
	}
}

// The runner commits on nil, and a failed commit, here event_log's deferred
// gap check, comes back through it as is.
func TestTxRunnerCommits(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	runner := conversationpg.NewTxRunner(pool)
	requireNoError(t, runner.InTx(ctx, func(tx platform.Tx) error {
		_, err := writerIn(tx).InsertMessage(ctx, f.acme, f.general, f.generalTopic, f.alice, "hello", 1)
		return err
	}))
	if n := messages(t, pool); n != 1 {
		t.Fatalf("%d messages after commit, want 1", n)
	}
	err := runner.InTx(ctx, func(tx platform.Tx) error {
		// The update succeeds; only the deferred trigger at commit refuses it.
		tag, err := pgxbridge.Tx(tx).Exec(ctx, "UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1", f.acme)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("update in the callback = %v, %v; want one row", tag, err)
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || err != error(pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.Message, "without event_log rows") {
		t.Fatalf("commit without an event = %v, want the gap check's error as is", err)
	}
	var seq int64
	if fixture(t, pool, "SELECT event_seq FROM organization WHERE id = $1", []any{f.acme}, &seq); seq != 0 {
		t.Fatalf("event_seq = %d after the failed commit, want 0", seq)
	}
}
