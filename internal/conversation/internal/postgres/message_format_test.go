package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// Messages from before migration 00016 become plain_text, an insert that
// names no format is plain_text, and the CHECK refuses NULL and unknown
// formats (decisions 18 and 39).
func TestMessageFormatMigration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := pgtest.NewEmpty(t)
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 15)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	insert := `INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := pool.Exec(ctx, insert, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, "**old**", 2); err != nil {
		t.Fatal(err)
	}
	migrator.UpTo(ctx, 16)
	if _, err := pool.Exec(ctx, insert, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, "**new**", 3); err != nil {
		t.Fatal(err)
	}
	var plain int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE format = 'plain_text'").Scan(&plain); err != nil || plain != 2 {
		t.Fatalf("%d plain_text rows, %v; want the old and the unnamed one", plain, err)
	}
	for format, code := range map[any]string{nil: "23502", "markdown": "23514", "chat_v1": ""} {
		_, err := pool.Exec(ctx, "UPDATE message SET format = $1 WHERE event_seq = 3", format)
		var pgErr *pgconn.PgError
		if code == "" && err != nil || code != "" && (!errors.As(err, &pgErr) || pgErr.Code != code) {
			t.Errorf("format %v: %v, want %q", format, err, code)
		}
	}
}
