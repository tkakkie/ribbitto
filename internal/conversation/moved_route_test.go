package conversation_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestRouteMovedTopics(t *testing.T) {
	channelID := kernel.ID{0xc1}
	firstTopicID, secondTopicID := kernel.ID{0xf1}, kernel.ID{0xf2}
	firstMessageID, secondMessageID, thirdMessageID := kernel.ID{0xa1}, kernel.ID{0xa2}, kernel.ID{0xa3}

	for _, tt := range []struct {
		name       string
		moved      conversation.Moved
		wantTopics []kernel.ID
	}{
		{
			name: "forward single message",
			moved: conversation.Moved{
				ChannelID:   channelID,
				FromTopicID: firstTopicID,
				ToTopicID:   secondTopicID,
				MessageIDs:  []kernel.ID{firstMessageID},
			},
			wantTopics: []kernel.ID{firstTopicID, secondTopicID},
		},
		{
			name: "reverse single message keeps source first",
			moved: conversation.Moved{
				ChannelID:   channelID,
				FromTopicID: secondTopicID,
				ToTopicID:   firstTopicID,
				MessageIDs:  []kernel.ID{firstMessageID},
			},
			wantTopics: []kernel.ID{secondTopicID, firstTopicID},
		},
		{
			name: "three messages route only the two topics",
			moved: conversation.Moved{
				ChannelID:   channelID,
				FromTopicID: firstTopicID,
				ToTopicID:   secondTopicID,
				MessageIDs:  []kernel.ID{firstMessageID, secondMessageID, thirdMessageID},
			},
			wantTopics: []kernel.ID{firstTopicID, secondTopicID},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotChannel, gotTopics, err := conversation.RouteMoved(conversation.EncodeMoved(tt.moved))
			if err != nil {
				t.Fatalf("RouteMoved() error = %v, want nil", err)
			}
			if gotChannel != channelID {
				t.Errorf("channel = %v, want %v", gotChannel, channelID)
			}
			if len(gotTopics) != 2 || !reflect.DeepEqual(gotTopics, tt.wantTopics) {
				t.Errorf("topics = %v (len %d), want %v (len 2)", gotTopics, len(gotTopics), tt.wantTopics)
			}
		})
	}
}

func TestRouteMovedRejects(t *testing.T) {
	channelID := kernel.ID{0xc1}
	firstTopicID, secondTopicID := kernel.ID{0xf1}, kernel.ID{0xf2}
	firstMessageID := kernel.ID{0xa1}

	for _, tt := range []struct {
		name          string
		payload       []byte
		wantSyntaxErr bool
		wantErr       string
	}{
		{
			name:          "nil payload",
			payload:       nil,
			wantSyntaxErr: true,
		},
		{
			name:          "malformed JSON",
			payload:       []byte("not json"),
			wantSyntaxErr: true,
		},
		{
			name:    "empty object",
			payload: []byte("{}"),
			wantErr: "required ID is not a canonical UUID",
		},
		{
			name:    "non-UUID topics",
			payload: []byte(`{"channel_id":"00000000-0000-0000-0000-0000000000c1","from_topic_id":"x","to_topic_id":"y"}`),
			wantErr: "required ID is not a canonical UUID",
		},
		{
			name: "same source and destination",
			payload: conversation.EncodeMoved(conversation.Moved{
				ChannelID:   channelID,
				FromTopicID: firstTopicID,
				ToTopicID:   firstTopicID,
				MessageIDs:  []kernel.ID{firstMessageID},
			}),
			wantErr: "move destination is the source",
		},
		{
			name: "no messages",
			payload: conversation.EncodeMoved(conversation.Moved{
				ChannelID:   channelID,
				FromTopicID: firstTopicID,
				ToTopicID:   secondTopicID,
			}),
			wantErr: "move has no messages",
		},
		{
			name: "repeated message ID",
			payload: conversation.EncodeMoved(conversation.Moved{
				ChannelID:   channelID,
				FromTopicID: firstTopicID,
				ToTopicID:   secondTopicID,
				MessageIDs:  []kernel.ID{firstMessageID, firstMessageID},
			}),
			wantErr: "move repeats a message ID",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotChannel, gotTopics, err := conversation.RouteMoved(tt.payload)
			if gotChannel != (kernel.ID{}) {
				t.Errorf("channel = %v, want zero ID", gotChannel)
			}
			if gotTopics != nil {
				t.Errorf("topics = %#v, want nil", gotTopics)
			}
			if tt.wantSyntaxErr {
				var syntaxErr *json.SyntaxError
				if !errors.As(err, &syntaxErr) {
					t.Fatalf("error = %v (%T), want *json.SyntaxError", err, err)
				}
				if err.Error() != syntaxErr.Error() {
					t.Errorf("error = %q, want %q", err.Error(), syntaxErr.Error())
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
