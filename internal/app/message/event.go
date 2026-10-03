package message

import (
	"encoding/json"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EncodePosted returns the stored data of realtime.EventMessagePosted,
// which this feature publishes and so owns (decision 26), for a post in
// topicID: canonical UUID text under channel_id, message_id and topic_id.
func EncodePosted(channelID, messageID, topicID domain.ID) ([]byte, error) {
	data, err := json.Marshal(map[string]string{
		"channel_id": realtime.FormatPayloadID(channelID),
		"message_id": realtime.FormatPayloadID(messageID),
		"topic_id":   realtime.FormatPayloadID(topicID),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding message.posted data: %w", err)
	}
	return data, nil
}
