package conversationpg_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func fixtureOrganization(t *testing.T, pool *pgxpool.Pool, slug string) kernel.ID {
	t.Helper()
	var id kernel.ID
	if err := pool.QueryRow(t.Context(), "INSERT INTO organization (slug, name) VALUES ($1, $1) RETURNING id", slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func fixtureDefaultChannel(t *testing.T, pool *pgxpool.Pool, organizationID kernel.ID) {
	t.Helper()
	// The deferred channel/topic relationship must be complete in one statement.
	_, err := pool.Exec(t.Context(), `WITH channel AS (
        INSERT INTO channel (organization_id, name, is_default)
        VALUES ($1, $2, true) RETURNING organization_id, id, default_topic_id
    ) INSERT INTO topic (organization_id, channel_id, id, is_default)
      SELECT organization_id, id, default_topic_id, true FROM channel`, organizationID, conversation.DefaultChannelName)
	if err != nil {
		t.Fatal(err)
	}
}

// The use cases see only the member's organisation, even with a known id.
func TestChannels(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := fixtureOrganization(t, pool, "acme")
	globex := fixtureOrganization(t, pool, "globex")
	member := func(orgID kernel.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{OrganizationID: orgID}}
	}
	service := conversationpg.NewChannels(pool)
	for _, org := range []kernel.ID{acme, globex} {
		fixtureDefaultChannel(t, pool, org)
	}
	secret, err := service.Create(ctx, member(globex), " 開発 ")
	if err != nil || secret.Name != "開発" || secret.IsDefault {
		t.Fatalf("create: %+v, %v", secret, err)
	}
	if _, err := service.Create(ctx, member(globex), "開発"); !errors.Is(err, conversation.ErrChannelNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := service.Create(ctx, member(acme), "開発"); err != nil {
		t.Fatalf("same name in another organisation: %v", err)
	}
	if _, err := service.Get(ctx, member(acme), secret.ID); !errors.Is(err, conversation.ErrChannelNotFound) {
		t.Fatalf("another organisation's channel by id: %v", err)
	}
	if got, err := service.Default(ctx, member(globex)); err != nil || got.Name != conversation.DefaultChannelName || got.OrganizationID != globex {
		t.Fatalf("default: %+v, %v", got, err)
	}
}
