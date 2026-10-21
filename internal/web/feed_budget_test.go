package web

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestFeedMarkupBudget(t *testing.T) {
	markup := renderIn(t, "en", view.ChannelScreen("/static/css/app.css", feedFixture(t, 50)))
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	hx := countElements(doc, hasHTMXAttribute)
	elements := countElements(doc, nil)
	// #1500: same English feedFixture(50), now 254826 bytes, 1631 elements
	// and 10 hx carriers. Keep this whole-page check alongside the standing
	// guard's slope/intercept and handler checks: counts have zero headroom;
	// bytes allow 5% rounded up. Its 267568-byte cap is one byte stricter
	// than the standing guard's rounded fixed + 50*slope byte caps.
	t.Logf("whole page: %d bytes, %d elements, %d hx elements", len(markup), elements, hx)
	if hx > 10 || len(markup) > 267568 || elements > 1631 {
		t.Fatalf("whole-page budget exceeded: bytes=%d/267568 elements=%d/1631 hx=%d/10", len(markup), elements, hx)
	}
}
