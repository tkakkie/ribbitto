package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestAuthzStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// acme is the setup organisation; globex is a second one, inserted
	// directly as later multi-organisation work would.
	acme := orgtest.Organization(t, pool, "acme", "Acme", 0)
	globex := orgtest.Organization(t, pool, "globex", "Globex", 0)
	alice := identitytest.Account(t, pool, "alice@example.com", "Alice")
	bob := identitytest.Account(t, pool, "bob@example.com", "Bob")
	carol := identitytest.Account(t, pool, "carol@example.com", "Carol")
	// Direct SQL selects the home organisation without adding full setup fixtures.
	if _, err := pool.Exec(ctx, "INSERT INTO setup (organization_id) VALUES ($1)", acme); err != nil {
		t.Fatal(err)
	}
	orgtest.Member(t, pool, acme, alice, org.RoleOwner, "alice", 1)
	orgtest.Member(t, pool, globex, bob, org.RoleMember, "bob", 1)
	store := postgres.NewAuthzStore(pool)
	m, err := store.Membership(ctx, alice, "acme")
	if err != nil || m.Organization != (org.Organization{ID: acme, Slug: "acme", Name: "Acme"}) ||
		m.Member.OrganizationID != acme || m.Member.AccountID != alice || m.Member.Role != org.RoleOwner || m.Member.Handle != "alice" {
		t.Fatalf("alice in acme: %+v, %v", m, err)
	}
	for _, tt := range []struct {
		name    string
		account kernel.ID
		slug    string
	}{
		{"member of the other organisation", bob, "acme"},
		{"no membership", carol, "acme"},
		{"own organisation's member, other slug", alice, "globex"},
		{"unknown slug", alice, "initech"},
	} {
		if _, err := store.Membership(ctx, tt.account, tt.slug); !errors.Is(err, org.ErrNotFound) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if slug, err := store.HomeSlug(ctx, alice); err != nil || slug != "acme" {
		t.Fatalf("alice's home: %q, %v", slug, err)
	}
	// bob belongs to globex only, which is not the setup organisation.
	for _, account := range []kernel.ID{bob, carol} {
		if _, err := store.HomeSlug(ctx, account); !errors.Is(err, org.ErrNotFound) {
			t.Errorf("home of %x: %v", account, err)
		}
	}
}
