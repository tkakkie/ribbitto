package message

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

// ErrInvalidBody wraps a body that breaks domain.ValidateMessageBody.
var ErrInvalidBody = errors.New("invalid message body")

// Store posts a message atomically: in one transaction it takes the
// organisation's next event_seq first, then inserts the message and event with it. A
// channel that is not in the organisation is channel.ErrNotFound, and the
// sequence is not consumed. A mismatched topic is topic.ErrNotFound.
// Success means the transaction has committed; nil topicID selects the default.
type Store interface {
	PostToTopic(ctx context.Context, organizationID, channelID, memberID domain.ID, topicID *domain.ID, body string) (domain.Message, error)
}

// Notifier records the latest committed event sequence for an organisation.
type Notifier interface {
	Raise(organizationID domain.ID, seq int64)
}

// Service runs the message use cases for a member resolved by org.
type Service struct {
	store    Store
	notifier Notifier
}

// New returns a Service without commit notifications, for example for seeding.
func New(store Store) *Service {
	return &Service{store: store}
}

// NewWithNotifier returns a Service that notifies after each committed post.
// A nil notifier disables notifications, as with New.
func NewWithNotifier(store Store, notifier Notifier) *Service {
	return &Service{store: store, notifier: notifier}
}

// Post writes body to the channel as the member. The organisation and the
// author come from the membership, never from the request; the channel id
// does, and is checked against that organisation.
func (s *Service) Post(ctx context.Context, m org.Membership, channelID domain.ID, body string) (domain.Message, error) {
	return s.PostToTopic(ctx, m, channelID, nil, body)
}

// PostToTopic posts to a topic of the channel; nil selects its default topic.
func (s *Service) PostToTopic(ctx context.Context, m org.Membership, channelID domain.ID, topicID *domain.ID, body string) (domain.Message, error) {
	body, err := domain.ValidateMessageBody(body)
	if err != nil {
		return domain.Message{}, fmt.Errorf("%w: %w", ErrInvalidBody, err)
	}
	posted, err := s.store.PostToTopic(ctx, m.Organization.ID, channelID, m.Member.ID, topicID, body)
	if err != nil {
		return domain.Message{}, fmt.Errorf("posting message: %w", err)
	}
	if s.notifier != nil {
		s.notifier.Raise(m.Organization.ID, posted.EventSeq)
	}
	return posted, nil
}
