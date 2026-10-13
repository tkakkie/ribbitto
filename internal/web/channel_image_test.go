package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestChannelImages(t *testing.T) {
	catalogues, err := i18n.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key, want string
		id        kernel.ID
	}{
		{"leaf", "leaf", kernel.ID{1}},
		{"", "flowers", kernel.ID{1}},
		{"removed", "flowers", kernel.ID{1}},
		{"../../missing", "flowers", kernel.ID{1}},
		{"", "mountain", kernel.ID{2}},
	} {
		t.Run(tc.key+tc.want, func(t *testing.T) {
			channels := &fakeChannels{channels: []conversation.Channel{{ID: tc.id, Name: "Example", Image: tc.key}}}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Channels, s.Messages = channels, fakeMessages{channels: channels}
			}))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, view.ChannelURL("acme", tc.id), nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("page status = %d", w.Code)
			}
			doc, err := html.Parse(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			images := 0
			for n := range doc.Descendants() {
				// The app bar's logo is an image too; a wrong channel URL
				// still fails the count below.
				if n.DataAtom != atom.Img || !strings.HasPrefix(attr(n, "src"), "/static/channel-images-v1/") {
					continue
				}
				inForm := false
				for a := n.Parent; a != nil; a = a.Parent {
					inForm = inForm || a.DataAtom == atom.Form
				}
				if inForm {
					continue
				}
				images++
				wantSize, wantLoading := "24", "lazy"
				for a := n.Parent; a != nil; a = a.Parent {
					if a.DataAtom == atom.Header {
						wantSize, wantLoading = "32", ""
					}
				}
				wantURL := "/static/channel-images-v1/" + tc.want + ".svg"
				alt, hasAlt := attrOK(n, "alt")
				if attr(n, "src") != wantURL || !hasAlt || alt != "" || attr(n, "width") != wantSize || attr(n, "height") != wantSize || attr(n, "loading") != wantLoading || attr(n, "decoding") != "async" {
					t.Errorf("image attributes = %v", n.Attr)
				}
			}
			if images != 2 {
				t.Fatalf("images = %d, want sidebar and header", images)
			}
		})
	}
}

func TestChannelImageAssets(t *testing.T) {
	handler, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range conversation.ChannelImageKeys() {
		t.Run(key, func(t *testing.T) {
			url := channelImageURL(kernel.ID{}, key)
			if !strings.HasSuffix(url, "/"+key+".svg") {
				t.Fatalf("stored key resolved to %q", url)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
				t.Fatalf("asset status = %d, headers = %v", w.Code, w.Header())
			}
		})
	}
}

func TestWelcomeComposition(t *testing.T) {
	handler, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/signin"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			doc, err := html.Parse(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			images := 0
			for n := range doc.Descendants() {
				if n.DataAtom != atom.Img || !strings.HasPrefix(attr(n, "src"), "/static/channel-images-v1/") {
					continue
				}
				images++
				hidden := false
				for a := n.Parent; a != nil; a = a.Parent {
					hidden = hidden || attr(a, "aria-hidden") == "true"
				}
				if alt, ok := attrOK(n, "alt"); !ok || alt != "" || !hidden {
					t.Errorf("illustration %v is not decorative", n.Attr)
				}
			}
			if images != 4 {
				t.Fatalf("illustrations = %d, want the welcome card's four", images)
			}
		})
	}
}
