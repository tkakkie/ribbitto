package message

import (
	"encoding/json"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// Posted is the decoded payload of realtime.EventMessagePosted.
type Posted struct {
	ChannelID domain.ID
	MessageID domain.ID
	// TopicID is the posting-time topic. Nil means a legacy payload
	// written before topics, without topic_id.
	TopicID *domain.ID
}

// EncodePosted returns the stored data of realtime.EventMessagePosted,
// which this feature publishes and so owns (decision 26), for a post in
// topicID: canonical UUID text under channel_id, message_id and topic_id.
func EncodePosted(channelID, messageID, topicID domain.ID) []byte {
	// This payload contains only strings, so marshaling cannot fail.
	data, _ := json.Marshal(map[string]string{
		"channel_id": realtime.FormatPayloadID(channelID),
		"message_id": realtime.FormatPayloadID(messageID),
		"topic_id":   realtime.FormatPayloadID(topicID),
	})
	return data
}

// DecodePosted reads stored data. Both IDs are required. Only an absent
// topic_id is legacy; a present malformed one fails, rather than silently
// changing how the post is routed.
func DecodePosted(data []byte) (Posted, error) {
	var raw struct {
		ChannelID string          `json:"channel_id"`
		MessageID string          `json:"message_id"`
		TopicID   json.RawMessage `json:"topic_id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Posted{}, err
	}
	var p Posted
	var err error
	if p.ChannelID, err = realtime.ParsePayloadID(raw.ChannelID); err != nil {
		return Posted{}, err
	}
	if p.MessageID, err = realtime.ParsePayloadID(raw.MessageID); err != nil {
		return Posted{}, err
	}
	if len(raw.TopicID) != 0 {
		var value string
		if err := json.Unmarshal(raw.TopicID, &value); err != nil {
			return Posted{}, err
		}
		id, err := realtime.ParsePayloadID(value)
		if err != nil {
			return Posted{}, err
		}
		p.TopicID = &id
	}
	return p, nil
}

// RoutePosted is message.posted's realtime.Router: the channel and, unless
// the payload is legacy, the posting-time topic.
func RoutePosted(payload []byte) (domain.ID, []domain.ID, error) {
	p, err := DecodePosted(payload)
	if err != nil || p.TopicID == nil {
		return p.ChannelID, nil, err
	}
	return p.ChannelID, []domain.ID{*p.TopicID}, nil
}
