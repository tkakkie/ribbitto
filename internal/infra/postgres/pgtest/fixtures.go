package pgtest

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

// ChannelFixture holds a channel and the default topic created with it.
type ChannelFixture struct {
	ID, OrganizationID, DefaultTopicID domain.ID
	Name                               string
	IsDefault                          bool
	CreatedAt                          time.Time
}

// OrganizationFixture holds the IDs and default channel created by OrganizationWithOwner.
type OrganizationFixture struct {
	OrganizationID domain.ID
	AccountID      domain.ID
	MemberID       domain.ID
	Channel        ChannelFixture
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

// Channel inserts a channel and its default topic with the given name and default flag.
// It leaves event_seq unchanged and creates no accounts or memberships.
func Channel(t *testing.T, pool *pgxpool.Pool, org domain.ID, name string, isDefault bool) ChannelFixture {
	t.Helper()
	var channel ChannelFixture
	// One statement satisfies the channel's deferred foreign key even on a pool.
	require(t, pool.QueryRow(t.Context(), `
WITH created AS (
  INSERT INTO channel (organization_id, name, is_default)
  VALUES ($1, $2, $3) RETURNING *
), default_topic AS (
  INSERT INTO topic (id, organization_id, channel_id, is_default)
  SELECT default_topic_id, organization_id, id, true FROM created
)
SELECT id, organization_id, default_topic_id, name, is_default, created_at FROM created;
`, org, name, isDefault).Scan(&channel.ID, &channel.OrganizationID, &channel.DefaultTopicID, &channel.Name, &channel.IsDefault, &channel.CreatedAt))
	return channel
}
