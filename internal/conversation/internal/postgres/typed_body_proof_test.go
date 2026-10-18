package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestTypedBodyProofMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := pgtest.NewEmpty(t)
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 18)
	f := conversationtest.OrganizationWithOwner(t, pool, "proof", "general")
	_, err := pool.Exec(ctx, "INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq) VALUES ($1, $2, $3, $4, 'legacy', 1)", f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	requireNoError(t, err)
	migrator.Up(ctx)
	var missing bool
	requireNoError(t, pool.QueryRow(ctx, "SELECT typed_body_proof IS NULL FROM message").Scan(&missing))
	if !missing {
		t.Fatal("migration invented proof for a legacy row")
	}
	for _, length := range []int{0, 35, 36, 37} {
		_, err := pool.Exec(ctx, "UPDATE message SET typed_body_proof = $1", make([]byte, length))
		var pgErr *pgconn.PgError
		if length == 36 && err != nil || length != 36 && (!errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "message_typed_body_proof_check") {
			t.Fatalf("proof length %d: %v", length, err)
		}
	}
	migrator.Down(ctx)
	var present bool
	requireNoError(t, pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM information_schema.columns WHERE table_name = 'message' AND column_name = 'typed_body_proof')").Scan(&present))
	if present {
		t.Fatal("down kept typed_body_proof")
	}
	migrator.Up(ctx)
	requireNoError(t, pool.QueryRow(ctx, "SELECT typed_body_proof IS NULL FROM message").Scan(&missing))
	if !missing {
		t.Fatal("up invented proof")
	}
	var body string
	requireNoError(t, pool.QueryRow(ctx, "SELECT body FROM message").Scan(&body))
	if body != "legacy" {
		t.Fatalf("down changed the body: %q", body)
	}
}
