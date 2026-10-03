package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// account inserts an account with a placeholder hash, as the legacy
// pgtest.Account fixture does; identity's tests own their fixtures.
func account(t *testing.T, pool *pgxpool.Pool, email, name string) domain.ID {
	t.Helper()
	var id domain.ID
	requireNoError(t, pool.QueryRow(t.Context(), "INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, '$argon2id$x') RETURNING id", email, name).Scan(&id))
	return id
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
