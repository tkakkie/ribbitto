package realtime

import (
	"errors"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/domain"
)

func TestStreamSubscriptionInterest(t *testing.T) {
	topicA, topicB := domain.ID{0x7a}, domain.ID{0x7b}
	for _, tt := range []struct {
		name            string
		channel         domain.ID
		kind            domain.EventKind
		selected, topic *domain.ID
		rendered        domain.ID
		deny, revoke    bool
		wantCalls       []string
		wantSent        []int64
	}{
		{"other channel", channelB, domain.EventMessagePosted, nil, &topicA, topicA, false, false, nil, nil},
		{"other topic", channelA, domain.EventMessagePosted, &topicA, &topicB, topicA, false, false, nil, nil},
		{"unknown kind", channelA, "future.kind", nil, nil, topicA, false, false, nil, nil},
		{"move pending", channelA, domain.EventMessagesMoved, nil, nil, topicA, false, false, nil, nil},
		{"feed", channelA, domain.EventMessagePosted, nil, &topicB, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"posting-time topic wins", channelA, domain.EventMessagePosted, &topicA, &topicA, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy matching topic", channelA, domain.EventMessagePosted, &topicA, nil, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy other topic", channelA, domain.EventMessagePosted, &topicA, nil, topicB, false, false, []string{"render"}, nil},
		{"denied member", channelA, domain.EventMessagePosted, &topicA, &topicA, topicA, true, false, []string{"render", "authorize"}, nil},
		{"access lost while rendering", channelA, domain.EventMessagePosted, &topicA, &topicA, topicA, false, true, []string{"render", "authorize"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := posted(1, tt.channel)
			event.Kind, event.TopicID = tt.kind, tt.topic
			selected := sub
			selected.Topic = tt.selected
			var calls []string
			allowed := !tt.deny
			s := Stream{
				Hub: NewHub(), BatchSize: 1,
				Events: &fakeLog{events: []domain.Event{event}, maxReads: 1},
				Renderer: rendererFunc(func(e domain.Event) (Outgoing, error) {
					calls = append(calls, "render")
					if tt.revoke {
						allowed = false
					}
					return Outgoing{ID: e.Seq, Name: "message", Topic: tt.rendered}, nil
				}),
				Authorizer: authorizerFunc(func(domain.Event) (bool, error) {
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
