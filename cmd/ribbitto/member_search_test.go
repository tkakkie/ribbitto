package main

import (
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestMemberSearchOrganizationScope(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	acme := orgtest.Organization(t, pool, "acme", "Acme", 0)
	globex := orgtest.Organization(t, pool, "globex", "Globex", 0)
	account := identitytest.Account(t, pool, "local@example.org", "加藤 智也")
	local := orgtest.Member(t, pool, acme, account, org.RoleMember, "tomoya", 1)
	secondAccount := identitytest.Account(t, pool, "second@example.org", "加藤 智也")
	second := orgtest.Member(t, pool, acme, secondAccount, org.RoleMember, "bob", 1)
	foreign := identitytest.Account(t, pool, "foreign@example.org", "加藤 智也")
	orgtest.Member(t, pool, globex, foreign, org.RoleMember, "tomoya", 1)
	// This account must not become a candidate merely because its name matches.
	identitytest.Account(t, pool, "unjoined@example.org", "加藤 智也")
	// The same account's membership in another organisation must also stay out.
	orgtest.Member(t, pool, globex, account, org.RoleMember, "tomoya-other", 1)
	membership, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: account}, "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"智也", "tomo", "@"} {
		t.Run(query, func(t *testing.T) {
			got, err := newMemberSearch(pool).Search(t.Context(), membership, query)
			want := []org.MemberCandidate{{ID: local, Handle: "tomoya", DisplayName: "加藤 智也"}}
			if query != "tomo" {
				want = append([]org.MemberCandidate{{ID: second, Handle: "bob", DisplayName: "加藤 智也"}}, want...)
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Search(%q) = %v, %v; want %v", query, got, err, want)
			}
		})
	}
}
