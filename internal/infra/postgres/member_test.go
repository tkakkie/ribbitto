package postgres_test

import (
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestChangeHandle(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// acme: alice, bob and six racers; globex: carol; dave has no membership.
	acmeID := pgtest.Organization(t, pool, "acme", "Acme", 0)
	globexID := pgtest.Organization(t, pool, "globex", "Globex", 0)
	for _, name := range []string{"alice", "bob", "carol", "dave", "racer1", "racer2", "racer3", "racer4", "racer5", "racer6"} {
		account := pgtest.Account(t, pool, name+"@example.org", name)
		org := acmeID
		if name == "carol" {
			org = globexID
		}
		if name != "dave" {
			pgtest.Member(t, pool, org, account, domain.RoleMember, name, 1)
		}
	}
	accounts := map[string]*identity.Account{}
	rows, err := pool.Query(ctx, "SELECT id, display_name FROM account")
	requireNoError(t, err)
	for rows.Next() {
		var a identity.Account
		requireNoError(t, rows.Scan(&a.ID, &a.DisplayName))
		accounts[a.DisplayName] = &a
	}
	requireNoError(t, rows.Err())
	service := member.New(authz.New(postgres.NewAuthzStore(pool)), postgres.NewMemberStore(pool))
	change := func(name, slug, handle string) (string, error) {
		return service.ChangeHandle(ctx, accounts[name], slug, handle)
	}

	got, err := change("alice", "acme", " Alicia ")
	if err != nil || got != "alicia" || handles(t, pool)["alice"] != "alicia" {
		t.Fatalf("own change: %q, %v", got, err)
	}
	// Denied, invalid or conflicting changes leave every handle as it was.
	before := handles(t, pool)
	for _, tt := range []struct {
		name, account, slug, handle string
		want                        error
	}{
		{"case variant of another member's handle", "alice", "acme", "BOB", member.ErrHandleTaken},
		{"reserved word", "alice", "acme", "all", member.ErrInvalidHandle},
		{"member of another organisation", "carol", "acme", "carol2", authz.ErrNotFound},
		{"account without a membership", "dave", "acme", "dave", authz.ErrNotFound},
		{"signed out", "nobody", "acme", "nobody", authz.ErrNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := change(tt.account, tt.slug, tt.handle); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if after := handles(t, pool); !maps.Equal(after, before) {
				t.Fatalf("handles changed: %v → %v", before, after)
			}
		})
	}
	// The store is scoped too: another organisation's member id matches nothing.
	var acme, carol domain.ID
	requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT id FROM organization WHERE slug = 'acme'), (SELECT m.id FROM member m JOIN account a ON a.id = m.account_id WHERE a.email = 'carol@example.org')").Scan(&acme, &carol))
	if err := postgres.NewMemberStore(pool).UpdateHandle(ctx, acme, carol, "hijack"); !errors.Is(err, authz.ErrNotFound) || handles(t, pool)["carol"] != "carol" {
		t.Fatalf("cross-organisation update: %v", err)
	}

	// Handles are unique per organisation only.
	if _, err := change("carol", "globex", "bob"); err != nil {
		t.Fatalf("same handle in another organisation: %v", err)
	}
	// A released handle can be claimed again at once.
	if _, err := change("bob", "acme", "robert"); err != nil {
		t.Fatal(err)
	}
	if _, err := change("alice", "acme", "bob"); err != nil {
		t.Fatalf("claiming a released handle: %v", err)
	}

	// Concurrent claims: exactly one winner, and every loser keeps its handle.
	results := make(chan error, 6)
	for i := range 6 {
		go func() {
			_, err := change(fmt.Sprintf("racer%d", i+1), "acme", "winner")
			results <- err
		}()
	}
	winners := 0
	for range 6 {
		switch err := <-results; {
		case err == nil:
			winners++
		case !errors.Is(err, member.ErrHandleTaken):
			t.Fatalf("racer: %v", err)
		}
	}
	final := handles(t, pool)
	unchanged := 0
	for i := range 6 {
		if name := fmt.Sprintf("racer%d", i+1); final[name] == name {
			unchanged++
		}
	}
	if winners != 1 || unchanged != 5 {
		t.Fatalf("winners=%d, unchanged losers=%d: %v", winners, unchanged, final)
	}
}

// handles maps each member's display name to their handle.
func handles(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT a.display_name, m.handle FROM member m JOIN account a ON a.id = m.account_id")
	requireNoError(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, handle string
		requireNoError(t, rows.Scan(&name, &handle))
		got[name] = handle
	}
	requireNoError(t, rows.Err())
	return got
}
