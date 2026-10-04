package conversationpg_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// The topic lookup sees only the membership's organisation and the given
// channel, even with a real topic's ID.
func TestTopicsGet(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	query := func(sql string, args []any, dest ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dest...); err != nil {
			t.Fatal(err)
		}
	}
	var acme, globex, general, random kernel.ID
	query("INSERT INTO organization (slug, name) VALUES ('acme', 'acme') RETURNING id", nil, &acme)
	query("INSERT INTO organization (slug, name) VALUES ('globex', 'globex') RETURNING id", nil, &globex)
	// A channel and its default topic in one statement: the channel's foreign
	// key to the topic is deferred to commit.
	for _, ch := range []struct {
		name string
		id   *kernel.ID
	}{{"general", &general}, {"random", &random}} {
		query(`WITH channel AS (
			INSERT INTO channel (organization_id, name, is_default) VALUES ($1, $2, $2 = 'general')
			RETURNING organization_id, id, default_topic_id
		), topic AS (
			INSERT INTO topic (organization_id, channel_id, id, is_default)
			SELECT organization_id, id, default_topic_id, true FROM channel
		) SELECT id FROM channel`, []any{acme, ch.name}, ch.id)
	}
	planning := conversation.Topic{OrganizationID: acme, ChannelID: general, Name: "Planning"}
	query("INSERT INTO topic (organization_id, channel_id, name, is_default) VALUES ($1, $2, $3, false) RETURNING id, created_at",
		[]any{acme, general, planning.Name}, &planning.ID, &planning.CreatedAt)
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
