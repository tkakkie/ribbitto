package channel

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

// DefaultName is the initial name of an organisation's default channel. It
// is only a name: the default is found by its is_default flag.
const DefaultName = "general"

// ErrNotFound means the channel does not exist in the caller's organisation,
// including when it exists in another one.
var ErrNotFound = errors.New("channel not found")

// ErrInvalidName wraps a name that breaks domain.ValidateChannelName.
var ErrInvalidName = errors.New("invalid channel name")

// ErrNameTaken means the organisation already has a channel with that name.
// Names are unique only because channels sit flat under the organisation;
// the id, not the name, identifies a channel.
var ErrNameTaken = errors.New("channel name already taken")

// Store reads and writes channels, always within one organisation. Lookups
// return ErrNotFound; CreateChannel returns ErrNameTaken for a duplicate.
type Store interface {
	ListChannels(ctx context.Context, organizationID domain.ID) ([]domain.Channel, error)
	CreateChannel(ctx context.Context, organizationID domain.ID, name string, isDefault bool) (domain.Channel, error)
	GetChannel(ctx context.Context, organizationID, id domain.ID) (domain.Channel, error)
	GetDefaultChannel(ctx context.Context, organizationID domain.ID) (domain.Channel, error)
}

// Service runs the channel use cases for a member resolved by org.
type Service struct {
	store Store
}

// New returns a Service.
func New(store Store) *Service {
	return &Service{store: store}
}

// List returns the channels of the member's organisation.
func (s *Service) List(ctx context.Context, m org.Membership) ([]domain.Channel, error) {
	channels, err := s.store.ListChannels(ctx, m.Organization.ID)
	if err != nil {
		return nil, fmt.Errorf("listing channels: %w", err)
	}
	return channels, nil
}

// Create adds a channel. Every member may create one (all channels are
// public in the MVP); it is never the default, and nothing here deletes a
// channel or moves the default, so each organisation keeps exactly one.
func (s *Service) Create(ctx context.Context, m org.Membership, name string) (domain.Channel, error) {
	name, err := domain.ValidateChannelName(name)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("%w: %w", ErrInvalidName, err)
	}
	created, err := s.store.CreateChannel(ctx, m.Organization.ID, name, false)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("creating channel: %w", err)
	}
	return created, nil
}

// Get returns a channel of the member's organisation by id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, m org.Membership, id domain.ID) (domain.Channel, error) {
	found, err := s.store.GetChannel(ctx, m.Organization.ID, id)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("finding channel: %w", err)
	}
	return found, nil
}

// Default returns the member's organisation's default channel. Its absence
// is a broken invariant: the error deliberately does not wrap ErrNotFound,
// so a caller answers it as a server error, not as a 404 that would hide it.
func (s *Service) Default(ctx context.Context, m org.Membership) (domain.Channel, error) {
	found, err := s.store.GetDefaultChannel(ctx, m.Organization.ID)
	if errors.Is(err, ErrNotFound) {
		return domain.Channel{}, fmt.Errorf("organisation %x has no default channel", m.Organization.ID)
	}
	if err != nil {
		return domain.Channel{}, fmt.Errorf("finding default channel: %w", err)
	}
	return found, nil
}
