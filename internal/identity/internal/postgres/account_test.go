package postgres_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// TestAccountSchema holds the account and session part of the schema checks;
// the organisation and member part stays in internal/infra/postgres until org
// becomes a module.
func TestAccountSchema(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// Raw SQL and queries exercise schema constraints directly, including invalid rows.
	q := sqlcgen.New(pool)
	var nullable int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name IN ('account', 'session') AND is_nullable = 'YES'").Scan(&nullable))
	if nullable != 0 {
		t.Fatalf("new tables have %d nullable columns", nullable)
	}
	// Both minimum and multibyte maximum values must survive the database checks.
	var accounts []sqlcgen.Account
	for i, size := range []int{1, 50} {
		email, err := identity.ValidateEmail([]string{"a@b", strings.Repeat("界", 84) + "@b"}[i])
		requireNoError(t, err)
		name, err := domain.ValidateDisplayName(strings.Repeat("界", size))
		requireNoError(t, err)
		account, err := q.CreateAccount(ctx, sqlcgen.CreateAccountParams{Email: email, DisplayName: name, PasswordHash: "$argon2id$test"})
		requireNoError(t, err)
		accounts = append(accounts, account)
		for _, lookup := range []func() (sqlcgen.Account, error){func() (sqlcgen.Account, error) { return q.GetAccountByEmail(ctx, email) }, func() (sqlcgen.Account, error) { return q.GetAccountByID(ctx, account.ID) }} {
			got, err := lookup()
			if err != nil || got != account {
				t.Fatalf("account lookup: got %+v, %v; want %+v", got, err, account)
			}
		}
	}
	expiry := pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond), Valid: true}
	session, err := q.CreateSession(ctx, sqlcgen.CreateSessionParams{TokenHash: make([]byte, 32), AccountID: accounts[1].ID, ExpiresAt: expiry})
	requireNoError(t, err)
	for _, id := range []pgtype.UUID{accounts[0].ID, session.ID} {
		if !id.Valid || id.Bytes[6]>>4 != 7 {
			t.Fatalf("expected UUIDv7, got %v", id)
		}
	}
	for _, tc := range []struct{ name, sql, code string }{
		{"duplicate email", "UPDATE account SET email = 'a@b' WHERE id = $2", "23505"},
		{"uppercase email", "UPDATE account SET email = 'A@b' WHERE id = $2", "23514"},
		{"non-NFC email", "UPDATE account SET email = 'e\u0301@b' WHERE id = $2", "23514"},
		{"non-NFC name", "UPDATE account SET display_name = 'e\u0301' WHERE id = $2", "23514"},
		{"long email", "UPDATE account SET email = repeat('a', 255) WHERE id = $2", "23514"},
		{"empty name", "UPDATE account SET display_name = '' WHERE id = $2", "23514"},
		{"long name", "UPDATE account SET display_name = repeat('界', 51) WHERE id = $2", "23514"},
		{"hash format", "UPDATE account SET password_hash = 'x$argon2id$' WHERE id = $2", "23514"},
		{"missing session account", "UPDATE session SET account_id = uuidv7() WHERE account_id = $2", "23503"},
		{"short token", "UPDATE session SET token_hash = decode(repeat('00', 31), 'hex') WHERE account_id = $2", "23514"},
		{"duplicate token", "INSERT INTO session (token_hash, account_id, expires_at) SELECT token_hash, account_id, expires_at FROM session WHERE account_id = $2", "23505"},
		{"expiry", "UPDATE session SET expires_at = created_at WHERE account_id = $2", "23514"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Typed parameters allow each statement to use either fixture identifier.
			// These cases use only the second account ($2); $1 keeps the statements as in the legacy test.
			_, err := pool.Exec(ctx, "WITH fixture AS (SELECT $1::uuid, $2::uuid) "+tc.sql, accounts[0].ID, accounts[1].ID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatalf("want SQLSTATE %s, got %v", tc.code, err)
			}
		})
	}
	for _, offset := range []time.Duration{-time.Second, 0, time.Second} {
		got, err := q.GetSessionByTokenHash(ctx, sqlcgen.GetSessionByTokenHashParams{TokenHash: session.TokenHash, Now: pgtype.Timestamptz{Time: expiry.Time.Add(offset), Valid: true}})
		if offset < 0 {
			if err != nil || got.Session.ID != session.ID || got.ID != accounts[1].ID || got.Email != accounts[1].Email || got.DisplayName != accounts[1].DisplayName {
				t.Fatalf("live session: %+v, %v", got, err)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("expired session returned: %+v, %v", got, err)
		}
	}
	requireNoError(t, q.DeleteExpiredSessions(ctx, expiry))
	_, err = q.GetSessionByTokenHash(ctx, sqlcgen.GetSessionByTokenHashParams{TokenHash: session.TokenHash, Now: session.CreatedAt})
	requireNoError(t, err) // Expiring exactly at the cutoff is not before it.
	for i, remove := range []func() error{
		func() error {
			return q.DeleteExpiredSessions(ctx, pgtype.Timestamptz{Time: expiry.Time.Add(time.Second), Valid: true})
		},
		func() error {
			_, err := q.DeleteSessionByTokenHash(ctx, session.TokenHash)
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, "DELETE FROM account WHERE id = $1", accounts[1].ID)
			return err
		},
	} {
		requireNoError(t, remove())
		var count int
		requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM session WHERE account_id = $1", accounts[1].ID).Scan(&count))
		if count != 0 {
			t.Fatalf("sessions remain after deletion: %d", count)
		}
		if i < 2 {
			_, err = q.CreateSession(ctx, sqlcgen.CreateSessionParams{TokenHash: session.TokenHash, AccountID: accounts[1].ID, ExpiresAt: expiry})
			requireNoError(t, err)
		}
	}
}
