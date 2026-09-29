package message

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// ErrInvalidBody wraps a body that breaks domain.ValidateMessageBody.
var ErrInvalidBody = errors.New("invalid message body")

// Store posts a message atomically: in one transaction it takes the
// organisation's next event_seq first, then inserts the message with it. A
// channel that is not in the organisation is channel.ErrNotFound, and the
// sequence is not consumed.
type Store interface {
	Post(ctx context.Context, organizationID, channelID, memberID domain.ID, body string) (domain.Message, error)
}

// Service runs the message use cases for a member resolved by authz.
type Service struct {
	store Store
}

// New returns a Service.
func New(store Store) *Service {
	return &Service{store: store}
}

// Post writes body to the channel as the member. The organisation and the
// author come from the membership, never from the request; the channel id
// does, and is checked against that organisation.
func (s *Service) Post(ctx context.Context, m authz.Membership, channelID domain.ID, body string) (domain.Message, error) {
	body, err := domain.ValidateMessageBody(body)
	if err != nil {
		return domain.Message{}, fmt.Errorf("%w: %w", ErrInvalidBody, err)
	}
	posted, err := s.store.Post(ctx, m.Organization.ID, channelID, m.Member.ID, body)
	if err != nil {
		return domain.Message{}, fmt.Errorf("posting message: %w", err)
	}
	return posted, nil
}
