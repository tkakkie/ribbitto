package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestAccountStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	id := pgtest.Account(t, pool, "a@example.com", "A")
	store := postgres.NewAccountStore(pool)
	account, hash, err := store.AccountCredentials(ctx, "a@example.com")
	if err != nil || account != (identity.Account{ID: id, Email: "a@example.com", DisplayName: "A"}) || hash != "$argon2id$x" {
		t.Fatalf("AccountCredentials = %+v, %q, %v", account, hash, err)
	}
	if _, _, err := store.AccountCredentials(ctx, "b@example.com"); !errors.Is(err, identity.ErrNoAccount) {
		t.Fatalf("unknown email: %v", err)
	}
}
