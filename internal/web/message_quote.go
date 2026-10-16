package web

import (
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// viewQuote shows entry's quote from the stored row and the slug alone
// (decision 38), so page and live renders match and the shared render cache
// stays right for every reader. A deleted original leaves no link.
func viewQuote(slug string, entry conversation.Entry) *view.Quote {
	if entry.Quote == nil {
		return nil
	}
	quote := &view.Quote{DisplayName: memberDisplayName(entry.QuoteAuthor.DisplayName), Handle: entry.QuoteAuthor.Handle, Text: entry.Quote.Text}
	if entry.Quote.MessageID != nil {
		quote.URL = view.MessageURL(slug, *entry.Quote.MessageID)
	}
	return quote
}
