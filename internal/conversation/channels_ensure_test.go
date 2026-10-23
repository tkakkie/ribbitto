package conversation_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func TestChannelsEnsure(t *testing.T) {
	acme, globex := kernel.ID{1}, kernel.ID{2}
	general := conversation.Channel{ID: kernel.ID{3}, OrganizationID: acme, Name: "general", IsDefault: true}
	random := conversation.Channel{ID: kernel.ID{4}, OrganizationID: acme, Name: "Random"}
	secret := conversation.Channel{ID: kernel.ID{5}, OrganizationID: globex, Name: "secret", IsDefault: true}
	member := org.Membership{Organization: org.Organization{ID: acme}, Member: org.Member{ID: kernel.ID{9}, OrganizationID: acme}}
	for _, tt := range []struct {
		name        string
		input       string
		want        string
		wantID      kernel.ID
		wantCreated int
		wantErr     error
	}{
		{name: "existing channel", input: "general", want: "general", wantID: general.ID},
		{name: "existing channel, other case", input: "RANDOM", want: "Random", wantID: random.ID},
		{name: "existing channel, padded", input: "  general ", want: "general", wantID: general.ID},
		{name: "new channel", input: "雑談", want: "雑談", wantCreated: 1},
		{name: "name of another organisation's channel", input: "secret", want: "secret", wantCreated: 1},
		{name: "invalid name", input: "  ", wantErr: conversation.ErrInvalidChannelName},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &channelStore{channels: map[kernel.ID][]conversation.Channel{acme: {general, random}, globex: {secret}}}
			runner := &postingFake{t: t}
			service := conversation.NewTransactionalChannels(s, runner, func(tx platform.Tx) conversation.ChannelCreator {
				runner.bound(tx)
				return s
			})
			got, err := service.Ensure(t.Context(), member, tt.input)
			if !errors.Is(err, tt.wantErr) || got.Name != tt.want {
				t.Fatalf("Ensure(%q) = %+v, %v; want %q, %v", tt.input, got, err, tt.want, tt.wantErr)
			}
			if err == nil && tt.wantID != (kernel.ID{}) && got.ID != tt.wantID {
				t.Fatalf("Ensure(%q) returned channel %x, want the existing %x", tt.input, got.ID, tt.wantID)
			}
			if len(s.created) != tt.wantCreated {
				t.Fatalf("created %+v, want %d new channels", s.created, tt.wantCreated)
			}
			if err == nil && got.OrganizationID != acme {
				t.Fatalf("Ensure(%q) returned a channel of organisation %x", tt.input, got.OrganizationID)
			}
		})
	}
}
