package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Reading marks a resolved feed or topic read through the unread use case.
type Reading interface {
	Feed(context.Context, unread.Scope, int64, int64) error
	Topic(context.Context, unread.TopicScope, int64, int64) error
	FeedShown(context.Context, unread.Scope, int64, int64, int64, int64) error
	TopicShown(context.Context, unread.TopicScope, int64, int64, int64, int64) error
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
	p.topicID, ok = p.resolvePlace(w, r, m, id, r.PathValue("topicID"))
	if !ok {
		return
	}
	if !parseForm(w, r) {
		return
	}
	cursor, from, through, shown, valid := readForm(r)
	if !valid {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	release, admitted := p.readLimit.Admit(m.Organization.ID, m.Member.ID)
	if !admitted {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	defer release()
	var err error
	scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: id, MemberID: m.Member.ID}
	if p.topicID != nil && shown {
		err = p.reading.TopicShown(r.Context(), unread.TopicScope{Scope: scope, TopicID: *p.topicID}, m.Member.JoinedEventSeq, cursor, from, through)
	} else if shown {
		err = p.reading.FeedShown(r.Context(), scope, m.Member.JoinedEventSeq, cursor, from, through)
	} else if p.topicID != nil {
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

// readForm preserves body-only values; query keys cannot override the page's form.
// Numeric bounds belong to unread, shared by every caller.
func readForm(r *http.Request) (cursor, from, through int64, shown, valid bool) {
	for _, key := range []string{"cursor", "from", "through", "scope"} {
		if len(r.Form[key]) > 1 {
			return
		}
	}
	form := r.PostForm
	if r.Form.Has("before") || r.Form.Has("around") || !form.Has("cursor") ||
		form.Has("from") != form.Has("through") ||
		form.Has("scope") && (form.Get("scope") != "all" || form.Has("from") || form.Has("through")) {
		return
	}
	var err error
	cursor, err = strconv.ParseInt(form.Get("cursor"), 10, 64)
	if err != nil {
		return
	}
	shown = form.Has("from")
	if shown {
		from, err = strconv.ParseInt(form.Get("from"), 10, 64)
		if err != nil {
			return
		}
		through, err = strconv.ParseInt(form.Get("through"), 10, 64)
	}
	valid = err == nil
	return
}
