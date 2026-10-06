package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

var writerIn conversation.WriterIn = func(tx platform.Tx) conversation.Writer { return postgres.WriterIn(tx) }

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
	f.acme, f.globex = orgtest.Organization(t, pool, "acme", "acme", 0), orgtest.Organization(t, pool, "globex", "globex", 0)
	alice := identitytest.Account(t, pool, "alice@example.org", "alice")
	bob := identitytest.Account(t, pool, "bob@example.org", "bob")
	f.alice = orgtest.Member(t, pool, f.acme, alice, org.RoleMember, "alice", 1)
	f.bob = orgtest.Member(t, pool, f.globex, bob, org.RoleMember, "bob", 1)
	general := conversationtest.Channel(t, pool, f.acme, "general", false)
	foreign := conversationtest.Channel(t, pool, f.globex, "general", false)
	f.general, f.generalTopic = general.ID, general.DefaultTopicID
	f.foreign, f.foreignTopic = foreign.ID, foreign.DefaultTopicID
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
	err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
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
	notice := func(channelID, topicID, memberID kernel.ID, body string) func(conversation.Writer) error {
		return func(w conversation.Writer) error {
			_, err := w.InsertNotice(ctx, f.acme, channelID, topicID, memberID, body, 1)
			return err
		}
	}
	createTopic := func(name string) func(conversation.Writer) error {
		return func(w conversation.Writer) error { _, err := w.CreateTopic(ctx, f.acme, f.general, name); return err }
	}
	conversationtest.Topic(t, pool, f.acme, f.general, "Planning")
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
		{"topic_name_idx", createTopic("planning"), conversation.ErrTopicNameTaken, ""},
		{"topic_name_check", createTopic(strings.Repeat("a", 81)), conversation.ErrInvalidTopicName, "topic_name_check"},
		// The notice maps nothing (R2 on #502), its foreign keys included.
		{"notice's channel key", notice(f.foreign, f.foreignTopic, f.alice, "hello"), nil, "message_organization_id_channel_id_fkey"},
		{"notice's member key", notice(f.general, f.generalTopic, f.bob, "hello"), nil, "message_organization_id_member_id_fkey"},
		{"notice's body CHECK", notice(f.general, f.generalTopic, f.alice, " hello"), nil, "message_body_check2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.InTx(ctx, pool, func(tx platform.Tx) error { return tc.write(writerIn(tx)) })
			var pgErr *pgconn.PgError
			if tc.want != nil && !errors.Is(err, tc.want) || tc.constraint != "" && (!errors.As(err, &pgErr) || pgErr.ConstraintName != tc.constraint) {
				t.Fatalf("error = %v, want %v with the PostgreSQL error of %q", err, tc.want, tc.constraint)
			}
			if tc.want == nil && err == error(pgErr) {
				t.Fatalf("error = %v, want it wrapped, not the bare PostgreSQL error", err)
			}
			// An untranslated failure must not become an error web answers with 404, 409 or 422.
			for _, mapped := range []error{conversation.ErrChannelNotFound, org.ErrNotFound, conversation.ErrTopicNotFound, conversation.ErrTopicNameTaken, conversation.ErrInvalidTopicName} {
				if tc.want == nil && errors.Is(err, mapped) {
					t.Fatalf("error = %v, mapped to %v", err, mapped)
				}
			}
			var topics int
			if fixture(t, pool, "SELECT count(*) FROM topic WHERE NOT is_default", nil, &topics); topics != 1 || messages(t, pool) != 0 {
				t.Fatalf("%d named topics and %d messages after the failed write, want only Planning", topics, messages(t, pool))
			}
		})
	}
}

