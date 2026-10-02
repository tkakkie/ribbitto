package realtime

import "github.com/tkakkie/ribbitto/internal/domain"

// wants checks connection interest without rendering or authorization.
// Topic-page moves remain pending; feeds can apply stable-ID corrections.
func (s Subscription) wants(event domain.Event) bool {
	if event.ChannelID != s.Channel {
		return false
	}
	switch event.Kind {
	case domain.EventMessagesMoved:
		return s.Topic == nil // Topic-page corrections follow separately.
	case domain.EventMessagePosted:
		return s.Topic == nil || event.TopicID == nil || *s.Topic == *event.TopicID
	default:
		return false
	}
}

func (s Subscription) wantsRendered(event domain.Event, out Outgoing) bool {
	// Old log rows have no posting-time topic. Keep their shared-render
	// fallback; new rows must not be rerouted by a subsequent move.
	return s.Topic == nil || event.TopicID != nil || *s.Topic == out.Topic
}
