package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestAccountStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	id := identitytest.Account(t, pool, "a@example.com", "A")
	store := postgres.NewAccountStore(pool)
	accountID, hash, err := store.AccountCredentials(ctx, "a@example.com")
	if err != nil || accountID != id || hash != "$argon2id$x" {
		t.Fatalf("AccountCredentials = %v, %q, %v", accountID, hash, err)
	}
	if _, _, err := store.AccountCredentials(ctx, "b@example.com"); !errors.Is(err, identity.ErrNoAccount) {
		t.Fatalf("unknown email: %v", err)
	}
}

func TestRehashPasswordCompareAndSet(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	id := identitytest.Account(t, pool, "a@example.com", "A")
	identitytest.Account(t, pool, "b@example.com", "B")
	store := postgres.NewAccountStore(pool)
	for _, tc := range []struct{ oldHash, newHash, want string }{
		{"$argon2id$x", "$argon2id$new", "$argon2id$new"},
		{"$argon2id$x", "$argon2id$stale", "$argon2id$new"},
	} {
		if err := store.RehashPassword(t.Context(), id, tc.oldHash, tc.newHash); err != nil {
			t.Fatal(err)
		}
		_, got, err := store.AccountCredentials(t.Context(), "a@example.com")
		if err != nil || got != tc.want {
			t.Fatalf("stored hash = %q, %v; want %q", got, err, tc.want)
		}
	}
	_, got, err := store.AccountCredentials(t.Context(), "b@example.com")
	if err != nil || got != "$argon2id$x" {
		t.Fatalf("other account hash = %q, %v; want fixture hash", got, err)
	}
}