// Branching's writes run on the caller's transaction: the new topic, the
// notice and the move are visible there and gone after the runner rolls back.
func TestWriterBranchesIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	rollback := errors.New("caller rolls back")
	err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
		writer := writerIn(tx)
		planning, err := writer.CreateTopic(ctx, f.acme, f.general, "Planning")
		if err != nil || planning.OrganizationID != f.acme || planning.ChannelID != f.general || planning.Name != "Planning" || planning.IsDefault || planning.CreatedAt.IsZero() {
			t.Fatalf("CreateTopic = %+v, %v", planning, err)
		}
		if got, err := writer.GetTopic(ctx, f.acme, f.general, planning.ID); err != nil || got != planning {
			t.Fatalf("GetTopic = %+v, %v; want %+v", got, err, planning)
		}
		posted, err := writer.InsertNotice(ctx, f.acme, f.general, f.generalTopic, f.alice, "moved", 1)
		if err != nil || posted.OrganizationID != f.acme || posted.ChannelID != f.general || posted.TopicID != f.generalTopic ||
			posted.MemberID != f.alice || posted.Body != "moved" || posted.EventSeq != 1 || posted.CreatedAt.IsZero() {
			t.Fatalf("InsertNotice = %+v, %v", posted, err)
		}
		if moved, err := writer.MoveMessages(ctx, f.acme, f.general, f.generalTopic, planning.ID, []kernel.ID{posted.ID}); err != nil || moved != 1 {
			t.Fatalf("MoveMessages = %d, %v; want 1", moved, err)
		}
		var topic kernel.ID
		requireNoError(t, pgxbridge.Tx(tx).QueryRow(ctx, "SELECT topic_id FROM message WHERE id = $1", posted.ID).Scan(&topic))
		if topic != planning.ID {
			t.Fatalf("message in the caller's transaction is in topic %v, want %v", topic, planning.ID)
		}
		return rollback
	})
	if err != rollback {
		t.Fatalf("caller rollback: %v, want it as is", err)
	}
	var topics int
	if fixture(t, pool, "SELECT count(*) FROM topic WHERE NOT is_default", nil, &topics); topics != 0 || messages(t, pool) != 0 {
		t.Fatalf("after rollback: %d named topics, %d messages; want none", topics, messages(t, pool))
	}
}

// MoveMessages moves only the selected messages still in the source topic of
// the organisation's channel, and counts only those.
func TestWriterMoveMessages(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	planning := conversationtest.Topic(t, pool, f.acme, f.general, "Planning").ID
	randomChannel := conversationtest.Channel(t, pool, f.acme, "random", false)
	random, randomTopic := randomChannel.ID, randomChannel.DefaultTopicID
	before, want := map[kernel.ID]kernel.ID{}, map[kernel.ID]kernel.ID{}
	selected := []kernel.ID{{0xee}} // unknown
	for i, m := range []struct{ organization, channel, topic, member, after kernel.ID }{
		{f.acme, f.general, f.generalTopic, f.alice, planning},       // the only one that moves
		{f.acme, f.general, f.generalTopic, f.alice, f.generalTopic}, // not selected
		{f.acme, f.general, planning, f.alice, planning},             // already moved
		{f.acme, random, randomTopic, f.alice, randomTopic},          // another channel
		{f.globex, f.foreign, f.foreignTopic, f.bob, f.foreignTopic}, // another organisation
	} {
		var id kernel.ID
		fixture(t, pool, "INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq) VALUES ($1, $2, $3, $4, 'hello', $5) RETURNING id", []any{m.organization, m.channel, m.topic, m.member, i + 1}, &id)
		before[id], want[id] = m.topic, m.after
		if i != 1 {
			selected = append(selected, id)
		}
	}
	move := func(organizationID, channelID kernel.ID) (moved int64) {
		requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error {
			var err error
			moved, err = writerIn(tx).MoveMessages(ctx, organizationID, channelID, f.generalTopic, planning, selected)
			return err
		}))
		return moved
	}
	topicsAre := func(want map[kernel.ID]kernel.ID, when string) {
		t.Helper()
		for id, topic := range want {
			var got kernel.ID
			if fixture(t, pool, "SELECT topic_id FROM message WHERE id = $1", []any{id}, &got); got != topic {
				t.Fatalf("%s: message %v is in topic %v, want %v", when, id, got, topic)
			}
		}
	}
	// The source topic matches, so only the organisation or the channel
	// predicate can keep its selected message in place.
	for _, scope := range []struct {
		name                  string
		organization, channel kernel.ID
	}{{"another organisation", f.globex, f.general}, {"another channel", f.acme, random}} {
		if moved := move(scope.organization, scope.channel); moved != 0 {
			t.Fatalf("%s: moved = %d, want 0", scope.name, moved)
		}
		topicsAre(before, scope.name)
	}
	if moved := move(f.acme, f.general); moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}
	topicsAre(want, "after the move")
}
