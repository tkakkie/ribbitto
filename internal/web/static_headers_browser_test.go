package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// The static files' own CSP (#937) must not reach the pages that use them:
// sprite icons through <use> and illustrations through <img> still paint.
func testStaticHeadersBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	app, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", app)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
			Current:  view.Channel{ID: kernel.ID{1}, Name: "general", ImageURL: view.ChannelImageURL("lake")},
			Channels: []view.Channel{{ID: kernel.ID{1}, Name: "general", ImageURL: view.ChannelImageURL("lake")}}}
		templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustNavigate(server.URL + view.ChannelURL("acme", kernel.ID{1})).MustWaitLoad()
	got := page.MustEval(`async () => {
		const uses = [...document.querySelectorAll('use')];
		const images = [...document.images];
		await Promise.all(images.map(i => i.decode().catch(() => {})));
		const sprite = uses[0] && uses[0].getAttribute('href').split('#')[0];
		const policy = sprite ? (await fetch(sprite)).headers.get('content-security-policy') : '';
		return {
			uses: uses.length,
			blankUses: uses.filter(u => u.getBBox().width === 0).map(u => u.getAttribute('href')),
			images: images.length,
			blankImages: images.filter(i => i.naturalWidth === 0).map(i => i.getAttribute('src')),
			policy,
		};
	}`)
	if got.Get("policy").Str() != staticContentSecurityPolicy {
		t.Fatalf("sprite policy = %q, want the static CSP", got.Get("policy").Str())
	}
	if got.Get("uses").Int() == 0 || got.Get("images").Int() == 0 {
		t.Fatalf("page has %d icons and %d images, want both", got.Get("uses").Int(), got.Get("images").Int())
	}
	if blank := got.Get("blankUses").Arr(); len(blank) != 0 {
		t.Errorf("icons not painted: %v", blank)
	}
	if blank := got.Get("blankImages").Arr(); len(blank) != 0 {
		t.Errorf("images not painted: %v", blank)
	}
}
