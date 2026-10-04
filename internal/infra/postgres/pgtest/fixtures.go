package pgtest

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/org"
)

// OrganizationFixture holds the IDs and default channel created by OrganizationWithOwner.
type OrganizationFixture struct {
	OrganizationID domain.ID
	AccountID      domain.ID
	MemberID       domain.ID
	Channel        conversation.Channel
}

// OrganizationWithOwner creates an organisation with event_seq 1, an owner
// account at <slug>@example.org with the placeholder hash $argon2id$x, and an
// owner membership with handle "owner" and joined_event_seq 1. The organisation
// and account display names equal slug. It creates exactly one default channel
// with channelName, leaves event_seq at 1, and creates no setup row.
func OrganizationWithOwner(t *testing.T, pool *pgxpool.Pool, slug, channelName string) OrganizationFixture {
	t.Helper()
	orgID := Organization(t, pool, slug, slug, 1)
	account := Account(t, pool, slug+"@example.org", slug)
	member := Member(t, pool, orgID, account, org.RoleOwner, "owner", 1)
	channel := Channel(t, pool, orgID, channelName, true)
	return OrganizationFixture{OrganizationID: orgID, AccountID: account, MemberID: member, Channel: channel}
}

// Organization inserts only an organisation, with the given event_seq.
// Compose it with Account, Member and Channel for a full fixture without setup.
func Organization(t *testing.T, pool *pgxpool.Pool, slug, name string, eventSeq int64) domain.ID {
	t.Helper()
	var id domain.ID
	require(t, pool.QueryRow(t.Context(), "INSERT INTO organization (slug, name, event_seq) VALUES ($1, $2, $3) RETURNING id", slug, name, eventSeq).Scan(&id))
	return id
}

// Account inserts an account with the placeholder hash $argon2id$x and no membership.
func Account(t *testing.T, pool *pgxpool.Pool, email, name string) domain.ID {
	t.Helper()
	var id domain.ID
	require(t, pool.QueryRow(t.Context(), "INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, '$argon2id$x') RETURNING id", email, name).Scan(&id))
	return id
}

// Member inserts a membership with the given role, handle and joined_event_seq.
// It leaves the organisation's event_seq unchanged, including for partial fixtures.
func Member(t *testing.T, pool *pgxpool.Pool, orgID, account domain.ID, role org.Role, handle string, joinedEventSeq int64) domain.ID {
	t.Helper()
	var id domain.ID
	require(t, pool.QueryRow(t.Context(), "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $2, $3, $4, $5) RETURNING id", orgID, account, role, joinedEventSeq, handle).Scan(&id))
	return id
}

// Channel creates a channel with the given name and default flag through the store.
// It leaves event_seq unchanged and creates no accounts or memberships.
func Channel(t *testing.T, pool *pgxpool.Pool, org domain.ID, name string, isDefault bool) conversation.Channel {
	t.Helper()
	channel, err := postgres.NewChannelStore(pool).CreateChannel(t.Context(), org, name, isDefault)
	require(t, err)
	return channel
}
