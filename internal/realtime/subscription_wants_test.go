package realtime

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Stream.deliver drops events of another organisation before calling wants, so
// calling wants directly covers its own organisation guard as well as channel,
// routing-topic and interest scoping.
func TestSubscriptionWants(t *testing.T) {
	topicA, topicB := kernel.ID{0x7a}, kernel.ID{0x7b}
	for _, tt := range []struct {
		name      string
		topic     *kernel.ID
		interests []Interest
		org       kernel.ID
		channel   kernel.ID
		topics    []kernel.ID
		want      bool
	}{
		{name: "feed with event topics", interests: []Interest{InterestMessages}, org: orgA, channel: channelA, topics: []kernel.ID{topicB}, want: true},
		{name: "matching routing topic", topic: &topicA, interests: []Interest{InterestMessages}, org: orgA, channel: channelA, topics: []kernel.ID{topicA, topicB}, want: true},
		{name: "other routing topic", topic: &topicA, interests: []Interest{InterestMessages}, org: orgA, channel: channelA, topics: []kernel.ID{topicB}, want: false},
		{name: "topic subscription with legacy event", topic: &topicA, interests: []Interest{InterestMessages}, org: orgA, channel: channelA, topics: nil, want: true},
		{name: "feed with legacy event", interests: []Interest{InterestMessages}, org: orgA, channel: channelA, topics: nil, want: true},
		{name: "other organisation", interests: []Interest{InterestMessages}, org: orgB, channel: channelA, topics: nil, want: false},
		{name: "other channel", interests: []Interest{InterestMessages}, org: orgA, channel: channelB, topics: nil, want: false},
		{name: "no interests", interests: nil, org: orgA, channel: channelA, topics: nil, want: false},
		{name: "sidebar interest only", interests: []Interest{InterestSidebar}, org: orgA, channel: channelA, topics: nil, want: false},
		{name: "sidebar and messages interests", interests: []Interest{InterestSidebar, InterestMessages}, org: orgA, channel: channelA, topics: nil, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sub := Subscription{
				Organization: orgA,
				Channel:      channelA,
				Interests:    tt.interests,
				Topic:        tt.topic,
			}
			event := Event{
				OrganizationID: tt.org,
				ChannelID:      tt.channel,
				Topics:         tt.topics,
			}
			if got := sub.wants(event); got != tt.want {
				t.Fatalf("%s: wants = %t, want %t", tt.name, got, tt.want)
			}
		})
	}
}

func TestSubscriptionWantsRendered(t *testing.T) {
	topicA, topicB, topicC := kernel.ID{0x7a}, kernel.ID{0x7b}, kernel.ID{0x7c}
	for _, tt := range []struct {
		name     string
		topic    *kernel.ID
		topics   []kernel.ID
		outTopic kernel.ID
		want     bool
	}{
		{name: "feed", topic: nil, topics: nil, outTopic: topicB, want: true},
		{name: "legacy matching rendered topic", topic: &topicA, topics: nil, outTopic: topicA, want: true},
		{name: "legacy other rendered topic", topic: &topicA, topics: nil, outTopic: topicB, want: false},
		// An event routed by its topics is never rerouted by a later move.
		{name: "routed other topic not rerouted by rendered topic", topic: &topicA, topics: []kernel.ID{topicB}, outTopic: topicC, want: true},
		{name: "routed matching topic not rerouted by rendered topic", topic: &topicA, topics: []kernel.ID{topicA}, outTopic: topicB, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sub := Subscription{
				Organization: orgA,
				Channel:      channelA,
				Interests:    []Interest{InterestMessages},
				Topic:        tt.topic,
			}
			event := Event{
				OrganizationID: orgA,
				ChannelID:      channelA,
				Topics:         tt.topics,
			}
			out := Outgoing{Topic: tt.outTopic}
			if got := sub.wantsRendered(event, out); got != tt.want {
				t.Fatalf("%s: wantsRendered = %t, want %t", tt.name, got, tt.want)
			}
		})
	}
}
