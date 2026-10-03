package realtime

import "slices"

// wants checks connection interest without rendering or authorization: an
// event of the channel, and for a topic view, one of its routing topics. An
// event without routing topics is decided after rendering (wantsRendered).
func (s Subscription) wants(event Event) bool {
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
