package topic

import (
	"encoding/json"
	"errors"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// KindMessagesMoved is the kind of a branch's move, which branching
// publishes and so owns (decision 26): the messages it moved, their channel,
// and the topics they left and joined (docs/domain/topics.md).
const KindMessagesMoved realtime.EventKind = "messages.moved"

// Moved is the payload of KindMessagesMoved.
type Moved struct {
	ChannelID   domain.ID
	FromTopicID domain.ID
	ToTopicID   domain.ID
	MessageIDs  []domain.ID
}

// EncodeMoved returns the stored data of m: canonical UUID text under
// channel_id, from_topic_id and to_topic_id, and a message_ids array.
func EncodeMoved(m Moved) []byte {
	ids := make([]string, len(m.MessageIDs))
	for i, id := range m.MessageIDs {
		ids[i] = realtime.FormatPayloadID(id)
	}
	// This payload contains only strings and string slices, so marshaling cannot fail.
	data, _ := json.Marshal(map[string]any{
		"channel_id":    realtime.FormatPayloadID(m.ChannelID),
		"from_topic_id": realtime.FormatPayloadID(m.FromTopicID),
		"to_topic_id":   realtime.FormatPayloadID(m.ToTopicID),
		"message_ids":   ids,
	})
	return data
}

// DecodeMoved reads stored data. Every ID is required, the destination
// differs from the source, and there is at least one message with no
// repeats. MaxBranchMessages is not checked: a lower write-side limit must
// not make moves already committed unreadable.
func DecodeMoved(data []byte) (Moved, error) {
	var raw struct {
		ChannelID   string   `json:"channel_id"`
		FromTopicID string   `json:"from_topic_id"`
		ToTopicID   string   `json:"to_topic_id"`
		MessageIDs  []string `json:"message_ids"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Moved{}, err
	}
	var m Moved
	var err error
	for _, field := range []struct {
		value string
		id    *domain.ID
	}{{raw.ChannelID, &m.ChannelID}, {raw.FromTopicID, &m.FromTopicID}, {raw.ToTopicID, &m.ToTopicID}} {
		if *field.id, err = realtime.ParsePayloadID(field.value); err != nil {
			return Moved{}, err
		}
	}
	if m.FromTopicID == m.ToTopicID {
		return Moved{}, errors.New("move destination is the source")
	}
	if len(raw.MessageIDs) == 0 {
		return Moved{}, errors.New("move has no messages")
	}
	seen := make(map[domain.ID]bool, len(raw.MessageIDs))
	for _, value := range raw.MessageIDs {
		id, err := realtime.ParsePayloadID(value)
		if err != nil {
			return Moved{}, err
		}
		if seen[id] {
			return Moved{}, errors.New("move repeats a message ID")
		}
		seen[id] = true
		m.MessageIDs = append(m.MessageIDs, id)
	}
	return m, nil
}

// RouteMoved is messages.moved's realtime.Router: the channel and both topics,
// so a view of either sees the move.
func RouteMoved(payload []byte) (domain.ID, []domain.ID, error) {
	m, err := DecodeMoved(payload)
	if err != nil {
		return domain.ID{}, nil, err
	}
	return m.ChannelID, []domain.ID{m.FromTopicID, m.ToTopicID}, nil
}
