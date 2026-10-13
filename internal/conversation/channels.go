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
	CreateChannel(ctx context.Context, organizationID kernel.ID, name, image string) (Channel, error)
	SetChannelImage(ctx context.Context, organizationID, id kernel.ID, image string) error
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

// Create adds a channel. Every member may create one (all channels are
// public in the MVP); it is never the default, and nothing here deletes a
// channel or moves the default, so each organisation keeps exactly one.
func (s *Channels) Create(ctx context.Context, m org.Membership, name string) (Channel, error) {
	return s.CreateWithImage(ctx, m, name, "")
}

// CreateWithImage adds a channel with a bundled image, or its ID-based default.
func (s *Channels) CreateWithImage(ctx context.Context, m org.Membership, name, image string) (Channel, error) {
	if err := ValidateChannelImage(image); err != nil {
		return Channel{}, err
	}
	name, err := ValidateChannelName(name)
	if err != nil {
		return Channel{}, fmt.Errorf("%w: %w", ErrInvalidChannelName, err)
	}
	created, err := s.store.CreateChannel(ctx, m.Organization.ID, name, image)
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

// SetImage changes a channel's illustration within the resolved membership's organisation.
// It writes no event: other members see the image on their next page load.
func (s *Channels) SetImage(ctx context.Context, m org.Membership, id kernel.ID, image string) error {
	if err := ValidateChannelImage(image); err != nil {
		return err
	}
	if err := s.store.SetChannelImage(ctx, m.Organization.ID, id, image); err != nil {
		return fmt.Errorf("changing channel image: %w", err)
	}
	return nil
}
