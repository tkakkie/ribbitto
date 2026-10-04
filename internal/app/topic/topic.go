package topic

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// Store reads and writes topics, always within one organisation and
// channel. Lookups return conversation.ErrTopicNotFound; CreateTopic returns
// conversation.ErrTopicNameTaken for a duplicate name.
// ListTopics returns at most limit topics, the default
// first, then by name (case-insensitive).
type Store interface {
	CreateTopic(ctx context.Context, organizationID, channelID domain.ID, name string) (conversation.Topic, error)
	CreateDefaultTopic(ctx context.Context, organizationID, channelID domain.ID) (conversation.Topic, error)
	GetTopic(ctx context.Context, organizationID, channelID, id domain.ID) (conversation.Topic, error)
	GetDefaultTopic(ctx context.Context, organizationID, channelID domain.ID) (conversation.Topic, error)
	ListTopics(ctx context.Context, organizationID, channelID domain.ID, limit int) ([]conversation.Topic, error)
}
