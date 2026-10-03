package topic_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
)

func TestMovedPayload(t *testing.T) {
	want := topic.Moved{
		ChannelID: domain.ID{15: 1}, FromTopicID: domain.ID{0: 0xab, 15: 2}, ToTopicID: domain.ID{0: 0xcd, 15: 3},
		MessageIDs: []domain.ID{{15: 4}, {0: 0xef, 15: 5}},
	}
	data, err := topic.EncodeMoved(want)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := topic.DecodeMoved(data); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, want)
	}
	uuid := func(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012x", n) }
	for _, tt := range []struct {
		name  string
		field string
		value any
	}{
		{"missing channel", "channel_id", nil},
		{"malformed channel", "channel_id", "invalid"},
		{"missing source", "from_topic_id", nil},
		{"malformed source", "from_topic_id", "invalid"},
		{"missing destination", "to_topic_id", nil},
		{"malformed destination", "to_topic_id", "invalid"},
		{"same topic", "to_topic_id", uuid(2)},
		{"missing messages", "message_ids", nil},
		{"null messages", "message_ids", json.RawMessage(`null`)},
		{"empty messages", "message_ids", []string{}},
		{"repeated message", "message_ids", []string{uuid(4), uuid(4)}},
		{"malformed message", "message_ids", []string{uuid(4), "invalid"}},
		{"noncanonical message", "message_ids", []string{"00000000x0000x0000x0000x000000000004"}},
		{"null message", "message_ids", []any{nil}},
		{"wrong message type", "message_ids", []any{false}},
		{"wrong list type", "message_ids", uuid(4)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := map[string]any{
				"channel_id": uuid(1), "from_topic_id": uuid(2), "to_topic_id": uuid(3),
				"message_ids": []string{uuid(4)},
			}
			if tt.value == nil {
				delete(data, tt.field)
			} else {
				data[tt.field] = tt.value
			}
			encoded, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := topic.DecodeMoved(encoded); err == nil {
				t.Fatalf("DecodeMoved(%s) = %+v; want an error", encoded, got)
			}
		})
	}
}
