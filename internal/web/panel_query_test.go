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

// Panel links carry the position the page renders (#936, #1170): the older
// page's before or around and the divider it shows, never the request's
// other keys, on the plain branching error too. The older link reads the same divider, so it
// shows what the page displays.
func TestPanelLinksFollowPageState(t *testing.T) {
	channel := view.ChannelURL("acme", kernel.ID{1})
	selection := "08000000-0000-0000-0000-000000000000/31000000-0000-0000-0000-000000000000"
	branchError := func(fields ...string) url.Values {
		form := url.Values{"message": {selection}, "name": {strings.Repeat("x", 81)}}
		for i := 0; i < len(fields); i += 2 {
			form.Set(fields[i], fields[i+1])
		}
		return form
	}
	for _, tc := range []struct {
		name, method, target string
		form                 url.Values
		snapshot             int64
		status               int
		query, branchUnread  string
	}{
		{"older page", "GET", channel + "?before=9&unread=4", nil, 6, 200, "?before=9&unread=4", "4"},
		{"older page, nothing unread", "GET", channel + "?before=9&unread=0", nil, 6, 200, "?before=9&unread=0", "0"},
		{"older page without divider", "GET", channel + "?before=9", nil, 6, 200, "?before=9&unread=6", "6"},
		{"unknown keys", "GET", channel + "?before=9&unread=4&x=1", nil, 6, 200, "?before=9&unread=4", "4"},
		{"latest page", "GET", channel + "?after=zzz&x=1", nil, 6, 200, "", ""},
		{"around page", "GET", channel + "?around=9&unread=4&x=1", nil, 6, 200, "?around=9&unread=4", "4"},
		{"around page without divider", "GET", channel + "?around=9", nil, 6, 200, "?around=9&unread=6", "6"},
		{"branching error, around page", "POST", channel + "/branch", branchError("before", "0", "around", "9", "unread", "4"), 6, 422, "?around=9&unread=4", "4"},
		{"branching error, older page", "POST", channel + "/branch", branchError("before", "9", "unread", "4"), 6, 422, "?before=9&unread=4", "4"},
		{"branching error, nothing unread", "POST", channel + "/branch", branchError("before", "9", "unread", "0"), 6, 422, "?before=9&unread=0", "0"},
		{"branching error, no divider sent", "POST", channel + "/branch", branchError("before", "9"), 6, 422, "?before=9&unread=6", "6"},
		{"branching error, latest page", "POST", channel + "/branch", branchError("before", "0", "unread", "4"), 6, 422, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogues, err := i18n.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			messages := olderMessages()
			messages.firstUnread = tc.snapshot
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Messages, s.Branching = messages, testBrancher(&fakeBranchWriter{})
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
			panels, branchUnread, branchAround, older := 0, "", "", ""
			for n := range doc.Descendants() {
				href := attr(n, "href")
				switch {
				case n.DataAtom == atom.A && (strings.HasPrefix(href, channel+"/members") || strings.HasPrefix(href, channel+"/overview")):
					panels++
					if want := strings.SplitN(href, "?", 2)[0] + tc.query; href != want {
						t.Errorf("panel link = %q, want %q", href, want)
					}
				case n.DataAtom == atom.A && strings.HasPrefix(href, channel+"?before="):
					u, err := url.Parse(href)
					if err != nil {
						t.Fatal(err)
					}
					older = u.Query().Get("unread")
				case n.DataAtom == atom.Input && attr(n, "name") == "unread":
					branchUnread = attr(n, "value")
				case n.DataAtom == atom.Input && attr(n, "name") == "around":
					branchAround = attr(n, "value")
				}
			}
			if panels == 0 {
				t.Fatal("no panel link")
			}
			if branchUnread != tc.branchUnread {
				t.Errorf("branch form unread = %q, want %q", branchUnread, tc.branchUnread)
			}
			// The older link always carries the divider the page displays.
			linked, err := url.ParseQuery(strings.TrimPrefix(tc.query, "?"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.query != "" && older != linked.Get("unread") {
				t.Errorf("older link divider %q, panel links %q", older, linked.Get("unread"))
			}
			// A failed plain branch returns to the same around page.
			if branchAround != linked.Get("around") {
				t.Errorf("branch form around = %q, want %q", branchAround, linked.Get("around"))
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
		{"kept around position", "?around=9&unread=4&x=1", http.StatusSeeOther, 1, overview + "?around=9&unread=4"},
		{"malformed around", "?around=abc", http.StatusBadRequest, 0, ""},
		{"around with before", "?around=9&before=9", http.StatusBadRequest, 0, ""},
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
