package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Panel links carry the position the page renders (#936): the older page's
// before and unread, never the request's other keys, on error renders too.
func TestPanelLinksFollowPageState(t *testing.T) {
	channel := view.ChannelURL("acme", kernel.ID{1})
	selection := "08000000-0000-0000-0000-000000000000/31000000-0000-0000-0000-000000000000"
	for _, tc := range []struct {
		name, method, target string
		form                 url.Values
		status               int
		want                 string
	}{
		{"older page", "GET", channel + "?before=9&unread=4", nil, 200, channel + "/members?before=9&unread=4"},
		{"unknown keys", "GET", channel + "?before=9&x=1", nil, 200, channel + "/members?before=9"},
		{"stray after", "GET", channel + "?after=zzz", nil, 200, channel + "/members"},
		{"plain branching error", "POST", channel + "/branch",
			url.Values{"message": {selection}, "name": {strings.Repeat("x", 81)}, "before": {"9"}}, 422, channel + "/members?before=9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogues, err := i18n.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Messages, s.Branching = populatedMessages(), testBrancher(&fakeBranchWriter{})
			}))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.form.Encode()))
			if tc.form != nil {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			doc, err := html.Parse(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			links := 0
			for n := range doc.Descendants() {
				href := attr(n, "href")
				if n.DataAtom != atom.A || !strings.HasPrefix(href, channel+"/members") {
					continue
				}
				links++
				if href != tc.want {
					t.Errorf("members link = %q, want %q", href, tc.want)
				}
			}
			if links == 0 {
				t.Fatal("no members link")
			}
		})
	}
}

// An image change returns to the page's own position and refuses a
// malformed one before it writes (#936).
func TestImageChangeQuery(t *testing.T) {
	overview := view.ChannelURL("acme", kernel.ID{1}) + "/overview"
	for _, tc := range []struct {
		name, query  string
		status, sets int
		location     string
	}{
		{"kept position", "?before=9&unread=4&x=1", http.StatusSeeOther, 1, overview + "?before=9&unread=4"},
		{"latest page", "?x=1", http.StatusSeeOther, 1, overview},
		{"malformed before", "?before=abc", http.StatusBadRequest, 0, ""},
		{"unread without before", "?unread=4", http.StatusBadRequest, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channels := &fakeChannels{}
			catalogues, err := i18n.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Channels = channels }))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", overview+tc.query, strings.NewReader("image=moon"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || channels.imageSets != tc.sets || w.Header().Get("Location") != tc.location {
				t.Fatalf("status %d, image writes %d, Location %q; want %d, %d, %q",
					w.Code, channels.imageSets, w.Header().Get("Location"), tc.status, tc.sets, tc.location)
			}
		})
	}
}
