package topic

import (
	"context"
	"errors"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// ErrNotFound means the topic does not exist in the given organisation and
// channel, including when it exists in another channel or organisation.
var ErrNotFound = errors.New("topic not found")

// ErrInvalidName wraps a name that breaks domain.ValidateTopicName.
var ErrInvalidName = errors.New("invalid topic name")

// ErrNameTaken means the channel already has a topic with that name,
// ignoring case.
var ErrNameTaken = errors.New("topic name already taken")

// Store reads and writes topics, always within one organisation and
// channel. Lookups return ErrNotFound; CreateTopic returns ErrNameTaken for
// a duplicate name. ListTopics returns at most limit topics, the default
// first, then by creation.
type Store interface {
	CreateTopic(ctx context.Context, organizationID, channelID domain.ID, name string) (domain.Topic, error)
	CreateDefaultTopic(ctx context.Context, organizationID, channelID domain.ID) (domain.Topic, error)
	GetTopic(ctx context.Context, organizationID, channelID, id domain.ID) (domain.Topic, error)
	ListTopics(ctx context.Context, organizationID, channelID domain.ID, limit int) ([]domain.Topic, error)
}
