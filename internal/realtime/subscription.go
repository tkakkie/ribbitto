package realtime

import "github.com/tkakkie/ribbitto/internal/domain"

// wants checks connection interest without rendering or authorization.
// Moves remain undelivered until their view corrections are implemented.
func (s Subscription) wants(event domain.Event) bool {
	if event.Kind != domain.EventMessagePosted || event.ChannelID != s.Channel {
		return false
	}
	return s.Topic == nil || event.TopicID == nil || *s.Topic == *event.TopicID
}

func (s Subscription) wantsRendered(event domain.Event, out Outgoing) bool {
	// Old log rows have no posting-time topic. Keep their shared-render
	// fallback; new rows must not be rerouted by a subsequent move.
	return s.Topic == nil || event.TopicID != nil || *s.Topic == out.Topic
}
