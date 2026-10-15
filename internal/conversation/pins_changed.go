package conversation

import (
	"encoding/json"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// KindPinsChanged invalidates current channel pins, without retaining content.
const KindPinsChanged realtime.EventKind = "pins.changed"

// EncodePinsChanged stores only routing IDs, never pin state or message content.
func EncodePinsChanged(channelID, messageID kernel.ID) []byte {
	data, _ := json.Marshal(map[string]string{"channel_id": realtime.FormatPayloadID(channelID), "message_id": realtime.FormatPayloadID(messageID)})
	return data
}

// DecodePinsChanged validates both required IDs, even though only the channel routes.
func DecodePinsChanged(data []byte) (channelID, messageID kernel.ID, err error) {
	var raw struct {
		Channel string `json:"channel_id"`
		Message string `json:"message_id"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return
	}
	if channelID, err = realtime.ParsePayloadID(raw.Channel); err != nil {
		return
	}
	messageID, err = realtime.ParsePayloadID(raw.Message)
	return
}

// RoutePinsChanged routes channel-wide invalidations across every topic.
func RoutePinsChanged(data []byte) (kernel.ID, []kernel.ID, error) {
	channel, _, err := DecodePinsChanged(data)
	return channel, nil, err
}
