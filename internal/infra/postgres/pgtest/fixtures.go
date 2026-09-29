package pgtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
)

// Organization inserts only an organisation, with the given event_seq.
// Compose it with Account, Member and Channel for a full fixture without setup.
func Organization(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug, name string, eventSeq int64) domain.ID {
	t.Helper()
	var id domain.ID
	require(t, pool.QueryRow(ctx, "INSERT INTO organization (slug, name, event_seq) VALUES ($1, $2, $3) RETURNING id", slug, name, eventSeq).Scan(&id))
	return id
}

// Account inserts an account with the placeholder hash $argon2id$x and no membership.
func Account(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email, name string) domain.ID {
	t.Helper()
	var id domain.ID
	require(t, pool.QueryRow(ctx, "INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, '$argon2id$x') RETURNING id", email, name).Scan(&id))
	return id
}

// Member inserts a membership with the given role, handle and joined_event_seq.
// It leaves the organisation's event_seq unchanged, including for partial fixtures.
func Member(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, account domain.ID, role domain.Role, handle string, joinedEventSeq int64) domain.ID {
	t.Helper()
	var id domain.ID
	require(t, pool.QueryRow(ctx, "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $2, $3, $4, $5) RETURNING id", org, account, role, joinedEventSeq, handle).Scan(&id))
	return id
}

// Channel creates a channel with the given name and default flag through the store.
// It leaves event_seq unchanged and creates no accounts or memberships.
func Channel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org domain.ID, name string, isDefault bool) domain.Channel {
	t.Helper()
	channel, err := postgres.NewChannelStore(pool).CreateChannel(ctx, org, name, isDefault)
	require(t, err)
	return channel
}
