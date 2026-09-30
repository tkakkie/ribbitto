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
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

func TestAccountSchema(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// Raw SQL and queries exercise schema constraints directly, including invalid rows.
	q := sqlcgen.New(pool)
	var nullable int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name IN ('account', 'session', 'member') AND is_nullable = 'YES'").Scan(&nullable))
	if nullable != 0 {
		t.Fatalf("new tables have %d nullable columns", nullable)
	}
	// Both minimum and multibyte maximum values must survive the database checks.
	var accounts []sqlcgen.Account
	var organizations []sqlcgen.Organization
	for i, size := range []int{1, 50} {
		email, err := domain.ValidateEmail([]string{"a@b", strings.Repeat("界", 84) + "@b"}[i])
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
		name, err = domain.ValidateOrganizationName(strings.Repeat("界", []int{1, 100}[i]))
		requireNoError(t, err)
		slug, err := domain.ValidateSlug(strings.Repeat("a", []int{1, 63}[i]))
		requireNoError(t, err)
		_, err = pool.Exec(ctx, "INSERT INTO organization (slug, name) VALUES ($1, $2)", slug, name)
		requireNoError(t, err)
		org, err := q.GetOrganizationBySlug(ctx, slug)
		requireNoError(t, err)
		organizations = append(organizations, org)
	}
	org, other := organizations[0], organizations[1]
	// Each sequence commits with its event row, as every writer must (the
	// migration's deferred trigger refuses a sequence without one).
	for want := int64(1); want <= 3; want++ {
		tx, err := pool.Begin(ctx)
		requireNoError(t, err)
		got, err := sqlcgen.New(tx).NextEventSeq(ctx, org.ID)
		if err != nil || got != want {
			t.Fatalf("sequence: got %d, %v; want %d", got, err, want)
		}
		_, err = tx.Exec(ctx, "INSERT INTO event_log (organization_id, seq, kind, data) VALUES ($1, $2, 'test.sequence', '{}')", org.ID, got)
		requireNoError(t, err)
		requireNoError(t, tx.Commit(ctx))
	}
	// The rows only satisfied the trigger; remove them so the restrict checks
	// below see member's foreign key alone.
	_, err := pool.Exec(ctx, "DELETE FROM event_log WHERE organization_id = $1", org.ID)
	requireNoError(t, err)
	unchanged, err := q.GetOrganizationBySlug(ctx, other.Slug)
	if err != nil || unchanged.EventSeq != 0 {
		t.Fatalf("other organization changed: %+v, %v", unchanged, err)
	}
	member, err := q.CreateMember(ctx, sqlcgen.CreateMemberParams{OrganizationID: org.ID, AccountID: accounts[0].ID, Role: "owner", JoinedEventSeq: 1, Handle: "owner"})
	requireNoError(t, err)
	gotMember, err := q.GetMemberByOrganizationAndAccount(ctx, sqlcgen.GetMemberByOrganizationAndAccountParams{OrganizationID: org.ID, AccountID: accounts[0].ID})
	if err != nil || gotMember != member {
		t.Fatalf("member lookup: %+v, %v", gotMember, err)
	}
	_, err = q.GetMemberByOrganizationAndAccount(ctx, sqlcgen.GetMemberByOrganizationAndAccountParams{OrganizationID: other.ID, AccountID: accounts[0].ID})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-organization lookup: %v", err)
	}
	expiry := pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond), Valid: true}
	session, err := q.CreateSession(ctx, sqlcgen.CreateSessionParams{TokenHash: make([]byte, 32), AccountID: accounts[1].ID, ExpiresAt: expiry})
	requireNoError(t, err)
	for _, id := range []pgtype.UUID{accounts[0].ID, session.ID, member.ID} {
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
		{"role", "UPDATE member SET role = 'admin' WHERE organization_id = $1", "23514"},
		{"join sequence", "UPDATE member SET joined_event_seq = 0 WHERE organization_id = $1", "23514"},
		{"duplicate member", "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) SELECT organization_id, account_id, role, joined_event_seq, 'other' FROM member WHERE organization_id = $1", "23505"},
		{"missing organization", "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES (uuidv7(), $2, 'member', 1, 'other')", "23503"},
		{"missing member account", "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, uuidv7(), 'member', 1, 'other')", "23503"},
		{"upper-case handle", "UPDATE member SET handle = 'Owner' WHERE organization_id = $1", "23514"},
		{"non-ASCII handle", "UPDATE member SET handle = 'ownér' WHERE organization_id = $1", "23514"},
		{"short handle", "UPDATE member SET handle = 'o' WHERE organization_id = $1", "23514"},
		{"long handle", "UPDATE member SET handle = repeat('o', 33) WHERE organization_id = $1", "23514"},
		{"handle start", "UPDATE member SET handle = '0owner' WHERE organization_id = $1", "23514"},
		{"handle end", "UPDATE member SET handle = 'owner.' WHERE organization_id = $1", "23514"},
		{"reserved handle", "UPDATE member SET handle = 'everyone' WHERE organization_id = $1", "23514"},
		{"missing session account", "UPDATE session SET account_id = uuidv7() WHERE account_id = $2", "23503"},
		{"short token", "UPDATE session SET token_hash = decode(repeat('00', 31), 'hex') WHERE account_id = $2", "23514"},
		{"duplicate token", "INSERT INTO session (token_hash, account_id, expires_at) SELECT token_hash, account_id, expires_at FROM session WHERE account_id = $2", "23505"},
		{"expiry", "UPDATE session SET expires_at = created_at WHERE account_id = $2", "23514"},
		{"restrict organization", "DELETE FROM organization WHERE id = $1", "23001"},
		{"restrict account", "DELETE FROM account WHERE id IN (SELECT account_id FROM member WHERE organization_id = $1)", "23001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Typed parameters allow each statement to use either fixture identifier.
			_, err := pool.Exec(ctx, "WITH fixture AS (SELECT $1::uuid, $2::uuid) "+tc.sql, org.ID, accounts[1].ID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatalf("want SQLSTATE %s, got %v", tc.code, err)
			}
			if tc.name == "restrict organization" && pgErr.ConstraintName != "member_organization_id_fkey" {
				t.Fatalf("restricted by %q, want member_organization_id_fkey", pgErr.ConstraintName)
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
		func() error { return q.DeleteSessionByTokenHash(ctx, session.TokenHash) },
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

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
