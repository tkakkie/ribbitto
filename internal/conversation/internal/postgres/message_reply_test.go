package postgres_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestMessageReplyMigration(t *testing.T) {
	t.Parallel()
	pool := pgtest.NewEmpty(t)
	ctx := t.Context()
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 22)
	f := conversationtest.OrganizationWithOwner(t, pool, "reply", "general")
	other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
	channel := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	var indexBefore, indexAfter uint32
	requireNoError(t, pool.QueryRow(ctx, "SELECT 'message_org_channel_id_key'::regclass::oid").Scan(&indexBefore))
	migrator.Up(ctx)
	var indexes int
	requireNoError(t, pool.QueryRow(ctx, `SELECT 'message_org_channel_id_key'::regclass::oid,
		(SELECT count(*) FROM pg_index WHERE indrelid = 'message'::regclass AND indisunique
		AND indkey::text = (SELECT string_agg(attnum::text, ' ' ORDER BY position)
		FROM unnest(ARRAY['organization_id', 'channel_id', 'id']) WITH ORDINALITY AS wanted(name, position)
		JOIN pg_attribute ON attrelid = 'message'::regclass AND attname = wanted.name))`).Scan(&indexAfter, &indexes))
	if indexAfter != indexBefore || indexes != 1 {
		t.Fatalf("shared index changed or duplicated: %d -> %d, count %d", indexBefore, indexAfter, indexes)
	}
	insert := func(id, org, ch, topic, member kernel.ID, target, author any) error {
		_, err := pool.Exec(ctx, `INSERT INTO message
			(id, organization_id, channel_id, topic_id, member_id, body, event_seq, reply_to_message_id, reply_to_member_id)
			VALUES ($1,$2,$3,$4,$5,'reply',$8,$6,$7)`, id, org, ch, topic, member, target, author, int64(id[0])+10)
		return err
	}
	original, reply := kernel.ID{1}, kernel.ID{2}
	requireNoError(t, insert(original, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, nil, nil))
	for _, tc := range []struct {
		name, code, constraint string
		org, ch, topic, member kernel.ID
		target, author         any
	}{
		{"target alone", "23514", "message_reply_pair_check", f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, original, nil},
		{"author alone", "23514", "message_reply_pair_check", f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, nil, f.MemberID},
		{"self", "23514", "message_reply_self_check", f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, reply, f.MemberID},
		{"missing", "23503", "message_reply_message_fkey", f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, kernel.ID{9}, f.MemberID},
		{"foreign channel", "23503", "message_reply_message_fkey", f.OrganizationID, channel.ID, channel.DefaultTopicID, f.MemberID, original, f.MemberID},
		{"foreign organisation", "23503", "message_reply_message_fkey", other.OrganizationID, other.Channel.ID, other.Channel.DefaultTopicID, other.MemberID, original, other.MemberID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := insert(reply, tc.org, tc.ch, tc.topic, tc.member, tc.target, tc.author); !isConstraint(err, tc.code, tc.constraint) {
				t.Fatalf("invalid reply: %v", err)
			}
		})
	}
	_, err := pool.Exec(ctx, `INSERT INTO message (id,organization_id,channel_id,topic_id,member_id,body,event_seq,reply_to_message_id,reply_to_member_id)
		VALUES ($1,$2,$3,$4,$5,'reply',12,$6,$5)`, reply, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, original)
	requireNoError(t, err)
	for _, column := range []string{"reply_to_message_id", "reply_to_member_id"} {
		_, err := pool.Exec(ctx, "UPDATE message SET "+column+" = $3 WHERE organization_id=$1 AND id=$2", f.OrganizationID, reply, kernel.ID{9})
		if !isConstraint(err, "23514", "") {
			t.Fatalf("mutable %s: %v", column, err)
		}
	}
	for _, assignment := range []string{
		"reply_to_message_id = NULL",
		"reply_to_member_id = NULL",
		"reply_to_message_id = NULL, reply_to_member_id = NULL",
	} {
		t.Run(assignment, func(t *testing.T) {
			_, err := pool.Exec(ctx, "UPDATE message SET "+assignment+" WHERE organization_id=$1 AND id=$2", f.OrganizationID, reply)
			if !isConstraint(err, "23514", "") {
				t.Fatalf("erasing reply link: %v", err)
			}
		})
	}
	_, err = pool.Exec(ctx, "UPDATE message SET reply_to_message_id=$3, reply_to_member_id=$4 WHERE organization_id=$1 AND id=$2", f.OrganizationID, original, reply, f.MemberID)
	if !isConstraint(err, "23514", "") {
		t.Fatalf("adding a link later could create a cycle: %v", err)
	}
	_, err = pool.Exec(ctx, "UPDATE message SET reply_to_message_id=reply_to_message_id, reply_to_member_id=reply_to_member_id WHERE organization_id=$1", f.OrganizationID)
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM message WHERE organization_id=$1 AND id=$2", f.OrganizationID, original)
	if !isConstraint(err, "23001", "message_reply_message_fkey") {
		t.Fatalf("deleting answered message: %v", err)
	}
	migrator.DownTo(ctx, 22)
	requireNoError(t, pool.QueryRow(ctx, "SELECT 'message_org_channel_id_key'::regclass::oid").Scan(&indexAfter))
	if indexAfter != indexBefore {
		t.Fatal("Down changed shared index")
	}
	migrator.Up(ctx)
}
