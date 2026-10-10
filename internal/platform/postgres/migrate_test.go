package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestMigrations(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	// Raw SQL probes evolving schemas and slug constraints without current-schema fixtures.
	pool := pgtest.NewEmpty(t)
	var empty bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.organization') IS NULL AND to_regclass('public.goose_db_version') IS NULL").Scan(&empty); err != nil || !empty {
		t.Fatalf("expected an unmigrated database: empty=%t, error=%v", empty, err)
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, step := range []struct {
		command  string
		present  bool
		state    string
		accounts bool
		setup    bool
		handle   bool
		messages bool
		topics   bool
		unread   bool
	}{
		{"up", true, "applied", true, true, true, true, true, true},
		{"down", true, "applied", true, true, true, true, true, false},
		{"down", true, "applied", true, true, true, true, true, false},
		{"down", true, "applied", true, true, true, true, true, false},
		{"down", true, "applied", true, true, true, true, true, false},
		{"down", true, "applied", true, true, true, true, false, false},
		{"down", true, "applied", true, true, true, true, false, false},
		{"down", true, "applied", true, true, true, true, false, false},
		{"down", true, "applied", true, true, true, false, false, false},
		{"down", true, "applied", true, true, false, false, false, false},
		{"down", true, "applied", true, false, false, false, false, false},
		{"down", true, "applied", false, false, false, false, false, false},
		{"down", false, "pending", false, false, false, false, false, false},
		{"up", true, "applied", true, true, true, true, true, true},
	} {
		if err := postgres.Migrate(ctx, db, step.command, io.Discard); err != nil {
			t.Fatal(err)
		}
		var present bool
		if err := db.QueryRowContext(ctx, "SELECT to_regclass('public.organization') IS NOT NULL").Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present != step.present {
			t.Fatalf("after %s: table present = %t, want %t", step.command, present, step.present)
		}
		for _, table := range []string{"account", "session", "member"} {
			if err := db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&present); err != nil || present != step.accounts {
				t.Fatalf("after %s: %s present = %t, want %t: %v", step.command, table, present, step.accounts, err)
			}
		}
		if err := db.QueryRowContext(ctx, "SELECT to_regclass('public.setup') IS NOT NULL").Scan(&present); err != nil || present != step.setup {
			t.Fatalf("after %s: setup present = %t, want %t: %v", step.command, present, step.setup, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT FROM information_schema.columns WHERE table_name = 'member' AND column_name = 'handle')").Scan(&present); err != nil || present != step.handle {
			t.Fatalf("after %s: member.handle present = %t, want %t: %v", step.command, present, step.handle, err)
		}
		var status bytes.Buffer
		for _, table := range []string{"channel", "message"} {
			if err := db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&present); err != nil || present != step.messages {
				t.Fatalf("after %s: %s present = %t, want %t: %v", step.command, table, present, step.messages, err)
			}
		}
		if err := db.QueryRowContext(ctx, "SELECT to_regclass('public.topic') IS NOT NULL").Scan(&present); err != nil || present != step.topics {
			t.Fatalf("after %s: topic present = %t, want %t: %v", step.command, present, step.topics, err)
		}
		for _, table := range []string{"channel_read", "read_range", "topic_read_floor"} {
			if err := db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&present); err != nil || present != step.unread {
				t.Fatalf("after %s: %s present = %t, want %t: %v", step.command, table, present, step.unread, err)
			}
		}
		if err := postgres.Migrate(ctx, db, "status", &status); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(status.String(), "00001\t"+step.state+"\t00001_organization.sql") {
			t.Fatalf("after %s: unexpected status %q", step.command, status.String())
		}
	}
	for _, tc := range []struct {
		slug  string
		valid bool
	}{
		{"a", true}, {"ab", true}, {"foo-bar", true}, {"0", true},
		{strings.Repeat("a", 63), true}, {"a" + strings.Repeat("-", 61) + "0", true},
		{"", false}, {"foo-", false}, {"-foo", false}, {"-", false},
		{strings.Repeat("a", 64), false}, {"Foo", false}, {"foo_bar", false},
		{"foo.bar", false}, {"foo\n", false}, {"é", false},
	} {
		t.Run("slug="+tc.slug, func(t *testing.T) {
			_, err := db.ExecContext(ctx, "INSERT INTO organization (slug, name) VALUES ($1, 'Test')", tc.slug)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "organization_slug_check" {
				t.Fatalf("want slug CHECK violation, got %v", err)
			}
		})
	}
}
