package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func fixtureAccount(t *testing.T, pool *pgxpool.Pool, email, name string) (id kernel.ID) {
	t.Helper()
	fixture(t, pool, "INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, '$argon2id$x') RETURNING id", []any{email, name}, &id)
	return id
}

func fixtureMember(t *testing.T, pool *pgxpool.Pool, organizationID, accountID kernel.ID, role org.Role, handle string, seq int64) (id kernel.ID) {
	t.Helper()
	fixture(t, pool, "INSERT INTO member (organization_id, account_id, role, handle, joined_event_seq) VALUES ($1, $2, $3, $4, $5) RETURNING id", []any{organizationID, accountID, role, handle, seq}, &id)
	return id
}

func fixtureChannel(t *testing.T, pool *pgxpool.Pool, organizationID kernel.ID, name string, isDefault bool) conversation.Channel {
	t.Helper()
	var id, topic kernel.ID
	fixture(t, pool, channelSQL, []any{organizationID, name}, &id, &topic)
	_, err := pool.Exec(t.Context(), "UPDATE channel SET is_default = $2 WHERE id = $1", id, isDefault)
	requireNoError(t, err)
	channel, err := postgres.NewChannelStore(pool).GetChannel(t.Context(), organizationID, id)
	requireNoError(t, err)
	return channel
}

func fixtureOrganizationWithOwner(t *testing.T, pool *pgxpool.Pool, slug, name string) (f struct {
	OrganizationID, MemberID kernel.ID
	Channel                  conversation.Channel
}) {
	t.Helper()
	fixture(t, pool, "INSERT INTO organization (slug, name, event_seq) VALUES ($1, $1, 1) RETURNING id", []any{slug}, &f.OrganizationID)
	f.MemberID = fixtureMember(t, pool, f.OrganizationID, fixtureAccount(t, pool, slug+"@example.org", slug), org.RoleOwner, "owner", 1)
	f.Channel = fixtureChannel(t, pool, f.OrganizationID, name, true)
	return f
}
