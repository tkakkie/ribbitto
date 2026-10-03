package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// organizations counts rows with slug, outside any test transaction.
func organizations(t *testing.T, pool *pgxpool.Pool, slug string) int {
	t.Helper()
	var n int
	requireNoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM organization WHERE slug = $1", slug).Scan(&n))
	return n
}

func insertOrganization(ctx context.Context, tx postgres.Tx, slug string) error {
	_, err := pgxbridge.Tx(tx).Exec(ctx, "INSERT INTO organization (slug, name) VALUES ($1, 'Test')", slug)
	return err
}

// The runners follow pgx.BeginFunc: commit on success, roll back and return
// the callback's own error, roll back and re-panic with the original value.
func TestInTx(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()

	t.Run("commits on success", func(t *testing.T) {
		requireNoError(t, postgres.InTx(ctx, pool, func(tx postgres.Tx) error { return insertOrganization(ctx, tx, "committed") }))
		if n := organizations(t, pool, "committed"); n != 1 {
			t.Fatalf("%d rows after commit, want 1", n)
		}
	})
	t.Run("rolls back and returns the callback's error", func(t *testing.T) {
		failure := errors.New("callback failed")
		err := postgres.InTx(ctx, pool, func(tx postgres.Tx) error {
			requireNoError(t, insertOrganization(ctx, tx, "failed"))
			return failure
		})
		if err != failure {
			t.Fatalf("InTx = %v, want the callback's own error", err)
		}
		if n := organizations(t, pool, "failed"); n != 0 {
			t.Fatalf("%d rows after a failed callback, want 0", n)
		}
	})
	t.Run("rolls back and re-panics with the original value", func(t *testing.T) {
		type sentinel struct{ reason string }
		value := &sentinel{"boom"}
		func() {
			defer func() {
				if got := recover(); got != value {
					t.Fatalf("recovered %v, want the original panic value", got)
				}
			}()
			_ = postgres.InTx(ctx, pool, func(tx postgres.Tx) error {
				requireNoError(t, insertOrganization(ctx, tx, "panicked"))
				panic(value)
			})
			t.Fatal("InTx returned instead of panicking")
		}()
		if n := organizations(t, pool, "panicked"); n != 0 {
			t.Fatalf("%d rows after a panic, want 0", n)
		}
	})
	t.Run("one transaction across unwraps", func(t *testing.T) {
		requireNoError(t, postgres.InTx(ctx, pool, func(tx postgres.Tx) error {
			var first, second int64
			requireNoError(t, pgxbridge.Tx(tx).QueryRow(ctx, "SELECT txid_current()").Scan(&first))
			requireNoError(t, pgxbridge.Tx(tx).QueryRow(ctx, "SELECT txid_current()").Scan(&second))
			if first != second {
				t.Errorf("transaction ids %d and %d, want one transaction", first, second)
			}
			return nil
		}))
	})
	t.Run("returns a begin failure", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		called := false
		err := postgres.InTx(cancelled, pool, func(postgres.Tx) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("InTx on a cancelled context = %v, callback called = %t", err, called)
		}
	})
}

func TestInSnapshot(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()

	t.Run("refuses writes", func(t *testing.T) {
		err := postgres.InSnapshot(ctx, pool, func(s postgres.Snapshot) error {
			_, err := pgxbridge.Snapshot(s).Exec(ctx, "INSERT INTO organization (slug, name) VALUES ('written', 'Test')")
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
			t.Fatalf("write in a snapshot = %v, want read_only_sql_transaction (25006)", err)
		}
	})
	t.Run("sees one snapshot while others commit", func(t *testing.T) {
		requireNoError(t, postgres.InSnapshot(ctx, pool, func(s postgres.Snapshot) error {
			count := func() int {
				var n int
				requireNoError(t, pgxbridge.Snapshot(s).QueryRow(ctx, "SELECT count(*) FROM organization").Scan(&n))
				return n
			}
			before := count()
			requireNoError(t, postgres.InTx(ctx, pool, func(tx postgres.Tx) error { return insertOrganization(ctx, tx, "concurrent") }))
			if after := count(); after != before {
				t.Errorf("snapshot saw %d then %d organizations, want one snapshot", before, after)
			}
			return nil
		}))
		if n := organizations(t, pool, "concurrent"); n != 1 {
			t.Fatalf("the concurrent commit is missing outside the snapshot: %d rows", n)
		}
	})
}
