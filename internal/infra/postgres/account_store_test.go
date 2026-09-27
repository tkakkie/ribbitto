package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestAccountStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	var id domain.ID
	if err := pool.QueryRow(ctx, "INSERT INTO account (email, display_name, password_hash) VALUES ('a@example.com', 'A', '$argon2id$x') RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	store := postgres.NewAccountStore(pool)
	account, hash, err := store.AccountCredentials(ctx, "a@example.com")
	if err != nil || account != (domain.Account{ID: id, Email: "a@example.com", DisplayName: "A"}) || hash != "$argon2id$x" {
		t.Fatalf("AccountCredentials = %+v, %q, %v", account, hash, err)
	}
	if _, _, err := store.AccountCredentials(ctx, "b@example.com"); !errors.Is(err, auth.ErrNoAccount) {
		t.Fatalf("unknown email: %v", err)
	}
}
