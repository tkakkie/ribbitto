package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Resolution changes a topic's conclusion with the channel's write permissions.
type Resolution interface {
	Resolve(context.Context, org.Membership, kernel.ID, kernel.ID, kernel.ID) error
	Reopen(context.Context, org.Membership, kernel.ID, kernel.ID) error
}

func (p channelPages) resolve(w http.ResponseWriter, r *http.Request, m org.Membership) {
	p.changeResolution(w, r, m, true)
}

func (p channelPages) reopen(w http.ResponseWriter, r *http.Request, m org.Membership) {
	p.changeResolution(w, r, m, false)
}

func (p channelPages) changeResolution(w http.ResponseWriter, r *http.Request, m org.Membership, resolve bool) {
	channel, ok := channelID(r)
	topic, valid := pathID(r.PathValue("topicID"))
	if !ok || !valid || p.resolution == nil {
		http.NotFound(w, r)
		return
	}
	if !p.pinLimit.Allow(m.Organization.ID, m.Member.ID) {
		rejectMemberLimit(w, r, p.pinLimit, p.pages)
		return
	}
	var err error
	if resolve {
		if !parseForm(w, r) {
			return
		}
		message, valid := pathID(r.PostForm.Get("message"))
		if !valid || len(r.PostForm["message"]) != 1 {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		err = p.resolution.Resolve(r.Context(), m, channel, topic, message)
	} else {
		err = p.resolution.Reopen(r.Context(), m, channel, topic)
	}
	switch {
	case isNotFound(err):
		http.NotFound(w, r)
	case errors.Is(err, conversation.ErrDefaultTopicResolution):
		http.Error(w, i18n.T(r.Context(), "resolution.error.default"), http.StatusUnprocessableEntity)
	case errors.Is(err, conversation.ErrConclusionConflict):
		http.Error(w, i18n.T(r.Context(), "resolution.error.moved"), http.StatusConflict)
	case err != nil:
		serverError(w, r, "changing resolution", err)
	default:
		http.Redirect(w, r, view.ConversationURL(m.Organization.Slug, channel, &topic), http.StatusSeeOther)
	}
}
