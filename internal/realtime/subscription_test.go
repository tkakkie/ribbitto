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
		name         string
		channel      domain.ID
		kind         EventKind
		selected     *domain.ID
		topics       []domain.ID
		rendered     domain.ID
		deny, revoke bool
		wantCalls    []string
		wantSent     []int64
	}{
		{"other channel", channelB, EventMessagePosted, nil, []domain.ID{topicA}, topicA, false, false, nil, nil},
		{"other topic", channelA, EventMessagePosted, &topicA, []domain.ID{topicB}, topicA, false, false, nil, nil},
		{"unrouted kind", domain.ID{}, "future.kind", nil, nil, topicA, false, false, nil, nil},
		{"other topic move", channelA, EventMessagesMoved, &topicC, []domain.ID{topicA, topicB}, topicA, false, false, nil, nil},
		{"source move", channelA, EventMessagesMoved, &topicA, []domain.ID{topicA, topicB}, topicC, false, false, []string{"render", "authorize"}, []int64{1}},
		{"destination move", channelA, EventMessagesMoved, &topicB, []domain.ID{topicA, topicB}, topicC, false, false, []string{"render", "authorize"}, []int64{1}},
		{"denied topic move", channelA, EventMessagesMoved, &topicB, []domain.ID{topicA, topicB}, topicC, true, false, []string{"render", "authorize"}, nil},
		{"topic move access lost while rendering", channelA, EventMessagesMoved, &topicA, []domain.ID{topicA, topicB}, topicC, false, true, []string{"render", "authorize"}, nil},
		{"feed move", channelA, EventMessagesMoved, nil, []domain.ID{topicA, topicB}, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"other channel move", channelB, EventMessagesMoved, nil, []domain.ID{topicA, topicB}, topicA, false, false, nil, nil},
		{"denied move", channelA, EventMessagesMoved, nil, []domain.ID{topicA, topicB}, topicA, true, false, []string{"render", "authorize"}, nil},
		{"move access lost while rendering", channelA, EventMessagesMoved, nil, []domain.ID{topicA, topicB}, topicA, false, true, []string{"render", "authorize"}, nil},
		{"feed", channelA, EventMessagePosted, nil, []domain.ID{topicB}, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"posting-time topic wins", channelA, EventMessagePosted, &topicA, []domain.ID{topicA}, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy matching topic", channelA, EventMessagePosted, &topicA, nil, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy other topic", channelA, EventMessagePosted, &topicA, nil, topicB, false, false, []string{"render"}, nil},
		{"denied member", channelA, EventMessagePosted, &topicA, []domain.ID{topicA}, topicA, true, false, []string{"render", "authorize"}, nil},
		{"access lost while rendering", channelA, EventMessagePosted, &topicA, []domain.ID{topicA}, topicA, false, true, []string{"render", "authorize"}, nil},
		// Routing follows the envelope alone: a kind realtime has never heard of.
		{"synthetic kind", channelA, "test.synthetic", &topicC, []domain.ID{topicC}, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"synthetic kind, other topic", channelA, "test.synthetic", &topicA, []domain.ID{topicC}, topicA, false, false, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := posted(1, tt.channel)
			event.Kind, event.Topics = tt.kind, tt.topics
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
