package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestAccountStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	id := account(t, pool, "a@example.com", "A")
	store := postgres.NewAccountStore(pool)
	accountID, hash, err := store.AccountCredentials(ctx, "a@example.com")
	if err != nil || accountID != id || hash != "$argon2id$x" {
		t.Fatalf("AccountCredentials = %v, %q, %v", accountID, hash, err)
	}
	if _, _, err := store.AccountCredentials(ctx, "b@example.com"); !errors.Is(err, identity.ErrNoAccount) {
		t.Fatalf("unknown email: %v", err)
	}
}
