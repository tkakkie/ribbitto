package web

import (
	"bytes"
	"log/slog"
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

// The picker names each option through a computed message ID, so a key added
// without its two strings would fall back to English or to the raw ID. The
// catalogues log every fallback, which tells a missing Japanese string apart
// from a present one.
func TestChannelImageOptionNames(t *testing.T) {
	var logs bytes.Buffer
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ja"} {
		handler := catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			for _, key := range conversation.ChannelImageKeys() {
				id := "illustration.option." + key
				if name := i18n.T(r.Context(), id); name == id || strings.TrimSpace(name) == "" {
					t.Errorf("%s: %s has no name", lang, id)
				}
			}
		}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", lang)
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	if logs.Len() != 0 {
		t.Errorf("option names fell back:\n%s", logs.String())
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

// An image error belongs to the picker, never to the name field: each radio
// is invalid and described by the group's alert (docs/accessibility.md).
func TestImagePickerError(t *testing.T) {
	catalogues, err := i18n.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, form, errorID string }{
		{"create", "/organizations/acme/channels", "name=Plans&image=unknown", "channel-image-error"},
		{"overview", view.ChannelURL("acme", kernel.ID{1}) + "/overview", "image=unknown", "overview-image-error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, err := NewHandler("", catalogues, testServices(asAlice))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", w.Code)
			}
			doc, err := html.Parse(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			radios, alert := 0, false
			for n := range doc.Descendants() {
				switch {
				case n.DataAtom == atom.P && attr(n, "id") == tc.errorID:
					alert = attr(n, "role") == "alert"
				case n.DataAtom == atom.Input && attr(n, "name") == "name" && attr(n, "aria-invalid") == "true":
					t.Error("the name field is marked invalid for an image error")
				case n.DataAtom == atom.Input && attr(n, "type") == "radio" && attr(n, "aria-describedby") == tc.errorID:
					if attr(n, "aria-invalid") != "true" {
						t.Errorf("radio %q is described by the error but not invalid", attr(n, "value"))
					}
					radios++
				}
			}
			// Twelve images and the default.
			if !alert || radios != 13 {
				t.Fatalf("alert %v, radios linked to it = %d; want the alert and 13", alert, radios)
			}
		})
	}
}
