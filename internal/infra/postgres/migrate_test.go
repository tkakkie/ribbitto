package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestMigrations(t *testing.T) {
	url, configured := os.LookupEnv("RIBBITTO_TEST_DATABASE_URL")
	if !configured {
		if os.Getenv("RIBBITTO_REQUIRE_DB") == "1" {
			t.Fatal("RIBBITTO_TEST_DATABASE_URL is required")
		}
		t.Skip("RIBBITTO_TEST_DATABASE_URL is unset")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	db := disposableDatabase(t, ctx, url)
	for _, step := range []struct {
		command string
		present bool
		state   string
	}{{"up", true, "applied"}, {"down", false, "pending"}, {"up", true, "applied"}} {
		if err := Migrate(ctx, db, step.command, io.Discard); err != nil {
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
		if err := Migrate(ctx, db, "status", &status); err != nil {
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

func disposableDatabase(t *testing.T, ctx context.Context, url string) *sql.DB {
	t.Helper()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	name := "ribbitto_test_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Test cancellation must not prevent the disposable database being removed.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.Database = name
	db := stdlib.OpenDB(*config)
	// Cleanup runs in reverse order: close test connections before dropping.
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
