package identitytest

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Account inserts an account with the placeholder hash $argon2id$x and no membership.
func Account(t *testing.T, pool *pgxpool.Pool, email, name string) kernel.ID {
	t.Helper()
	var id kernel.ID
	if err := pool.QueryRow(t.Context(), "INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, '$argon2id$x') RETURNING id", email, name).Scan(&id); err != nil {
		t.Fatalf("inserting account: %v", err)
	}
	return id
}

// SetDisplayName changes an account's display name.
func SetDisplayName(t *testing.T, pool *pgxpool.Pool, accountID kernel.ID, name string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), "UPDATE account SET display_name=$2 WHERE id=$1", accountID, name); err != nil {
		t.Fatalf("setting display name: %v", err)
	}
}
