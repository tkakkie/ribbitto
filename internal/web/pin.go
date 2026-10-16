package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Pinning pins and unpins a channel's messages (conversation.Pinning, #888).
type Pinning interface {
	Pin(ctx context.Context, m org.Membership, channelID, messageID kernel.ID) error
	Unpin(ctx context.Context, m org.Membership, channelID, messageID kernel.ID) error
}

func (p channelPages) pin(w http.ResponseWriter, r *http.Request, m org.Membership) {
	p.changePin(w, r, m, true)
}

func (p channelPages) unpin(w http.ResponseWriter, r *http.Request, m org.Membership) {
	p.changePin(w, r, m, false)
}

// changePin serves POST …/channels/{channelID}/messages/{messageID}/pin and
// …/unpin. The organisation comes from the URL and the membership from the
// session; the use case checks the channel and message, so another
// channel's or organisation's message is the same 404 as a missing one.
// Success is 303 to the message without JavaScript; htmx gets the control
// for the new state. A full channel is 409 with an explanation either way.
func (p channelPages) changePin(w http.ResponseWriter, r *http.Request, m org.Membership, pin bool) {
	channel, ok := channelID(r)
	message, valid := pathID(r.PathValue("messageID"))
	if !ok || !valid || p.pinning == nil {
		http.NotFound(w, r)
		return
	}
	change := p.pinning.Unpin
	if pin {
		change = p.pinning.Pin
	}
	err := change(r.Context(), m, channel, message)
	status, errorKey := http.StatusOK, ""
	switch {
	case errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, conversation.ErrMessageNotFound), errors.Is(err, org.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, conversation.ErrPinLimit):
		status, errorKey, pin = http.StatusConflict, "pins.error.limit", false
	case err != nil:
		serverError(w, r, "changing pin", err)
		return
	}
	messageURL := view.MessageURL(m.Organization.Slug, message)
	if r.Header.Get("HX-Request") == "true" {
		showPins := r.Header.Get("X-Pins-Section") == "true"
		page := view.ChannelPage{Organization: view.Organization{Slug: m.Organization.Slug}}
		if showPins {
			state, err := p.messages.ReadPins(r.Context(), m, channel)
			if err != nil {
				serverError(w, r, "reading pin response", err)
				return
			}
			page.Pins, page.SnapshotSeq = viewPins(state.Pins), state.SnapshotSeq
		}
		entry := view.Message{ID: message, PinURL: view.PinURL(m.Organization.Slug, channel, message), Pinned: pin}
		templ.Handler(view.PinResponse(entry, errorKey, page, showPins), templ.WithStatus(status)).ServeHTTP(w, r)
		return
	}
	if errorKey != "" {
		p.pages.render(w, r, status, func(url string) templ.Component { return view.PinLimit(url, messageURL) })
		return
	}
	http.Redirect(w, r, messageURL, http.StatusSeeOther)
}
