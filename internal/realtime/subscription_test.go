package realtime

import (
	"errors"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestStreamSubscriptionInterest(t *testing.T) {
	topicA, topicB, topicC := kernel.ID{0x7a}, kernel.ID{0x7b}, kernel.ID{0x7c}
	for _, tt := range []struct {
		name         string
		channel      kernel.ID
		kind         EventKind
		selected     *kernel.ID
		topics       []kernel.ID
		rendered     kernel.ID
		deny, revoke bool
		wantCalls    []string
		wantSent     []int64
	}{
		{"other channel", channelB, kindPosted, nil, []kernel.ID{topicA}, topicA, false, false, nil, nil},
		{"other topic", channelA, kindPosted, &topicA, []kernel.ID{topicB}, topicA, false, false, nil, nil},
		{"unrouted kind", kernel.ID{}, "future.kind", nil, nil, topicA, false, false, nil, nil},
		{"other topic move", channelA, kindMoved, &topicC, []kernel.ID{topicA, topicB}, topicA, false, false, nil, nil},
		{"source move", channelA, kindMoved, &topicA, []kernel.ID{topicA, topicB}, topicC, false, false, []string{"render", "authorize"}, []int64{1}},
		{"destination move", channelA, kindMoved, &topicB, []kernel.ID{topicA, topicB}, topicC, false, false, []string{"render", "authorize"}, []int64{1}},
		{"denied topic move", channelA, kindMoved, &topicB, []kernel.ID{topicA, topicB}, topicC, true, false, []string{"render", "authorize"}, nil},
		{"topic move access lost while rendering", channelA, kindMoved, &topicA, []kernel.ID{topicA, topicB}, topicC, false, true, []string{"render", "authorize"}, nil},
		{"feed move", channelA, kindMoved, nil, []kernel.ID{topicA, topicB}, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"other channel move", channelB, kindMoved, nil, []kernel.ID{topicA, topicB}, topicA, false, false, nil, nil},
		{"denied move", channelA, kindMoved, nil, []kernel.ID{topicA, topicB}, topicA, true, false, []string{"render", "authorize"}, nil},
		{"move access lost while rendering", channelA, kindMoved, nil, []kernel.ID{topicA, topicB}, topicA, false, true, []string{"render", "authorize"}, nil},
		{"feed", channelA, kindPosted, nil, []kernel.ID{topicB}, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"posting-time topic wins", channelA, kindPosted, &topicA, []kernel.ID{topicA}, topicB, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy matching topic", channelA, kindPosted, &topicA, nil, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"legacy other topic", channelA, kindPosted, &topicA, nil, topicB, false, false, []string{"render"}, nil},
		{"denied member", channelA, kindPosted, &topicA, []kernel.ID{topicA}, topicA, true, false, []string{"render", "authorize"}, nil},
		{"access lost while rendering", channelA, kindPosted, &topicA, []kernel.ID{topicA}, topicA, false, true, []string{"render", "authorize"}, nil},
		// Routing follows the envelope alone: a kind realtime has never heard of.
		{"synthetic kind", channelA, "test.synthetic", &topicC, []kernel.ID{topicC}, topicA, false, false, []string{"render", "authorize"}, []int64{1}},
		{"synthetic kind, other topic", channelA, "test.synthetic", &topicA, []kernel.ID{topicC}, topicA, false, false, nil, nil},
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

// Each negative fixture changes exactly one scope from the matching fixture.
// Calling deliver directly keeps the reader and authorizer from masking a
// missing pre-filter, especially for an event of another organisation.
func TestStreamSubscriptionScopes(t *testing.T) {
	topicA, topicB := kernel.ID{0x7a}, kernel.ID{0x7b}
	for _, kind := range []EventKind{kindPosted, kindMoved} {
		t.Run(string(kind), func(t *testing.T) {
			for _, tt := range []struct {
				name      string
				org       kernel.ID
				channel   kernel.ID
				topic     kernel.ID
				interest  Interest
				deny      bool
				wantCalls []string
				wantSent  bool
			}{
				{"matching", orgA, channelA, topicA, InterestMessages, false, []string{"render", "authorize", "send"}, true},
				{"organisation", orgB, channelA, topicA, InterestMessages, false, nil, false},
				{"channel", orgA, channelB, topicA, InterestMessages, false, nil, false},
				{"routing topic", orgA, channelA, topicB, InterestMessages, false, nil, false},
				{"interest", orgA, channelA, topicA, InterestSidebar, false, nil, false},
				{"authorizer denies", orgA, channelA, topicA, InterestMessages, true, []string{"render", "authorize"}, false},
			} {
				t.Run(tt.name, func(t *testing.T) {
					selected := sub
					selected.Topic, selected.Interests = &topicA, []Interest{tt.interest}
					event := Event{OrganizationID: tt.org, ChannelID: tt.channel, Topics: []kernel.ID{tt.topic}, Seq: 1, Kind: kind}
					var calls []string
					s := Stream{
						Renderer: rendererFunc(func(e Event) (Outgoing, error) {
							calls = append(calls, "render")
							return Outgoing{ID: e.Seq, Topic: topicA}, nil
						}),
						Authorizer: authorizerFunc(func(Event) (bool, error) {
							calls = append(calls, "authorize")
							return !tt.deny, nil
						}),
					}
					send := newRecorder()
					sent, err := s.deliver(t.Context(), selected, event, send)
					if len(send.ids()) != 0 {
						calls = append(calls, "send")
					}
					if err != nil || sent != tt.wantSent || !slices.Equal(calls, tt.wantCalls) {
						t.Fatalf("deliver = %t, %v; calls %v; want sent %t, calls %v", sent, err, calls, tt.wantSent, tt.wantCalls)
					}
				})
			}
		})
	}
}
