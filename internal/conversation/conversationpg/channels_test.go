package conversationpg_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// The use cases see only the member's organisation, even with a known id.
func TestChannels(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := orgtest.Organization(t, pool, "acme", "acme", 0)
	globex := orgtest.Organization(t, pool, "globex", "globex", 0)
	member := func(orgID kernel.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{OrganizationID: orgID}}
	}
	service := conversationpg.NewChannels(pool)
	for _, org := range []kernel.ID{acme, globex} {
		conversationtest.Channel(t, pool, org, conversation.DefaultChannelName, true)
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
