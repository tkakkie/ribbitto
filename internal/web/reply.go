package web

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
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
