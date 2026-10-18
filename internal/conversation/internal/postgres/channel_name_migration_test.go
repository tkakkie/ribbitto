package postgres_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestChannelNameMigration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := pgtest.NewEmpty(t)
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 19)
	organizationID := orgtest.Organization(t, pool, "acme", "Acme", 0)
	first := conversationtest.Channel(t, pool, organizationID, "Announcements", true)
	second := conversationtest.Channel(t, pool, organizationID, "announcements", false)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	err := platform.Migrate(ctx, db, "up", io.Discard)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "channel_name_idx" || !strings.Contains(pgErr.Detail, "announcements") {
		t.Fatalf("upgrade error = %v, want unique violation naming the duplicate key", err)
	}
	assertSchema := func(oldConstraint, newIndex bool, version int) {
		t.Helper()
		var old, index bool
		var gotVersion int
		requireNoError(t, pool.QueryRow(ctx, `SELECT
			EXISTS (SELECT FROM pg_constraint WHERE conrelid = 'channel'::regclass AND conname = 'channel_organization_id_name_key'),
			to_regclass('channel_name_idx') IS NOT NULL,
			(SELECT max(version_id) FROM goose_db_version WHERE is_applied)`).Scan(&old, &index, &gotVersion))
		if old != oldConstraint || index != newIndex || gotVersion != version {
			t.Fatalf("schema = old constraint %t, new index %t, version %d; want %t, %t, %d", old, index, gotVersion, oldConstraint, newIndex, version)
		}
	}
	assertNames := func(secondName string) {
		t.Helper()
		for id, want := range map[kernel.ID]string{first.ID: "Announcements", second.ID: secondName} {
			var name string
			requireNoError(t, pool.QueryRow(ctx, "SELECT name FROM channel WHERE organization_id = $1 AND id = $2", organizationID, id).Scan(&name))
			if name != want {
				t.Fatalf("channel %v name = %q, want %q", id, name, want)
			}
		}
		var count int
		requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM channel WHERE organization_id = $1", organizationID).Scan(&count))
		if count != 2 {
			t.Fatalf("channel count = %d, want 2", count)
		}
	}
	assertSchema(true, false, 19)
	assertNames("announcements")
	// The rolled-back constraint must still reject exact duplicates.
	_, err = pool.Exec(ctx, "UPDATE channel SET name = 'Announcements' WHERE organization_id = $1 AND id = $2", organizationID, second.ID)
	if !isConstraint(err, "23505", "channel_organization_id_name_key") {
		t.Fatalf("old constraint after rollback: %v", err)
	}
	// Only an operator's explicit rename resolves the existing case-only twin.
	_, err = pool.Exec(ctx, "UPDATE channel SET name = 'News' WHERE organization_id = $1 AND id = $2", organizationID, second.ID)
	requireNoError(t, err)
	migrator.UpTo(ctx, 20)
	assertSchema(false, true, 20)
	assertNames("News")
	migrator.DownTo(ctx, 19)
	assertSchema(true, false, 19)
	assertNames("News")
	_, err = pool.Exec(ctx, "UPDATE channel SET name = 'Announcements' WHERE organization_id = $1 AND id = $2", organizationID, second.ID)
	if !isConstraint(err, "23505", "channel_organization_id_name_key") {
		t.Fatalf("restored constraint: %v", err)
	}
	_, err = pool.Exec(ctx, "UPDATE channel SET name = 'announcements' WHERE organization_id = $1 AND id = $2", organizationID, second.ID)
	requireNoError(t, err)
	assertNames("announcements")
}
