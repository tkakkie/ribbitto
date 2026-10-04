package channel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

// store keeps channels per organisation, as the real store scopes them.
type store struct {
	channels map[domain.ID][]conversation.Channel
	created  []conversation.Channel
}

func (s *store) ListChannels(_ context.Context, org domain.ID) ([]conversation.Channel, error) {
	return s.channels[org], nil
}

func (s *store) CreateChannel(_ context.Context, org domain.ID, name string, isDefault bool) (conversation.Channel, error) {
	for _, c := range s.channels[org] {
		if c.Name == name {
			return conversation.Channel{}, conversation.ErrChannelNameTaken
		}
	}
	c := conversation.Channel{ID: domain.ID{byte(len(s.created) + 10)}, OrganizationID: org, Name: name, IsDefault: isDefault}
	s.created = append(s.created, c)
	s.channels[org] = append(s.channels[org], c)
	return c, nil
}

func (s *store) GetChannel(_ context.Context, org, id domain.ID) (conversation.Channel, error) {
	for _, c := range s.channels[org] {
		if c.ID == id {
			return c, nil
		}
	}
	return conversation.Channel{}, conversation.ErrChannelNotFound
}

func (s *store) GetDefaultChannel(_ context.Context, org domain.ID) (conversation.Channel, error) {
	for _, c := range s.channels[org] {
		if c.IsDefault {
			return c, nil
		}
	}
	return conversation.Channel{}, conversation.ErrChannelNotFound
}

func TestChannels(t *testing.T) {
	acme, globex := domain.ID{1}, domain.ID{2}
	general := conversation.Channel{ID: domain.ID{3}, OrganizationID: acme, Name: "general", IsDefault: true}
	secret := conversation.Channel{ID: domain.ID{4}, OrganizationID: globex, Name: "secret", IsDefault: true}
	member := func(orgID domain.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: domain.ID{9}, OrganizationID: orgID}}
	}
	newService := func() (*channel.Service, *store) {
		s := &store{channels: map[domain.ID][]conversation.Channel{acme: {general}, globex: {secret}}}
		return channel.New(s), s
	}

	t.Run("create", func(t *testing.T) {
		for _, tt := range []struct {
			name, input, want string
			wantErr           error
		}{
			{name: "normalised", input: "  雑談 ", want: "雑談"},
			{name: "duplicate", input: "general", wantErr: conversation.ErrChannelNameTaken},
			{name: "empty", input: "  ", wantErr: conversation.ErrInvalidChannelName},
			{name: "too long", input: strings.Repeat("界", 81), wantErr: conversation.ErrInvalidChannelName},
			{name: "same name as another organisation's", input: "secret", want: "secret"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				service, s := newService()
				got, err := service.Create(t.Context(), member(acme), tt.input)
				if !errors.Is(err, tt.wantErr) || got.Name != tt.want {
					t.Fatalf("got %+v, %v; want %q, %v", got, err, tt.want, tt.wantErr)
				}
				if err == nil && (got.IsDefault || got.OrganizationID != acme || len(s.created) != 1) {
					t.Fatalf("created %+v", s.created)
				}
				if err != nil && len(s.created) != 0 {
					t.Fatalf("a rejected channel was stored: %+v", s.created)
				}
			})
		}
	})

	t.Run("another organisation's channel is not found by id", func(t *testing.T) {
		service, _ := newService()
		if _, err := service.Get(t.Context(), member(acme), secret.ID); !errors.Is(err, conversation.ErrChannelNotFound) {
			t.Fatalf("got %v", err)
		}
		if got, err := service.Get(t.Context(), member(acme), general.ID); err != nil || got != general {
			t.Fatalf("own channel: %+v, %v", got, err)
		}
	})

	t.Run("list and default stay in the member's organisation", func(t *testing.T) {
		service, _ := newService()
		list, err := service.List(t.Context(), member(globex))
		if err != nil || len(list) != 1 || list[0] != secret {
			t.Fatalf("list: %+v, %v", list, err)
		}
		if got, err := service.Default(t.Context(), member(acme)); err != nil || got != general {
			t.Fatalf("default: %+v, %v", got, err)
		}
	})

	t.Run("a missing default is an error, not a plain not-found", func(t *testing.T) {
		service, s := newService()
		s.channels[acme] = nil
		_, err := service.Default(t.Context(), member(acme))
		if err == nil || errors.Is(err, conversation.ErrChannelNotFound) {
			t.Fatalf("got %v", err)
		}
	})
}
