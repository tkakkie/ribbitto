package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// TopicReader looks topics up within one organisation and channel. A topic
// outside them, or none, is ErrTopicNotFound. It is bound to a pool: a lookup
// needs no transaction of its own.
type TopicReader interface {
	GetTopic(ctx context.Context, organizationID, channelID, id kernel.ID) (Topic, error)
}

// Topics runs the topic lookups for a member resolved by org.
type Topics struct {
	reader TopicReader
}

// NewTopics returns a Topics.
func NewTopics(reader TopicReader) *Topics {
	return &Topics{reader: reader}
}

// Get returns a topic of the channel in the member's organisation, or
// ErrTopicNotFound. The organisation comes only from m, so a known ID of
// another organisation's topic finds nothing; this is the scope check of the
// topic stream and of paging links (decision 26's resolver).
func (s *Topics) Get(ctx context.Context, m org.Membership, channelID, topicID kernel.ID) (Topic, error) {
	found, err := s.reader.GetTopic(ctx, m.Organization.ID, channelID, topicID)
	if err != nil {
		return Topic{}, fmt.Errorf("finding topic: %w", err)
	}
	return found, nil
}
