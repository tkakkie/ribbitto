package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestAuthzStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// acme is the setup organisation; globex is a second one, inserted
	// directly as later multi-organisation work would.
	acme := pgtest.Organization(t, pool, "acme", "Acme", 0)
	globex := pgtest.Organization(t, pool, "globex", "Globex", 0)
	alice := pgtest.Account(t, pool, "alice@example.com", "Alice")
	bob := pgtest.Account(t, pool, "bob@example.com", "Bob")
	carol := pgtest.Account(t, pool, "carol@example.com", "Carol")
	// Direct SQL selects the home organisation without adding full setup fixtures.
	if _, err := pool.Exec(ctx, "INSERT INTO setup (organization_id) VALUES ($1)", acme); err != nil {
		t.Fatal(err)
	}
	pgtest.Member(t, pool, acme, alice, domain.RoleOwner, "alice", 1)
	pgtest.Member(t, pool, globex, bob, domain.RoleMember, "bob", 1)
	store := postgres.NewAuthzStore(pool)
	m, err := store.Membership(ctx, alice, "acme")
	if err != nil || m.Organization != (domain.Organization{ID: acme, Slug: "acme", Name: "Acme"}) ||
		m.Member.OrganizationID != acme || m.Member.AccountID != alice || m.Member.Role != domain.RoleOwner || m.Member.Handle != "alice" {
		t.Fatalf("alice in acme: %+v, %v", m, err)
	}
	for _, tt := range []struct {
		name    string
		account domain.ID
		slug    string
	}{
		{"member of the other organisation", bob, "acme"},
		{"no membership", carol, "acme"},
		{"own organisation's member, other slug", alice, "globex"},
		{"unknown slug", alice, "initech"},
	} {
		if _, err := store.Membership(ctx, tt.account, tt.slug); !errors.Is(err, authz.ErrNotFound) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if slug, err := store.HomeSlug(ctx, alice); err != nil || slug != "acme" {
		t.Fatalf("alice's home: %q, %v", slug, err)
	}
	// bob belongs to globex only, which is not the setup organisation.
	for _, account := range []domain.ID{bob, carol} {
		if _, err := store.HomeSlug(ctx, account); !errors.Is(err, authz.ErrNotFound) {
			t.Errorf("home of %x: %v", account, err)
		}
	}
}
