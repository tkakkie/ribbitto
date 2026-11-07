package postgres_test

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestReplyIndexMigration(t *testing.T) {
	t.Parallel()
	pool := pgtest.NewEmpty(t)
	ctx := t.Context()
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 23)
	var before, after uint32
	requireNoError(t, pool.QueryRow(ctx, "SELECT 'message_org_channel_id_key'::regclass::oid").Scan(&before))
	migrator.UpTo(ctx, 24)
	var definition, predicate string
	requireNoError(t, pool.QueryRow(ctx, `SELECT pg_get_indexdef(indexrelid), pg_get_expr(indpred, indrelid)
 FROM pg_index WHERE indexrelid = 'message_replies_idx'::regclass AND indrelid = 'message'::regclass`).Scan(&definition, &predicate))
	if !strings.Contains(definition, "(organization_id, reply_to_member_id, event_seq DESC, channel_id)") || predicate != "(reply_to_member_id IS NOT NULL)" {
		t.Fatalf("unexpected reply index: %s WHERE %s", definition, predicate)
	}
	migrator.Down(ctx)
	var removed bool
	requireNoError(t, pool.QueryRow(ctx, `SELECT to_regclass('message_replies_idx') IS NULL,
 'message_org_channel_id_key'::regclass::oid`).Scan(&removed, &after))
	if !removed || before != after {
		t.Fatal("Down did not remove only the reply index")
	}
	var linkColumns int
	requireNoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
 WHERE table_name = 'message' AND column_name IN ('reply_to_message_id', 'reply_to_member_id')`).Scan(&linkColumns))
	if linkColumns != 2 {
		t.Fatal("Down removed reply storage")
	}
	migrator.UpTo(ctx, 24)
}
