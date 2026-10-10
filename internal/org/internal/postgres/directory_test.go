package postgres_test

import (
	"maps"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestDirectoryLookupMembers(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := orgtest.Organization(t, pool, "acme", "Acme", 0)
	globex := orgtest.Organization(t, pool, "globex", "Globex", 0)
	alice := identitytest.Account(t, pool, "alice@example.org", "Alice")
	bob := identitytest.Account(t, pool, "bob@example.org", "Bob")
	carol := identitytest.Account(t, pool, "carol@example.org", "Carol")
	aliceMember := orgtest.Member(t, pool, acme, alice, org.RoleMember, "alice", 1)
	bobMember := orgtest.Member(t, pool, acme, bob, org.RoleMember, "bob", 1)
	carolMember := orgtest.Member(t, pool, globex, carol, org.RoleMember, "carol", 1)
	// Bob is in the requested organisation but was not requested by ID.
	for _, tc := range []struct {
		name string
		ids  []kernel.ID
		want map[kernel.ID]org.DirectoryEntry
	}{
		{"scoped", []kernel.ID{aliceMember, carolMember, {0xee}}, map[kernel.ID]org.DirectoryEntry{aliceMember: {AccountID: alice, Handle: "alice"}}},
		{"multiple", []kernel.ID{aliceMember, bobMember}, map[kernel.ID]org.DirectoryEntry{aliceMember: {AccountID: alice, Handle: "alice"}, bobMember: {AccountID: bob, Handle: "bob"}}},
		{"empty", nil, map[kernel.ID]org.DirectoryEntry{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNoError(t, platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) error {
				got, err := postgres.NewDirectoryIn(snapshot).LookupMembers(ctx, acme, tc.ids)
				requireNoError(t, err)
				if !maps.Equal(got, tc.want) {
					t.Fatalf("LookupMembers = %v, want %v", got, tc.want)
				}
				return nil
			}))
		})
	}
}

func TestDirectoryListMembers(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	acme := orgtest.Organization(t, pool, "acme", "Acme", 0)
	globex := orgtest.Organization(t, pool, "globex", "Globex", 0)
	account := identitytest.Account(t, pool, "same@example.org", "Same")
	first := orgtest.Member(t, pool, acme, account, org.RoleMember, "local", 1)
	orgtest.Member(t, pool, globex, account, org.RoleMember, "foreign", 1)
	account2 := identitytest.Account(t, pool, "next@example.org", "Next")
	second := orgtest.Member(t, pool, acme, account2, org.RoleMember, "next", 1)
	for _, tt := range []struct {
		name  string
		after *kernel.ID
		limit int32
		want  []org.ListedMember
	}{
		{"organisation", nil, 101, []org.ListedMember{{ID: first, DirectoryEntry: org.DirectoryEntry{AccountID: account, Handle: "local"}}, {ID: second, DirectoryEntry: org.DirectoryEntry{AccountID: account2, Handle: "next"}}}},
		{"bounded", nil, 1, []org.ListedMember{{ID: first, DirectoryEntry: org.DirectoryEntry{AccountID: account, Handle: "local"}}}},
		{"next", &first, 1, []org.ListedMember{{ID: second, DirectoryEntry: org.DirectoryEntry{AccountID: account2, Handle: "next"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requireNoError(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, err := postgres.NewDirectoryIn(s).ListMembers(t.Context(), acme, tt.after, tt.limit)
				requireNoError(t, err)
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("members = %v, want %v", got, tt.want)
				}
				return nil
			}))
		})
	}
}
