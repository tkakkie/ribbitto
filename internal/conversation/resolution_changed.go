package conversation

import (
	"encoding/json"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// KindResolutionChanged invalidates a topic's current resolution and sidebar order.
const KindResolutionChanged realtime.EventKind = "topic.resolution_changed"

// EncodeResolutionChanged retains routing IDs only, never conclusion state.
func EncodeResolutionChanged(channelID, topicID kernel.ID) []byte {
	data, _ := json.Marshal(map[string]string{"channel_id": realtime.FormatPayloadID(channelID), "topic_id": realtime.FormatPayloadID(topicID)})
	return data
}

// DecodeResolutionChanged validates the channel and topic routing IDs.
func DecodeResolutionChanged(data []byte) (channelID, topicID kernel.ID, err error) {
	var raw struct {
		Channel string `json:"channel_id"`
		Topic   string `json:"topic_id"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return
	}
	if channelID, err = realtime.ParsePayloadID(raw.Channel); err != nil {
		return
	}
	topicID, err = realtime.ParsePayloadID(raw.Topic)
	return
}

// RouteResolutionChanged routes invalidations channel-wide, including other topics.
func RouteResolutionChanged(data []byte) (kernel.ID, []kernel.ID, error) {
	channel, _, err := DecodeResolutionChanged(data)
	return channel, nil, err
}
