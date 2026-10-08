package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// TestOrganizationMemberSchema holds the organisation, member and setup schema
// checks; identity's store tests hold the account and session part.
func TestOrganizationMemberSchema(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// Raw SQL and queries exercise schema constraints directly, including invalid rows.
	q := sqlcgen.New(pool)
	var nullable int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name IN ('organization', 'member', 'setup') AND is_nullable = 'YES'").Scan(&nullable))
	if nullable != 0 {
		t.Fatalf("new tables have %d nullable columns", nullable)
	}
	// Both minimum and multibyte maximum values must survive the database checks.
	var accounts []pgtype.UUID
	var organizations []sqlcgen.Organization
	for i, size := range []int{1, 50} {
		email, err := identity.ValidateEmail([]string{"a@b", strings.Repeat("界", 84) + "@b"}[i])
		requireNoError(t, err)
		name, err := identity.ValidateDisplayName(strings.Repeat("界", size))
		requireNoError(t, err)
		account := identitytest.Account(t, pool, email, name)
		accounts = append(accounts, pgtype.UUID{Bytes: account, Valid: true})
		name, err = org.ValidateOrganizationName(strings.Repeat("界", []int{1, 100}[i]))
		requireNoError(t, err)
		slug, err := org.ValidateSlug(strings.Repeat("a", []int{1, 63}[i]))
		requireNoError(t, err)
		_, err = pool.Exec(ctx, "INSERT INTO organization (slug, name) VALUES ($1, $2)", slug, name)
		requireNoError(t, err)
		organization, err := getOrganizationBySlug(ctx, pool, slug)
		requireNoError(t, err)
		organizations = append(organizations, organization)
	}
	organization, other := organizations[0], organizations[1]
	// Each sequence commits with its event row, as every writer must (the
	// migration's deferred trigger refuses a sequence without one).
	for want := int64(1); want <= 3; want++ {
		tx, err := pool.Begin(ctx)
		requireNoError(t, err)
		got, err := sqlcgen.New(tx).NextEventSeq(ctx, organization.ID)
		if err != nil || got != want {
			t.Fatalf("sequence: got %d, %v; want %d", got, err, want)
		}
		_, err = tx.Exec(ctx, "INSERT INTO event_log (organization_id, seq, kind, data) VALUES ($1, $2, 'test.sequence', '{}')", organization.ID, got)
		requireNoError(t, err)
		requireNoError(t, tx.Commit(ctx))
	}
	// The rows only satisfied the trigger; remove them so the restrict checks
	// below see member's foreign key alone.
	_, err := pool.Exec(ctx, "DELETE FROM event_log WHERE organization_id = $1", organization.ID)
	requireNoError(t, err)
	unchanged, err := getOrganizationBySlug(ctx, pool, other.Slug)
	if err != nil || unchanged.EventSeq != 0 {
		t.Fatalf("other organization changed: %+v, %v", unchanged, err)
	}
	member, err := q.CreateMember(ctx, sqlcgen.CreateMemberParams{OrganizationID: organization.ID, AccountID: accounts[0], Role: "owner", JoinedEventSeq: 1, Handle: "owner"})
	requireNoError(t, err)
	gotMember, err := q.GetMemberByOrganizationAndAccount(ctx, sqlcgen.GetMemberByOrganizationAndAccountParams{OrganizationID: organization.ID, AccountID: accounts[0]})
	if err != nil || gotMember != member {
		t.Fatalf("member lookup: %+v, %v", gotMember, err)
	}
	_, err = q.GetMemberByOrganizationAndAccount(ctx, sqlcgen.GetMemberByOrganizationAndAccountParams{OrganizationID: other.ID, AccountID: accounts[0]})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-organization lookup: %v", err)
	}
	if !member.ID.Valid || member.ID.Bytes[6]>>4 != 7 {
		t.Fatalf("expected UUIDv7, got %v", member.ID)
	}
	for _, tc := range []struct{ name, sql, code string }{
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
		{"restrict organization", "DELETE FROM organization WHERE id = $1", "23001"},
		{"restrict account", "DELETE FROM account WHERE id IN (SELECT account_id FROM member WHERE organization_id = $1)", "23001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Typed parameters allow each statement to use either fixture identifier.
			_, err := pool.Exec(ctx, "WITH fixture AS (SELECT $1::uuid, $2::uuid) "+tc.sql, organization.ID, accounts[1])
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatalf("want SQLSTATE %s, got %v", tc.code, err)
			}
			if tc.name == "restrict organization" && pgErr.ConstraintName != "member_organization_id_fkey" {
				t.Fatalf("restricted by %q, want member_organization_id_fkey", pgErr.ConstraintName)
			}
		})
	}
}
