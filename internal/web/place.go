package web

import (
	"errors"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// resolvePlace checks visibility before a caller uses the requested place.
// Resolve the channel before parsing the optional topic to preserve read
// requests' channel-error precedence over malformed topic IDs.
func (p channelPages) resolvePlace(w http.ResponseWriter, r *http.Request, m org.Membership, channel kernel.ID, rawTopic string) (*kernel.ID, bool) {
	_, err := p.channels.Get(r.Context(), m, channel)
	var topic *kernel.ID
	if err == nil && rawTopic != "" {
		selected, ok := pathID(rawTopic)
		if !ok {
			http.NotFound(w, r)
			return nil, false
		}
		topic = &selected
		_, err = p.topics.Get(r.Context(), m, channel, selected)
	}
	switch {
	case errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, conversation.ErrTopicNotFound):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "resolving conversation scope", err)
	default:
		return topic, true
	}
	return nil, false
}
