package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestServeSchema(t *testing.T) {
	// A later handler error proves the version check permits startup without
	// opening a listener or launching maintenance workers.
	t.Setenv("RIBBITTO_DEV_ASSETS", t.TempDir()+"/missing")
	for _, state := range []string{"never migrated", "behind", "equal", "ahead"} {
		t.Run(state, func(t *testing.T) {
			pool := pgtest.NewEmpty(t)
			if state != "never migrated" {
				m := pgtest.NewMigrator(t, pool)
				m.Up(t.Context())
				if state == "behind" {
					m.Down(t.Context())
				}
				if state == "ahead" {
					if _, err := pool.Exec(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) SELECT max(version_id) + 1, true FROM goose_db_version"); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := "ribbitto migrate up"
			if state == "equal" || state == "ahead" {
				want = "reading stylesheet"
			}
			if err := serve(t.Context(), schemaTestURL(t, pool)); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("serve error = %v, want %q", err, want)
			}
		})
	}
}

func schemaTestURL(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	databaseURL, err := url.Parse(pgtest.AdminURL(t))
	if err != nil {
		t.Fatal(err)
	}
	databaseURL.Path = "/" + pool.Config().ConnConfig.Database
	return databaseURL.String()
}
