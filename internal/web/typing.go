package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

type handlingStartKey struct{}

func (p channelPages) signal(w http.ResponseWriter, r *http.Request, m org.Membership) {
	if p.typing == nil {
		http.NotFound(w, r)
		return
	}
	if !p.limiter.Allow(m.Organization.ID, m.Member.ID) {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	channel, channelOK := channelID(r)
	topic, topicOK := pathID(r.PathValue("topicID"))
	if !channelOK || !topicOK || topic == (kernel.ID{}) {
		http.NotFound(w, r)
		return
	}
	_, err := p.channels.Get(r.Context(), m, channel)
	if err == nil {
		_, err = p.topics.Get(r.Context(), m, channel, topic)
	}
	switch {
	case errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, conversation.ErrTopicNotFound), errors.Is(err, org.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		serverError(w, r, "resolving typing scope", err)
		return
	}
	if !parseForm(w, r) {
		return
	}
	generation := r.PostForm.Get("composer")
	if generation == "" || len(r.PostForm["composer"]) != 1 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	handledAt, _ := r.Context().Value(handlingStartKey{}).(time.Time)
	account, _ := middleware.Account(r.Context())
	p.typing.Signal(m.Organization.ID, typing.Place{Channel: channel, Topic: topic}, typing.Typist{
		Member: m.Member.ID, DisplayName: memberDisplayName(account.DisplayName), Handle: m.Member.Handle,
	}, generation, handledAt)
	w.WriteHeader(http.StatusNoContent)
}
