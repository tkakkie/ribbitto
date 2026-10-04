package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// ChannelStore reads and writes channels, always within one organisation. Lookups
// return ErrChannelNotFound; CreateChannel returns ErrChannelNameTaken for a duplicate.
type ChannelStore interface {
	ListChannels(ctx context.Context, organizationID kernel.ID) ([]Channel, error)
	CreateChannel(ctx context.Context, organizationID kernel.ID, name string, isDefault bool) (Channel, error)
	GetChannel(ctx context.Context, organizationID, id kernel.ID) (Channel, error)
	GetDefaultChannel(ctx context.Context, organizationID kernel.ID) (Channel, error)
}

// Channels runs the channel use cases for a member resolved by org.
type Channels struct {
	store ChannelStore
}

// NewChannels returns a Channels.
func NewChannels(store ChannelStore) *Channels {
	return &Channels{store: store}
}

// List returns the channels of the member's organisation.
func (s *Channels) List(ctx context.Context, m org.Membership) ([]Channel, error) {
	channels, err := s.store.ListChannels(ctx, m.Organization.ID)
	if err != nil {
		return nil, fmt.Errorf("listing channels: %w", err)
	}
	return channels, nil
}

// Create adds a channel. Every member may create one (all channels are
// public in the MVP); it is never the default, and nothing here deletes a
// channel or moves the default, so each organisation keeps exactly one.
func (s *Channels) Create(ctx context.Context, m org.Membership, name string) (Channel, error) {
	name, err := ValidateChannelName(name)
	if err != nil {
		return Channel{}, fmt.Errorf("%w: %w", ErrInvalidChannelName, err)
	}
	created, err := s.store.CreateChannel(ctx, m.Organization.ID, name, false)
	if err != nil {
		return Channel{}, fmt.Errorf("creating channel: %w", err)
	}
	return created, nil
}

// Get returns a channel of the member's organisation by id, or ErrChannelNotFound.
func (s *Channels) Get(ctx context.Context, m org.Membership, id kernel.ID) (Channel, error) {
	found, err := s.store.GetChannel(ctx, m.Organization.ID, id)
	if err != nil {
		return Channel{}, fmt.Errorf("finding channel: %w", err)
	}
	return found, nil
}

// Default returns the member's organisation's default channel. Its absence
// is a broken invariant: the error deliberately does not wrap ErrChannelNotFound,
// so a caller answers it as a server error, not as a 404 that would hide it.
func (s *Channels) Default(ctx context.Context, m org.Membership) (Channel, error) {
	found, err := s.store.GetDefaultChannel(ctx, m.Organization.ID)
	if errors.Is(err, ErrChannelNotFound) {
		return Channel{}, fmt.Errorf("organisation %x has no default channel", m.Organization.ID)
	}
	if err != nil {
		return Channel{}, fmt.Errorf("finding default channel: %w", err)
	}
	return found, nil
}
