package conversation_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type channelImageCall struct {
	organizationID, id kernel.ID
	image              string
}

type channelImageStore struct {
	channelStore
	err   error
	calls []channelImageCall
}

func (s *channelImageStore) SetChannelImage(ctx context.Context, organizationID, id kernel.ID, image string) error {
	s.calls = append(s.calls, channelImageCall{organizationID: organizationID, id: id, image: image})
	if s.err != nil {
		return s.err
	}
	return s.channelStore.SetChannelImage(ctx, organizationID, id, image)
}

func TestValidateChannelImage(t *testing.T) {
	type testCase struct {
		name, key string
		wantErr   error
	}
	cases := []testCase{{name: "empty", key: ""}}
	for _, key := range conversation.ChannelImageKeys() {
		cases = append(cases, testCase{name: key, key: key})
	}
	for _, key := range []string{"removed", "Leaf", "../x", "leaf "} {
		cases = append(cases, testCase{name: key, key: key, wantErr: conversation.ErrInvalidChannelImage})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := conversation.ValidateChannelImage(tt.key); err != tt.wantErr {
				t.Fatalf("ValidateChannelImage(%q) = %v; want %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestChannelImage(t *testing.T) {
	acme, globex := kernel.ID{1}, kernel.ID{2}
	general := conversation.Channel{ID: kernel.ID{3}, OrganizationID: acme, Name: "general", Image: "lake", IsDefault: true}
	secret := conversation.Channel{ID: kernel.ID{4}, OrganizationID: globex, Name: "secret", Image: "hills", IsDefault: true}
	member := func(orgID kernel.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: kernel.ID{9}, OrganizationID: orgID}}
	}
	newChannelImageService := func(t *testing.T) (*conversation.Channels, *channelImageStore, *postingFake) {
		s := &channelImageStore{channelStore: channelStore{channels: map[kernel.ID][]conversation.Channel{acme: {general}, globex: {secret}}}}
		runner := &postingFake{t: t}
		return conversation.NewTransactionalChannels(s, runner, func(tx platform.Tx) conversation.ChannelCreator {
			runner.bound(tx)
			return s
		}), s, runner
	}

	t.Run("create with image", func(t *testing.T) {
		for _, tt := range []struct {
			name, input, image, wantName, wantImage string
			wantErr                                 error
		}{
			{name: "good image", input: "  design ", image: "leaf", wantName: "design", wantImage: "leaf"},
			{name: "empty image", input: "random", image: "", wantName: "random", wantImage: ""},
			{name: "bad image", input: "design", image: "removed", wantErr: conversation.ErrInvalidChannelImage},
			// Image validation runs before name validation, returning ErrInvalidChannelImage unwrapped.
			{name: "bad image before bad name", input: "  ", image: "removed", wantErr: conversation.ErrInvalidChannelImage},
		} {
			t.Run(tt.name, func(t *testing.T) {
				service, s, runner := newChannelImageService(t)
				got, err := service.CreateWithImage(t.Context(), member(acme), tt.input, tt.image)
				if tt.wantErr != nil {
					if err != tt.wantErr || got != (conversation.Channel{}) || len(runner.calls) != 0 || len(s.created) != 0 {
						t.Fatalf("got %+v, %v, calls %v, created %+v; want %v with no tx or write", got, err, runner.calls, s.created, tt.wantErr)
					}
					return
				}
				if err != nil || got.Name != tt.wantName || got.Image != tt.wantImage || got.OrganizationID != acme || got.IsDefault {
					t.Fatalf("got %+v, %v; want name %q image %q", got, err, tt.wantName, tt.wantImage)
				}
				if len(s.created) != 1 || s.created[0] != got || !slices.Equal(runner.calls, []string{"begin", "commit"}) {
					t.Fatalf("created %+v, calls %v", s.created, runner.calls)
				}
			})
		}
	})

	t.Run("set image", func(t *testing.T) {
		offline := errors.New("offline")
		for _, tt := range []struct {
			name, image, wantAcme string
			m                     org.Membership
			id                    kernel.ID
			storeErr, wantErr     error
			wantCalls             []channelImageCall
			wantUnwrapped         bool
		}{
			{name: "bundled key", m: member(acme), id: general.ID, image: "leaf", wantAcme: "leaf", wantCalls: []channelImageCall{{organizationID: acme, id: general.ID, image: "leaf"}}},
			{name: "clear key", m: member(acme), id: general.ID, image: "", wantAcme: "", wantCalls: []channelImageCall{{organizationID: acme, id: general.ID, image: ""}}},
			{name: "bad key", m: member(acme), id: general.ID, image: "removed", wantAcme: "lake", wantErr: conversation.ErrInvalidChannelImage, wantUnwrapped: true},
			// The member of another organisation passes acme's channel ID; the write must stay scoped to globex.
			{name: "another organisation's channel", m: member(globex), id: general.ID, image: "leaf", wantAcme: "lake", wantErr: conversation.ErrChannelNotFound, wantCalls: []channelImageCall{{organizationID: globex, id: general.ID, image: "leaf"}}},
			{name: "store failure", m: member(acme), id: general.ID, image: "leaf", wantAcme: "lake", storeErr: offline, wantErr: offline, wantCalls: []channelImageCall{{organizationID: acme, id: general.ID, image: "leaf"}}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				service, s, runner := newChannelImageService(t)
				s.err = tt.storeErr
				err := service.SetImage(t.Context(), tt.m, tt.id, tt.image)
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("SetImage = %v; want %v", err, tt.wantErr)
				}
				if tt.wantUnwrapped && err != tt.wantErr {
					t.Fatalf("SetImage = %v; want unwrapped %v", err, tt.wantErr)
				}
				if tt.wantErr != nil && !tt.wantUnwrapped && (err == nil || !strings.HasPrefix(err.Error(), "changing channel image: ")) {
					t.Fatalf("SetImage = %v; want prefix %q", err, "changing channel image: ")
				}
				if !slices.Equal(s.calls, tt.wantCalls) || len(runner.calls) != 0 {
					t.Fatalf("store calls %+v, runner calls %v; want %+v and no tx/event", s.calls, runner.calls, tt.wantCalls)
				}
				if s.channels[acme][0].Image != tt.wantAcme || s.channels[globex][0].Image != "hills" {
					t.Fatalf("channels acme=%q globex=%q; want %q and %q", s.channels[acme][0].Image, s.channels[globex][0].Image, tt.wantAcme, "hills")
				}
			})
		}
	})
}
