package realtime

import "slices"

// Interest identifies output a connection asks to receive, never permission.
type Interest string

const (
	// InterestMessages requests posts and moves in the selected channel or topic.
	InterestMessages Interest = "messages"
	// InterestSidebar declares sidebar output; no sidebar frames are delivered yet.
	InterestSidebar Interest = "sidebar"
)

// wants checks explicit interests and organisation/channel/routing-topic scope
// before rendering or authorization. Legacy events without routing topics are
// decided after rendering (wantsRendered).
func (s Subscription) wants(event Event) bool {
	if !slices.Contains(s.Interests, InterestMessages) {
		return false
	}
	if event.OrganizationID != s.Organization {
		return false
	}
	if event.ChannelID != s.Channel {
		return false
	}
	return s.Topic == nil || len(event.Topics) == 0 || slices.Contains(event.Topics, *s.Topic)
}

func (s Subscription) wantsRendered(event Event, out Outgoing) bool {
	// Old posts have no posting-time topic, so the shared render's topic
	// decides; events with routing topics are never rerouted by a later move.
	return s.Topic == nil || len(event.Topics) != 0 || *s.Topic == out.Topic
}
