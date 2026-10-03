package topic_test

import (
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
}
