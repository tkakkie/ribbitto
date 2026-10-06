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

// The topic lookup sees only the membership's organisation and the given
// channel, even with a real topic's ID.
func TestTopicsGet(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := orgtest.Organization(t, pool, "acme", "acme", 0)
	globex := orgtest.Organization(t, pool, "globex", "globex", 0)
	general := conversationtest.Channel(t, pool, acme, "general", true).ID
	random := conversationtest.Channel(t, pool, acme, "random", false).ID
	planning := conversationtest.Topic(t, pool, acme, general, "Planning")
	member := func(orgID kernel.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{OrganizationID: orgID}}
	}
	topics := conversationpg.NewTopics(pool)

	got, err := topics.Get(ctx, member(acme), general, planning.ID)
	if err != nil || got.ID != planning.ID || got.OrganizationID != acme || got.ChannelID != general ||
		got.Name != planning.Name || got.IsDefault || !got.CreatedAt.Equal(planning.CreatedAt) {
		t.Fatalf("own topic = %+v, %v; want %+v", got, err, planning)
	}
	for _, tt := range []struct {
		name      string
		m         org.Membership
		channelID kernel.ID
		topicID   kernel.ID
	}{
		{"another organisation's member", member(globex), general, planning.ID},
		{"another channel", member(acme), random, planning.ID},
		{"unknown topic", member(acme), general, kernel.ID{0xee}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := topics.Get(ctx, tt.m, tt.channelID, tt.topicID); !errors.Is(err, conversation.ErrTopicNotFound) {
				t.Fatalf("Get = %+v, %v; want ErrTopicNotFound", got, err)
			}
		})
	}
}
