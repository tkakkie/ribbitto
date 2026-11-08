package web

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func (p channelPages) messageActions(w http.ResponseWriter, r *http.Request, m org.Membership) {
	id, ok := pathID(r.PathValue("messageID"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	at, err := p.messages.Locate(r.Context(), m, id)
	var entries []conversation.Entry
	if err == nil {
		// Re-read the message through the scoped reader: it may have moved or
		// disappeared since Locate, and the pin action needs its current state.
		entries, err = p.messages.Many(r.Context(), m, at.ChannelID, []kernel.ID{id})
	}
	switch {
	case isNotFound(err):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "reading message actions", err)
	default:
		entry := viewMessage(r.Context(), m.Organization.Slug, entries[0])
		conversationURL := view.ConversationURL(m.Organization.Slug, entries[0].ChannelID, &entries[0].TopicID)
		if !entry.DefaultTopic {
			entry.ResolveURL = conversationURL + "/resolve"
		}
		p.pages.render(w, r, http.StatusOK, func(url string) templ.Component {
			return view.MessageActionsPage(url, entry, conversationURL)
		})
	}
}
