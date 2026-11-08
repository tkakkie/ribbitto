package web

import (
	"context"
	"net/http"
	"net/url"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func viewReply(ctx context.Context, slug string, entry conversation.Entry) *view.Reply {
	if entry.Reply == nil {
		return nil
	}
	preview := &view.Reply{}
	if p := entry.ReplyPreview; p != nil && p.Available {
		preview.URL = view.MessageURL(slug, entry.Reply.MessageID)
		preview.DisplayName, preview.Handle = memberDisplayName(p.Author.DisplayName), p.Author.Handle
		preview.Text = p.Text(view.UnknownMember(ctx))
	}
	return preview
}

func (p channelPages) readReply(w http.ResponseWriter, r *http.Request, m org.Membership, channel kernel.ID, values url.Values, page *view.ChannelPage) bool {
	raw, present := values["reply"]
	if !present {
		return true
	}
	id, ok := pathID(values.Get("reply"))
	if !ok || len(raw) != 1 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return false
	}
	entries, err := p.messages.Many(r.Context(), m, channel, []kernel.ID{id})
	if isNotFound(err) {
		http.NotFound(w, r)
		return false
	}
	if err != nil {
		serverError(w, r, "reading reply target", err)
		return false
	}
	entry := entries[0]
	page.Reply = &view.Message{ID: entry.ID, DisplayName: memberDisplayName(entry.DisplayName), Handle: entry.Handle, Body: conversation.PinExcerpt(entry, view.UnknownMember(r.Context()))}
	return true
}
