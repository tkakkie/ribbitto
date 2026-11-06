package web

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestFeedMarkupBudget(t *testing.T) {
	for _, page := range feedGuardPages() {
		t.Run(page.name, func(t *testing.T) {
			markup := renderIn(t, "en", view.ChannelScreen("/static/css/app.css", page.fixture(t, 50)))
			doc, err := html.Parse(strings.NewReader(markup))
			if err != nil {
				t.Fatal(err)
			}
			hx := countElements(doc, hasHTMXAttribute)
			elements := countElements(doc, nil)
			// #1457: feedFixture(50) at 2d5917c: 258687 bytes / 1632 elements / 10 hx.
			// Shared menu: 196627 bytes / 1294 elements / 10 hx; bytes allow 5%
			// rounded up, counts no headroom. Historical #1453 dev-page references
			// (different fixture): pre-#1190 17d6ed2: 190337 bytes / 1364 elements;
			// 51024b25: 357322 bytes / 2018 elements / 163 hx. The tighter whole-page
			// cap replaces the suggested 1.5x historical per-message starting budget.
			t.Logf("whole page: %d bytes, %d elements, %d hx elements", len(markup), elements, hx)
			if hx > 10 || len(markup) > 206459 || elements > page.wholeElements {
				t.Fatalf("whole-page budget exceeded: bytes=%d/206459 elements=%d/%d hx=%d/10", len(markup), elements, page.wholeElements, hx)
			}
		})
	}
}
