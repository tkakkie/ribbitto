package conversation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// topicReader scopes topics by organisation and channel, as the real store
// does, and records the scope of each lookup.
type topicReader struct {
	topics  []conversation.Topic
	err     error
	lookups [][2]kernel.ID
}

func (r *topicReader) GetTopic(_ context.Context, organizationID, channelID, id kernel.ID) (conversation.Topic, error) {
	r.lookups = append(r.lookups, [2]kernel.ID{organizationID, channelID})
	if r.err != nil {
		return conversation.Topic{}, r.err
	}
	for _, t := range r.topics {
		if t.OrganizationID == organizationID && t.ChannelID == channelID && t.ID == id {
			return t, nil
		}
	}
	return conversation.Topic{}, conversation.ErrTopicNotFound
}

func TestTopicsGet(t *testing.T) {
	acme, globex := kernel.ID{1}, kernel.ID{2}
	general, random := kernel.ID{3}, kernel.ID{4}
	planning := conversation.Topic{ID: kernel.ID{5}, OrganizationID: acme, ChannelID: general, Name: "Planning"}
	member := func(orgID kernel.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: kernel.ID{9}, OrganizationID: orgID}}
	}
	offline := errors.New("offline")
	for _, tt := range []struct {
		name      string
		m         org.Membership
		channelID kernel.ID
		storeErr  error
		want      error
	}{
		{name: "own topic", m: member(acme), channelID: general},
		// The member of another organisation passes acme's channel and topic
		// IDs; the lookup must still be scoped to globex.
		{name: "another organisation's topic", m: member(globex), channelID: general, want: conversation.ErrTopicNotFound},
		{name: "another channel's topic", m: member(acme), channelID: random, want: conversation.ErrTopicNotFound},
		{name: "store failure", m: member(acme), channelID: general, storeErr: offline, want: offline},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := &topicReader{topics: []conversation.Topic{planning}, err: tt.storeErr}
			got, err := conversation.NewTopics(reader).Get(t.Context(), tt.m, tt.channelID, planning.ID)
			if tt.want == nil && (err != nil || got != planning) {
				t.Fatalf("Get = %+v, %v; want %+v", got, err, planning)
			}
			if tt.want != nil && (!errors.Is(err, tt.want) || got != (conversation.Topic{})) {
				t.Fatalf("Get = %+v, %v; want only %v", got, err, tt.want)
			}
			if len(reader.lookups) != 1 || reader.lookups[0] != [2]kernel.ID{tt.m.Organization.ID, tt.channelID} {
				t.Fatalf("lookups %v; want one, scoped to the membership's organisation and the channel", reader.lookups)
			}
		})
	}
}
