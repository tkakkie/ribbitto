package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

var registrationIn org.RegistrationWriterIn = func(tx platform.Tx) org.RegistrationWriter { return postgres.RegistrationWriterIn(tx) }

// counts returns the number of organisations, members and setup rows and
// the sum of every organisation's event_seq, on the pool or in a pgx.Tx.
func counts(t *testing.T, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) [4]int64 {
	t.Helper()
	var c [4]int64
	requireNoError(t, db.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM organization), (SELECT count(*) FROM member), (SELECT count(*) FROM setup), (SELECT coalesce(sum(event_seq), 0) FROM organization)").Scan(&c[0], &c[1], &c[2], &c[3]))
	return c
}

func TestRegistrationWriterIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// first is created before acme, so the setup organisation can only be
	// acme if it comes from the setup row.
	first := orgtest.Organization(t, pool, "first", "First", 5)
	account := identitytest.Account(t, pool, "owner@example.org", "Owner")
	before := counts(t, pool)
	rollback := errors.New("caller rolls back")
	err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
		writer, q := registrationIn(tx), pgxbridge.Tx(tx)
		acme, err := writer.CreateOrganization(ctx, "Acme", "acme")
		requireNoError(t, err)
		var name string
		requireNoError(t, q.QueryRow(ctx, "SELECT name FROM organization WHERE id = $1 AND slug = 'acme'", acme).Scan(&name))
		if name != "Acme" {
			t.Fatalf("organisation name in caller's transaction = %q", name)
		}
		seq, err := writer.NextEventSeq(ctx, first)
		if err != nil || seq != 6 {
			t.Fatalf("NextEventSeq = %d, %v; want 6", seq, err)
		}
		member, err := writer.CreateMember(ctx, acme, account, org.RoleOwner, seq, "owner")
		requireNoError(t, err)
		var role, handle string
		var joined int64
		requireNoError(t, q.QueryRow(ctx, "SELECT role, joined_event_seq, handle FROM member WHERE id = $1 AND organization_id = $2 AND account_id = $3", member, acme, account).Scan(&role, &joined, &handle))
		if role != "owner" || joined != 6 || handle != "owner" {
			t.Fatalf("member in caller's transaction = %q, %d, %q", role, joined, handle)
		}
		requireNoError(t, writer.CompleteSetup(ctx, acme))
		if got, err := writer.SetupOrganization(ctx); err != nil || got != acme {
			t.Fatalf("SetupOrganization = %v, %v; want acme %v, not the first created %v", got, err, acme, first)
		}
		if got := counts(t, q); got != [4]int64{2, 1, 1, 6} {
			t.Fatalf("rows in caller's transaction = %v", got)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("caller rollback: %v", err)
	}
	if after := counts(t, pool); after != before {
		t.Fatalf("rows after rollback = %v, want %v (the sequence's increment too)", after, before)
	}
}

func TestRegistrationWriterErrors(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	taken := orgtest.Organization(t, pool, "taken", "Taken", 1)
	orgtest.Member(t, pool, taken, identitytest.Account(t, pool, "alice@example.org", "Alice"), org.RoleMember, "alice", 1)
	bob := identitytest.Account(t, pool, "bob@example.org", "Bob")
	createMember := func(role org.Role, handle string) func(org.RegistrationWriter) error {
		return func(w org.RegistrationWriter) error {
			_, err := w.CreateMember(ctx, taken, bob, role, 2, handle)
			return err
		}
	}
	createOrganization := func(name, slug string) func(org.RegistrationWriter) error {
		return func(w org.RegistrationWriter) error {
			_, err := w.CreateOrganization(ctx, name, slug)
			return err
		}
	}
	before := counts(t, pool)
	for _, tc := range []struct {
		name  string
		write func(org.RegistrationWriter) error
		want  error // nil: an untranslated CHECK stays a wrapped PostgreSQL error
	}{
		{"organization_slug_key", createOrganization("Other", "taken"), org.ErrSlugUnavailable},
		{"organization_slug_check", createOrganization("Other", "Bad_Slug"), org.ErrSlugUnavailable},
		{"organization name CHECK", createOrganization("", "other"), nil},
		{"member_organization_id_handle_key", createMember(org.RoleMember, "alice"), org.ErrHandleTaken},
		{"member_handle_format_check", createMember(org.RoleMember, "Bob"), org.ErrInvalidHandle},
		{"member_handle_reserved_check", createMember(org.RoleMember, "all"), org.ErrInvalidHandle},
		{"member role CHECK", createMember("admin", "bob"), nil},
		{"no setup row", func(w org.RegistrationWriter) error { _, err := w.SetupOrganization(ctx); return err }, org.ErrSignUpClosed},
		{"unknown organisation's sequence", func(w org.RegistrationWriter) error { _, err := w.NextEventSeq(ctx, kernel.ID{0xee}); return err }, org.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.InTx(ctx, pool, func(tx platform.Tx) error { return tc.write(registrationIn(tx)) })
			var pgErr *pgconn.PgError
			if tc.want != nil && !errors.Is(err, tc.want) || tc.want == nil && (!errors.As(err, &pgErr) || pgErr.Code != "23514" || errors.Is(err, org.ErrSlugUnavailable) || errors.Is(err, org.ErrInvalidHandle)) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			// The handle CHECKs keep the PostgreSQL error, so check which one fired.
			if errors.Is(tc.want, org.ErrInvalidHandle) && (!errors.As(err, &pgErr) || pgErr.ConstraintName != tc.name) {
				t.Fatalf("constraint = %v, want %s", err, tc.name)
			}
			if after := counts(t, pool); after != before {
				t.Fatalf("rows after the failed write = %v, want %v", after, before)
			}
		})
	}
	// A second completed setup: the first one is committed.
	requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error { return registrationIn(tx).CompleteSetup(ctx, taken) }))
	other := orgtest.Organization(t, pool, "other", "Other", 0)
	err := platform.InTx(ctx, pool, func(tx platform.Tx) error { return registrationIn(tx).CompleteSetup(ctx, other) })
	var named kernel.ID
	requireNoError(t, pool.QueryRow(ctx, "SELECT organization_id FROM setup").Scan(&named))
	if !errors.Is(err, org.ErrSetupCompleted) || named != taken {
		t.Fatalf("second setup: %v, setup names %v, want %v", err, named, taken)
	}
}
