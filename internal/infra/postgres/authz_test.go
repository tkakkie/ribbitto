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
	var acme, globex, alice, bob, carol domain.ID
	for _, q := range []struct {
		dest *domain.ID
		sql  string
	}{
		{&acme, "INSERT INTO organization (slug, name) VALUES ('acme', 'Acme') RETURNING id"},
		{&globex, "INSERT INTO organization (slug, name) VALUES ('globex', 'Globex') RETURNING id"},
		{&alice, "INSERT INTO account (email, display_name, password_hash) VALUES ('alice@example.com', 'Alice', '$argon2id$x') RETURNING id"},
		{&bob, "INSERT INTO account (email, display_name, password_hash) VALUES ('bob@example.com', 'Bob', '$argon2id$x') RETURNING id"},
		{&carol, "INSERT INTO account (email, display_name, password_hash) VALUES ('carol@example.com', 'Carol', '$argon2id$x') RETURNING id"},
	} {
		if err := pool.QueryRow(ctx, q.sql).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		"INSERT INTO setup (organization_id) VALUES ($1)",
		"INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $3, 'owner', 1, 'alice')",
		"INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($2, $4, 'member', 1, 'bob')",
	} {
		if _, err := pool.Exec(ctx, "WITH ids AS (SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid) "+sql, acme, globex, alice, bob); err != nil {
			t.Fatal(err)
		}
	}
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
