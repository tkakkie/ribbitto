package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

func TestAccountCreatorIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	var createAccount org.AccountCreatorIn = func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }
	rollback := errors.New("caller rolls back")
	err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
		id, err := createAccount(tx).CreateAccount(ctx, "new@example.org", "New", "$argon2id$new")
		requireNoError(t, err)
		row, err := sqlcgen.New(pgxbridge.Tx(tx)).GetAccountByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
		requireNoError(t, err)
		if id == (kernel.ID{}) || row.Email != "new@example.org" || row.DisplayName != "New" || row.PasswordHash != "$argon2id$new" {
			t.Fatal("account not visible with supplied values in caller's transaction")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("caller rollback: %v", err)
	}
	var count int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM account").Scan(&count))
	if count != 0 {
		t.Fatalf("accounts remain after rollback: %d", count)
	}
}

func TestAccountCreatorErrors(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	q := sqlcgen.New(pool)
	id := account(t, pool, "taken@example.org", "Original")
	original, err := q.GetAccountByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
	requireNoError(t, err)
	var createAccount org.AccountCreatorIn = func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }
	for _, tc := range []struct {
		name, email, displayName, hash string
		want                           error
	}{
		{"taken", "taken@example.org", "Changed", "$argon2id$changed", identity.ErrEmailTaken},
		{"lower case", "Upper@example.org", "New", "$argon2id$new", identity.ErrInvalidEmail},
		{"NFC", "e\u0301@example.org", "New", "$argon2id$new", identity.ErrInvalidEmail},
		{"length", strings.Repeat("a", 255) + "@b", "New", "$argon2id$new", identity.ErrInvalidEmail},
		{"display name", "new@example.org", "", "$argon2id$new", nil},
		{"password hash", "new@example.org", "New", "invalid", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
				creator := createAccount(tx)
				_, err := creator.CreateAccount(ctx, "earlier@example.org", "Earlier", "$argon2id$earlier")
				requireNoError(t, err)
				id, err := creator.CreateAccount(ctx, tc.email, tc.displayName, tc.hash)
				if id != (kernel.ID{}) {
					t.Fatalf("failed creation returned ID %v", id)
				}
				if tc.want != nil {
					if !errors.Is(err, tc.want) {
						t.Fatalf("CreateAccount error = %v, want %v", err, tc.want)
					}
				} else {
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != "23514" || !strings.HasPrefix(err.Error(), "creating account: ") {
						t.Fatalf("non-email CHECK should remain a wrapped PostgreSQL error: %v", err)
					}
				}
				return err
			})
			if err == nil {
				t.Fatal("failed transaction returned no error")
			}
			var count int
			requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM account").Scan(&count))
			if count != 1 {
				t.Fatalf("accounts after failed transaction rollback: %d, want only original", count)
			}
			got, err := q.GetAccountByID(ctx, original.ID)
			requireNoError(t, err)
			if got != original {
				t.Fatal("pre-existing account changed")
			}
		})
	}
}
