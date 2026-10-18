package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func (p channelPages) quote(w http.ResponseWriter, r *http.Request, m org.Membership) {
	id, ok := channelID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, ok := p.resolvePlace(w, r, m, id, ""); !ok || !parseForm(w, r) {
		return
	}
	quoted, ok := p.readQuote(w, r, m, id, r.PostForm)
	if ok {
		templ.Handler(view.ComposerQuote(view.ChannelPage{Quote: quoted}, false)).ServeHTTP(w, r)
	}
}

// Both entry paths and validation responses re-read through the same scoped
// snapshot; no author or text supplied by a browser becomes chip content.
func (p channelPages) readQuote(w http.ResponseWriter, r *http.Request, m org.Membership, channel kernel.ID, values url.Values) (*view.Message, bool) {
	raw, present := values["quote"]
	if !present {
		return nil, true
	}
	id, ok := pathID(raw[0])
	if !ok || len(raw) != 1 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return nil, false
	}
	entries, err := p.messages.Many(r.Context(), m, channel, []kernel.ID{id})
	if errors.Is(err, conversation.ErrMessageNotFound) || errors.Is(err, conversation.ErrChannelNotFound) || errors.Is(err, org.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		serverError(w, r, "reading quote", err)
		return nil, false
	}
	entry := entries[0]
	return &view.Message{ID: entry.ID, DisplayName: memberDisplayName(entry.DisplayName), Handle: entry.Handle,
		Body: conversation.PinExcerpt(entry, view.UnknownMember(r.Context()))}, true
}
