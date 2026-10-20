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
	// Same whole-document, English feedFixture(50), rendered locally:
	// origin/main 51024b25: 275376 bytes, 1631 elements, 160 hx elements;
	// Stage 1: 257626 bytes, 1631 elements, 10 hx elements. Size/element
	// budgets allow 5% headroom; hx stays at the approved maximum of 30.
	// Pre-menu 2699e7ee message.templ was inspected only with git show:
	// it has no actions menu. We retain that menu's DOM contract in Stage 1,
	// rather than setting budgets from an unrendered historical estimate.
	t.Logf("whole page: %d bytes, %d elements, %d hx elements", len(markup), elements, hx)
	if hx > 30 || len(markup) > 270510 || elements > 1713 {
		t.Fatalf("whole-page budget exceeded: bytes=%d/270510 elements=%d/1713 hx=%d/30", len(markup), elements, hx)
	}
}
