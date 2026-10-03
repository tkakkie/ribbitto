package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// account inserts an account with a placeholder hash, as the legacy
// pgtest.Account fixture does; identity's tests own their fixtures.
func account(t *testing.T, pool *pgxpool.Pool, email, name string) kernel.ID {
	t.Helper()
	var id kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), "INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, '$argon2id$x') RETURNING id", email, name).Scan(&id))
	return id
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
