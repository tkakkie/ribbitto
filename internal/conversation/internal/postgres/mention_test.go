package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestMentionMigrationAndConstraints(t *testing.T) {
	t.Parallel()
	pool := pgtest.NewEmpty(t)
	migrations := pgtest.NewMigrator(t, pool)
	migrations.UpTo(t.Context(), 20)
	f := newFixtures(t, pool)
	var messageID kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), `INSERT INTO message
		(organization_id, channel_id, topic_id, member_id, body, event_seq)
		VALUES ($1, $2, $3, $4, '<@00000000-0000-0000-0000-000000000001>', 1) RETURNING id`,
		f.acme, f.general, f.generalTopic, f.alice).Scan(&messageID))
	migrations.Up(t.Context())
	conversationtest.AssertMentions(t, pool, f.acme, messageID, 1, nil)
	var columns, messageKeys int
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'message_mention' AND column_name = 'channel_id'`).Scan(&columns))
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conrelid = 'message'::regclass AND contype = 'u'`).Scan(&messageKeys))
	if columns != 0 || messageKeys != 2 {
		t.Fatalf("channel columns=%d, message unique constraints=%d; want 0, 2", columns, messageKeys)
	}
	for _, tt := range []struct {
		name, table, deletion string
	}{
		{"message_mention_message_fkey", "message", "c"},
		{"message_mention_member_fkey", "member", "r"},
	} {
		var target, deletion, definition string
		requireNoError(t, pool.QueryRow(t.Context(), `SELECT confrelid::regclass::text, confdeltype::text, pg_get_constraintdef(oid)
			FROM pg_constraint WHERE conrelid = 'message_mention'::regclass AND conname = $1`, tt.name).Scan(&target, &deletion, &definition))
		if target != tt.table || deletion != tt.deletion {
			t.Fatalf("%s: %s delete=%s (%s)", tt.name, target, deletion, definition)
		}
	}
	insert := func(organizationID, messageID, memberID kernel.ID, seq int64) error {
		_, err := pool.Exec(t.Context(), `INSERT INTO message_mention (organization_id, message_id, member_id, event_seq) VALUES ($1, $2, $3, $4)`, organizationID, messageID, memberID, seq)
		return err
	}
	for _, tt := range []struct {
		name, code, constraint string
		org, message, member   kernel.ID
		seq                    int64
	}{
		{"foreign member", "23503", "message_mention_member_fkey", f.acme, messageID, f.bob, 1},
		{"foreign message", "23503", "message_mention_message_fkey", f.globex, messageID, f.bob, 1},
		{"nonpositive sequence", "23514", "message_mention_event_seq_check", f.acme, messageID, f.alice, 0},
	} {
		err := insert(tt.org, tt.message, tt.member, tt.seq)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != tt.code || pgErr.ConstraintName != tt.constraint {
			t.Fatalf("%s: %v", tt.name, err)
		}
	}
	var foreignMessage kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), `INSERT INTO message
		(organization_id, channel_id, topic_id, member_id, body, event_seq)
		VALUES ($1, $2, $3, $4, 'foreign', 1) RETURNING id`, f.globex, f.foreign, f.foreignTopic, f.bob).Scan(&foreignMessage))
	// A second member in acme is only mentioned, never the message's author.
	mentioned := orgtest.Member(t, pool, f.acme, identitytest.Account(t, pool, "mentioned@example.org", "Mentioned"), org.RoleMember, "mentioned", 1)
	requireNoError(t, insert(f.acme, messageID, mentioned, 1))
	conversationtest.AssertMentions(t, pool, f.acme, messageID, 1, []kernel.ID{mentioned})
	var pgErr *pgconn.PgError
	if err := insert(f.acme, messageID, mentioned, 1); !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "message_mention_pkey" {
		t.Fatalf("duplicate mention: %v", err)
	}
	_, err := pool.Exec(t.Context(), "DELETE FROM member WHERE organization_id = $1 AND id = $2", f.acme, mentioned)
	if !errors.As(err, &pgErr) || pgErr.Code != "23001" || pgErr.ConstraintName != "message_mention_member_fkey" {
		t.Fatalf("mentioned member deletion: %v", err)
	}
	_, err = pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id = $1 AND id = $2", f.acme, messageID)
	requireNoError(t, err)
	conversationtest.AssertMentions(t, pool, f.acme, messageID, 1, nil)
	_, err = pool.Exec(t.Context(), "DELETE FROM member WHERE organization_id = $1 AND id = $2", f.acme, mentioned)
	requireNoError(t, err)
	migrations.Down(t.Context())
	var remaining int
	requireNoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM message WHERE organization_id = $1 AND id = $2", f.globex, foreignMessage).Scan(&remaining))
	if remaining != 1 {
		t.Fatal("migration down changed existing messages")
	}
}

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
		conversationtest.AssertMentions(t, pool, f.acme, messageID, 1, nil)
	}
}
