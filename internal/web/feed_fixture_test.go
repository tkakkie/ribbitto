package web

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// feedFixture and countElements are reusable whole-page markup budget helpers.
// Use fixed seed authors, times and bodies so comparisons measure markup changes.
func feedFixture(t *testing.T, n int) view.ChannelPage {
	t.Helper()
	page := seedMessagePage(t)
	seed := page.Messages
	page.Messages = make([]view.Message, n)
	for i := range n {
		entry := seed[i%len(seed)]
		entry.ID, entry.EventSeq = kernel.ID{byte(i + 1)}, int64(i+1)
		entry.URL = view.MessageURL(page.Organization.Slug, entry.ID)
		entry.PinURL = view.PinURL(page.Organization.Slug, page.Current.ID, entry.ID)
		page.Messages[i] = entry
	}
	return page
}

func countElements(doc *html.Node, pred func(*html.Node) bool) int {
	n := 0
	for node := range doc.Descendants() {
		if node.Type == html.ElementNode && (pred == nil || pred(node)) {
			n++
		}
	}
	return n
}

func hasHTMXAttribute(n *html.Node) bool {
	for _, a := range n.Attr {
		if strings.HasPrefix(a.Key, "hx-") {
			return true
		}
	}
	return false
}
