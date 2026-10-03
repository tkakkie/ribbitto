package message_test

import (
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
)

func TestPostedPayload(t *testing.T) {
	channel, posted, topic := domain.ID{15: 1}, domain.ID{0: 0xab, 15: 2}, domain.ID{0: 0xcd, 15: 3}
	data, err := message.EncodePosted(channel, posted, topic)
	if err != nil {
		t.Fatal(err)
	}
	got, err := message.DecodePosted(data)
	if want := (message.Posted{ChannelID: channel, MessageID: posted, TopicID: &topic}); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, want)
	}
}
