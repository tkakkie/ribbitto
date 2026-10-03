package realtime

import (
	"errors"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/domain"
)

func TestStreamSubscriptionInterest(t *testing.T) {
	topicA, topicB, topicC := domain.ID{0x7a}, domain.ID{0x7b}, domain.ID{0x7c}
	for _, tt := range []struct {
		name            string
		channel         domain.ID
		kind            EventKind
		selected, topic *domain.ID
		rendered        domain.ID
		deny, revoke    bool
		wantCalls       []string
		wantSent        []int64
	}{
		{"other channel", channelB, EventMessagePosted, nil, &topicA, topicA, false, false, nil, nil},
		{"other topic", channelA, EventMessagePosted, &topicA, &topicB, topicA, false, false, nil, nil},
		{"unknown kind", channelA, "future.kind", nil, nil, topicA, false, false, nil, nil},
		{"other topic move", channelA, EventMessagesMoved, &topicC, nil, topicA, false, false, nil, nil},
		{"source move", channelA, EventMessagesMoved, &topicA, nil, topicC, false, false, []string{"render", "authorize"}, []int64{1}},
		{"destination move", channelA, EventMessagesMoved, &topicB, nil, topicC, false, false, []string{"render", "authorize"}, []int64{1}},
		{"denied topic move", channelA, EventMessagesMoved, &topicB, nil, topicC, true, false, []string{"render", "authorize"}, nil},
		{"topic move access lost while rendering", channelA, EventMessagesMoved, &topicA, nil, topicC, false, true, []string{"render", "authorize"}, nil},
		{"feed move", channelA, EventMessagesMoved, nil, nil, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"other channel move", channelB, EventMessagesMoved, nil, nil, topicA, false, false, nil, nil},
		{"denied move", channelA, EventMessagesMoved, nil, nil, topicA, true, false, []string{"render", "authorize"}, nil},
		{"move access lost while rendering", channelA, EventMessagesMoved, nil, nil, topicA, false, true, []string{"render", "authorize"}, nil},
		{"feed", channelA, EventMessagePosted, nil, &topicB, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"posting-time topic wins", channelA, EventMessagePosted, &topicA, &topicA, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy matching topic", channelA, EventMessagePosted, &topicA, nil, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy other topic", channelA, EventMessagePosted, &topicA, nil, topicB, false, false, []string{"render"}, nil},
		{"denied member", channelA, EventMessagePosted, &topicA, &topicA, topicA, true, false, []string{"render", "authorize"}, nil},
		{"access lost while rendering", channelA, EventMessagePosted, &topicA, &topicA, topicA, false, true, []string{"render", "authorize"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := posted(1, tt.channel)
			event.Kind, event.TopicID = tt.kind, tt.topic
			event.FromTopicID, event.ToTopicID = topicA, topicB
			selected := sub
			selected.Topic = tt.selected
			var calls []string
			allowed := !tt.deny
			s := Stream{
				Hub: NewHub(), BatchSize: 1,
				Events: &fakeLog{events: []Event{event}, maxReads: 1},
				Renderer: rendererFunc(func(e Event) (Outgoing, error) {
					calls = append(calls, "render")
					if tt.revoke {
						allowed = false
					}
					return Outgoing{ID: e.Seq, Name: "message", Topic: tt.rendered}, nil
				}),
				Authorizer: authorizerFunc(func(Event) (bool, error) {
					calls = append(calls, "authorize")
					return allowed, nil
				}),
			}
			send := newRecorder()
			cursor, err := s.Run(t.Context(), selected, 0, send)
			if cursor != 1 || !errors.Is(err, errTooManyReads) || !slices.Equal(calls, tt.wantCalls) || !slices.Equal(send.ids(), tt.wantSent) {
				t.Fatalf("Run = %d, %v; calls %v, sent %v; want cursor 1, calls %v, sent %v", cursor, err, calls, send.ids(), tt.wantCalls, tt.wantSent)
			}
		})
	}
}
