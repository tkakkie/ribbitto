package conversationtest

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
)

// ChannelFixture holds a channel and the default topic created with it.
type ChannelFixture struct {
	ID, OrganizationID, DefaultTopicID kernel.ID
	Name                               string
	IsDefault                          bool
	CreatedAt                          time.Time
}

// TopicFixture holds an ordinary named topic.
type TopicFixture struct {
	ID, OrganizationID, ChannelID kernel.ID
	Name                          string
	CreatedAt                     time.Time
}

// OrganizationFixture holds the IDs and default channel created by OrganizationWithOwner.
type OrganizationFixture struct {
	OrganizationID kernel.ID
	AccountID      kernel.ID
	MemberID       kernel.ID
	Channel        ChannelFixture
}

// OrganizationWithOwner creates an organisation with event_seq 1, an owner
// account at <slug>@example.org with the placeholder hash $argon2id$x, and an
// owner membership with handle "owner" and joined_event_seq 1. The organisation
// and account display names equal slug. It creates exactly one default channel
// with channelName, leaves event_seq at 1, and creates no setup row.
func OrganizationWithOwner(t *testing.T, pool *pgxpool.Pool, slug, channelName string) OrganizationFixture {
	t.Helper()
	orgID := orgtest.Organization(t, pool, slug, slug, 1)
	account := identitytest.Account(t, pool, slug+"@example.org", slug)
	member := orgtest.Member(t, pool, orgID, account, org.RoleOwner, "owner", 1)
	channel := Channel(t, pool, orgID, channelName, true)
	return OrganizationFixture{OrganizationID: orgID, AccountID: account, MemberID: member, Channel: channel}
}

// Channel inserts a channel and its default topic with the given name and default flag.
// It leaves event_seq unchanged and creates no accounts or memberships.
func Channel(t *testing.T, pool *pgxpool.Pool, org kernel.ID, name string, isDefault bool) ChannelFixture {
	t.Helper()
	var channel ChannelFixture
	// One statement satisfies the channel's deferred foreign key even on a pool.
	if err := pool.QueryRow(t.Context(), `
WITH created AS (
  INSERT INTO channel (organization_id, name, is_default)
  VALUES ($1, $2, $3) RETURNING *
), default_topic AS (
  INSERT INTO topic (id, organization_id, channel_id, is_default)
  SELECT default_topic_id, organization_id, id, true FROM created
)
SELECT id, organization_id, default_topic_id, name, is_default, created_at FROM created;
`, org, name, isDefault).Scan(&channel.ID, &channel.OrganizationID, &channel.DefaultTopicID, &channel.Name, &channel.IsDefault, &channel.CreatedAt); err != nil {
		t.Fatalf("inserting channel: %v", err)
	}
	return channel
}

// Topic inserts one named, non-default topic in the given organisation and channel.
// It leaves event_seq and messages unchanged.
func Topic(t *testing.T, pool *pgxpool.Pool, organizationID, channelID kernel.ID, name string) TopicFixture {
	t.Helper()
	var topic TopicFixture
	if err := pool.QueryRow(t.Context(), `
INSERT INTO topic (organization_id, channel_id, name, is_default)
VALUES ($1, $2, $3, false)
RETURNING id, organization_id, channel_id, name, created_at;
`, organizationID, channelID, name).Scan(&topic.ID, &topic.OrganizationID, &topic.ChannelID, &topic.Name, &topic.CreatedAt); err != nil {
		t.Fatalf("inserting topic: %v", err)
	}
	return topic
}
