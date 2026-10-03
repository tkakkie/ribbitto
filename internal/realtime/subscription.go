package realtime

// wants checks connection interest without rendering or authorization.
func (s Subscription) wants(event Event) bool {
	if event.ChannelID != s.Channel {
		return false
	}
	switch event.Kind {
	case EventMessagesMoved:
		return s.Topic == nil || *s.Topic == event.FromTopicID || *s.Topic == event.ToTopicID
	case EventMessagePosted:
		return s.Topic == nil || event.TopicID == nil || *s.Topic == *event.TopicID
	default:
		return false
	}
}

func (s Subscription) wantsRendered(event Event, out Outgoing) bool {
	// Old log rows have no posting-time topic. Keep their shared-render
	// fallback; new rows must not be rerouted by a subsequent move.
	return event.Kind != EventMessagePosted || s.Topic == nil || event.TopicID != nil || *s.Topic == out.Topic
}
