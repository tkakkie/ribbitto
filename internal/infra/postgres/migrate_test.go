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
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
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
		command string
		present bool
		state   string
	}{{"up", true, "applied"}, {"down", false, "pending"}, {"up", true, "applied"}} {
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
		var status bytes.Buffer
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
