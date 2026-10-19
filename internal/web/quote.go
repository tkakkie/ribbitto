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
	page := view.ChannelPage{}
	if p.readQuote(w, r, m, id, r.PostForm, &page) {
		status := http.StatusOK
		if page.QuoteInvalid {
			status = http.StatusUnprocessableEntity
		}
		templ.Handler(view.ComposerQuote(page, false), templ.WithStatus(status)).ServeHTTP(w, r)
	}
}

// Both entry paths and validation responses re-read through the same scoped
// snapshot; a submitted part is checked only after the scoped read.
func (p channelPages) readQuote(w http.ResponseWriter, r *http.Request, m org.Membership, channel kernel.ID, values url.Values, page *view.ChannelPage) bool {
	raw, present := values["quote"]
	part, editing := values["part"]
	if present && editing {
		http.Error(w, "Unprocessable Entity", http.StatusUnprocessableEntity)
		return false
	}
	if editing {
		raw, present = part, true
	}
	if !present {
		return true
	}
	id, ok := pathID(values.Get("quote"))
	if editing {
		id, ok = pathID(values.Get("part"))
	}
	if !ok || len(raw) != 1 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return false
	}
	entries, err := p.messages.Many(r.Context(), m, channel, []kernel.ID{id})
	if errors.Is(err, conversation.ErrMessageNotFound) || errors.Is(err, conversation.ErrChannelNotFound) || errors.Is(err, org.ErrNotFound) {
		http.NotFound(w, r)
		return false
	}
	if err != nil {
		serverError(w, r, "reading quote", err)
		return false
	}
	entry := entries[0]
	text := entry.Text(view.UnknownMember(r.Context()))
	page.Quote = &view.Message{ID: entry.ID, DisplayName: memberDisplayName(entry.DisplayName), Handle: entry.Handle,
		Body: conversation.PinExcerpt(entry, view.UnknownMember(r.Context()))}
	if r.Method == http.MethodPost && values.Has("quote_text") {
		submitted := values.Get("quote_text")
		page.QuoteText = &submitted
		_, err := conversation.ValidateQuotePart(submitted, text)
		page.QuoteInvalid = err != nil
	} else if editing {
		page.QuoteText = &text
	}
	return true
}
