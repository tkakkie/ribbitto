package conversation_test

import (
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestPostedPayload(t *testing.T) {
	channel, posted, topic := kernel.ID{15: 1}, kernel.ID{0: 0xab, 15: 2}, kernel.ID{0: 0xcd, 15: 3}
	data := conversation.EncodePosted(channel, posted, topic)
	got, err := conversation.DecodePosted(data)
	if want := (conversation.Posted{ChannelID: channel, MessageID: posted, TopicID: &topic}); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, want)
	}
	// Only an absent topic_id is legacy; every present invalid value fails.
	const ids = `"channel_id":"00000000-0000-0000-0000-000000000001","message_id":"00000000-0000-0000-0000-000000000002"`
	got, err = conversation.DecodePosted([]byte(`{` + ids + `}`))
	if err != nil || got.TopicID != nil || got.MessageID != (kernel.ID{15: 2}) {
		t.Fatalf("legacy post = %+v, %v", got, err)
	}
	if channel, topics, err := conversation.RoutePosted([]byte(`{` + ids + `}`)); err != nil || channel != (kernel.ID{15: 1}) || topics != nil {
		t.Fatalf("legacy RoutePosted = %v, %v, %v; want the channel and no topics", channel, topics, err)
	}
	if channel, topics, err := conversation.RoutePosted(data); err != nil || channel != (kernel.ID{15: 1}) || !reflect.DeepEqual(topics, []kernel.ID{topic}) {
		t.Fatalf("RoutePosted = %v, %v, %v; want the channel and the posting-time topic", channel, topics, err)
	}
	for _, data := range []string{
		`{"channel_id":"invalid","message_id":"invalid"}`,
		`{"channel_id":"00000000-0000-0000-0000-000000000001","message_id":false}`,
		`{"channel_id":"00000000-0000-0000-0000-000000000001"}`,
		`{"channel_id":null,"message_id":null}`,
		`{` + ids + `,"topic_id":null}`,
		`{` + ids + `,"topic_id":false}`,
		`{` + ids + `,"topic_id":42}`,
		`{` + ids + `,"topic_id":[]}`,
		`{` + ids + `,"topic_id":{}}`,
		`{` + ids + `,"topic_id":""}`,
		`{` + ids + `,"topic_id":"bad"}`,
		`{` + ids + `,"topic_id":"00000000x0000x0000x0000x000000000001"}`,
	} {
		if got, err := conversation.DecodePosted([]byte(data)); err == nil {
			t.Errorf("DecodePosted(%s) = %+v; want an error", data, got)
		}
	}
}
