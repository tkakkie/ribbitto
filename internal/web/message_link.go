package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// message answers a message's URL (decision 36) with a 303 to where the
// message is now: its topic's latest page when it is there, else the older
// page around it. A malformed or unknown ID and another organisation's
// message are the same 404, so the URL never says whether a message exists.
func (p channelPages) message(w http.ResponseWriter, r *http.Request, m org.Membership) {
	id, ok := pathID(r.PathValue("messageID"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	at, err := p.messages.Locate(r.Context(), m, id)
	if errors.Is(err, conversation.ErrMessageNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "locating message", err)
		return
	}
	target := view.ConversationURL(m.Organization.Slug, at.ChannelID, &at.TopicID)
	if !at.Latest {
		target += "?around=" + strconv.FormatInt(at.EventSeq, 10)
	}
	http.Redirect(w, r, target+"#"+view.MessageDOMID(id), http.StatusSeeOther)
}
