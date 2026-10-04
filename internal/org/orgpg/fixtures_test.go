package orgpg_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func fixtureOrganization(t *testing.T, pool *pgxpool.Pool, slug, name string, seq int64) kernel.ID {
	t.Helper()
	var id kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), "INSERT INTO organization (slug, name, event_seq) VALUES ($1, $2, $3) RETURNING id", slug, name, seq).Scan(&id))
	return id
}

func fixtureAccount(t *testing.T, pool *pgxpool.Pool, email, name string) kernel.ID {
	t.Helper()
	var id kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), "INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, '$argon2id$x') RETURNING id", email, name).Scan(&id))
	return id
}

func fixtureMember(t *testing.T, pool *pgxpool.Pool, organizationID, accountID kernel.ID, role org.Role, handle string, seq int64) kernel.ID {
	t.Helper()
	var id kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $2, $3, $4, $5) RETURNING id", organizationID, accountID, role, seq, handle).Scan(&id))
	return id
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
