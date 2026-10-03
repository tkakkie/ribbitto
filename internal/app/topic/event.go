package topic

import (
	"encoding/json"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// Moved is the payload of realtime.EventMessagesMoved, which branching
// publishes and so owns (decision 26): the messages a branch moved, their
// channel, and the topics they left and joined.
type Moved struct {
	ChannelID   domain.ID
	FromTopicID domain.ID
	ToTopicID   domain.ID
	MessageIDs  []domain.ID
}

// EncodeMoved returns the stored data of m: canonical UUID text under
// channel_id, from_topic_id and to_topic_id, and a message_ids array.
func EncodeMoved(m Moved) ([]byte, error) {
	ids := make([]string, len(m.MessageIDs))
	for i, id := range m.MessageIDs {
		ids[i] = realtime.FormatPayloadID(id)
	}
	data, err := json.Marshal(map[string]any{
		"channel_id":    realtime.FormatPayloadID(m.ChannelID),
		"from_topic_id": realtime.FormatPayloadID(m.FromTopicID),
		"to_topic_id":   realtime.FormatPayloadID(m.ToTopicID),
		"message_ids":   ids,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding messages.moved data: %w", err)
	}
	return data, nil
}
