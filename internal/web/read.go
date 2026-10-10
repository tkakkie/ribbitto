package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Reading marks a resolved feed or topic read through the unread use case.
type Reading interface {
	Feed(context.Context, unread.Scope, int64, int64) error
	Topic(context.Context, unread.TopicScope, int64, int64) error
}

func (p channelPages) read(w http.ResponseWriter, r *http.Request, m org.Membership) {
	if p.reading == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := channelID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, err := p.channels.Get(r.Context(), m, id)
	if raw := r.PathValue("topicID"); err == nil && raw != "" {
		selected, valid := pathID(raw)
		if !valid {
			http.NotFound(w, r)
			return
		}
		_, err = p.topics.Get(r.Context(), m, id, selected)
		p.topicID = &selected
	}
	switch {
	case errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, conversation.ErrTopicNotFound), errors.Is(err, org.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		serverError(w, r, "resolving read scope", err)
		return
	}
	if !parseForm(w, r) {
		return
	}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	values := r.PostForm["cursor"]
	cursor, err := strconv.ParseInt(r.PostForm.Get("cursor"), 10, 64)
	if err != nil || queryErr != nil || len(values) != 1 || query.Has("before") || r.PostForm.Has("before") {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: id, MemberID: m.Member.ID}
	if p.topicID != nil {
		err = p.reading.Topic(r.Context(), unread.TopicScope{Scope: scope, TopicID: *p.topicID}, m.Member.JoinedEventSeq, cursor)
	} else {
		err = p.reading.Feed(r.Context(), scope, m.Member.JoinedEventSeq, cursor)
	}
	switch {
	case errors.Is(err, unread.ErrInvalidCursor):
		http.Error(w, "Bad Request", http.StatusBadRequest)
	case errors.Is(err, org.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "advancing read state", err)
	case r.Header.Get("HX-Request") == "true":
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Redirect(w, r, view.ConversationURL(m.Organization.Slug, id, p.topicID), http.StatusSeeOther)
	}
}
