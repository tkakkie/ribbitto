package conversation_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestChannelsGetOrDefault(t *testing.T) {
	acme, globex := kernel.ID{1}, kernel.ID{2}
	general := conversation.Channel{ID: kernel.ID{3}, OrganizationID: acme, Name: "general", IsDefault: true}
	random := conversation.Channel{ID: kernel.ID{5}, OrganizationID: acme, Name: "random"}
	secret := conversation.Channel{ID: kernel.ID{4}, OrganizationID: globex, Name: "secret", IsDefault: true}
	store := &channelStore{channels: map[kernel.ID][]conversation.Channel{acme: {general, random}, globex: {secret}}}
	channels := conversation.NewTransactionalChannels(store, nil, nil)
	member := org.Membership{Organization: org.Organization{ID: acme}, Member: org.Member{ID: kernel.ID{9}, OrganizationID: acme}}

	for _, tt := range []struct {
		name string
		id   kernel.ID
		want conversation.Channel
	}{
		{"no channel named", kernel.ID{}, general},
		{"default named", general.ID, general},
		{"another channel named", random.ID, random},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := channels.GetOrDefault(t.Context(), member, tt.id)
			if err != nil || got.ID != tt.want.ID {
				t.Fatalf("GetOrDefault = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}
