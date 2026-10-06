package postgres_test

import (
	"maps"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestDirectoryLookupDisplayNames(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	alice := identitytest.Account(t, pool, "alice@example.org", "Alice")
	bob := identitytest.Account(t, pool, "bob@example.org", "Bob")
	// Carol exists but no case requests her, so she must never appear.
	identitytest.Account(t, pool, "carol@example.org", "Carol")
	for _, tc := range []struct {
		name string
		ids  []kernel.ID
		want map[kernel.ID]string
	}{
		{"several", []kernel.ID{alice, bob}, map[kernel.ID]string{alice: "Alice", bob: "Bob"}},
		{"not requested", []kernel.ID{alice}, map[kernel.ID]string{alice: "Alice"}},
		{"unknown", []kernel.ID{alice, {0xee}}, map[kernel.ID]string{alice: "Alice"}},
		{"empty", nil, map[kernel.ID]string{}},
		{"duplicate", []kernel.ID{bob, bob}, map[kernel.ID]string{bob: "Bob"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNoError(t, platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) error {
				got, err := postgres.NewDirectoryIn(snapshot).LookupDisplayNames(ctx, tc.ids)
				requireNoError(t, err)
				if !maps.Equal(got, tc.want) {
					t.Fatalf("LookupDisplayNames = %v, want %v", got, tc.want)
				}
				return nil
			}))
		})
	}
}
