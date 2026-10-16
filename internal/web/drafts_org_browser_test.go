package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// currentLogin resolves every request to the login the test chose last.
type currentLogin struct{ atomic.Pointer[draftsLogin] }

func (c *currentLogin) Resolve(ctx context.Context, _ string) (identity.Account, identity.Session, error) {
	return draftsLogins{"": c.Load()}.Resolve(ctx, "")
}

// testOrgDraftsBrowser covers organisation pages' drafts cleanup: its
// precedence, the values it reads and the session's deadline (decision 37 (d)).
func testOrgDraftsBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	login := &currentLogin{}
	handler, err := NewHandler("", catalogues, testServices(func(s *Services) { s.Sessions, s.Authz = login, draftsLogins{} }))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	const (
		alice   = "0190a000-0000-7000-8000-00000000000a"
		bob     = "0190a000-0000-7000-8000-00000000000b"
		earlier = "019a1b2c-3d4d-7fff-bfff-ffffffffffff" // one millisecond before, every later bit set
		session = "019a1b2c-3d4e-7800-8800-000000000000"
		below   = "019a1b2c-3d4e-7000-8000-000000000000" // the page's millisecond, lower bits below
		above   = "019a1b2c-3d4e-7fff-bfff-ffffffffffff"
		later   = "019a1b2c-3d4f-7000-8000-000000000000"
		channel = "0190a000-0000-7000-8000-00000000000c"
		day     = 24 * time.Hour
	)
	place := func(n int) string { return fmt.Sprintf("0190a000-0000-7000-8000-%012x", n) }
	key := func(member, session, place string) string {
		return "ribbitto.draft.v1:" + member + ":" + session + ":" + channel + ":" + place
	}
	own := func(n int) string { return key(alice, session, place(n)) }
	// value is an own entry edited at, with fields replaced or removed (nil).
	value := func(expires, at time.Time, change map[string]any) string {
		entry := map[string]any{"member": alice, "session": session, "expires": expires.UnixMilli(), "text": "draft", "at": at.UnixMilli()}
		for k, v := range change {
			entry[k] = v
			if v == nil {
				delete(entry, k)
			}
		}
		b, _ := json.Marshal(entry)
		return string(b)
	}
	// open seeds the origin's storage, then loads path as alice in session,
	// which expires at expires.
	open := func(t *testing.T, path string, expires time.Time, seed map[string]string) *rod.Page {
		login.Store(&draftsLogin{alice, session, expires})
		tab := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
		t.Cleanup(tab.MustClose)
		// Records the longest timer a page asks for: setTimeout overflows
		// above 2^31 - 1 ms.
		tab.MustEvalOnNewDocument(`window.longestTimer = 0; const set = window.setTimeout; window.setTimeout = (f, d, ...a) => { longestTimer = Math.max(longestTimer, d || 0); return set(f, d, ...a); };`)
		tab.MustNavigate(server.URL + "/healthz").MustWaitLoad()
		tab.MustEval(`seed => { localStorage.clear(); for (const [k, v] of Object.entries(seed)) localStorage.setItem(k, v); }`, seed)
		tab.MustNavigate(server.URL + path).MustWaitLoad()
		return tab
	}
	expect := func(t *testing.T, tab *rod.Page, want map[string]string) {
		t.Helper()
		var lines []string
		for k, v := range want {
			lines = append(lines, k+"="+v)
		}
		slices.Sort(lines)
		got := tab.MustEval(`() => Object.entries(localStorage).map(([k, v]) => k + "=" + v).sort().join("\n")`).Str()
		if got != strings.Join(lines, "\n") {
			t.Fatalf("localStorage:\n%s\nwant:\n%s", got, strings.Join(lines, "\n"))
		}
	}

	for name, path := range map[string]string{"channel": view.ChannelURL("acme", kernel.ID{1}), "list": "/organizations/acme/channels"} {
		t.Run(name, func(t *testing.T) {
			// Beyond setTimeout's 2^31 - 1 ms, as real sessions are.
			expires, now := time.Now().Add(30*day), time.Now()
			kept := map[string]string{
				"ribbitto.signed-out.v1": earlier,
				"ribbitto.other":         "x",
				// Another session from the page's millisecond on: kept unread.
				key(alice, below, "feed"): "not JSON",
				key(bob, above, "feed"):   "not JSON",
				key(bob, later, "feed"):   "not JSON",
				// Own entries: fresh, at the edges of retention and of the
				// future's tolerance, and 4,000 code points of four bytes.
				own(1): value(expires, now, nil),
				own(2): value(expires, now.Add(-7*day+time.Hour), nil),
				own(3): value(expires, now.Add(day-time.Hour), nil),
				own(4): value(expires, now, map[string]any{"text": strings.Repeat("🐸", 4000)}),
			}
			removed := map[string]string{
				// Earlier sessions of either member, whatever their value.
				key(alice, earlier, "feed"): value(expires, now, map[string]any{"session": earlier}),
				key(bob, earlier, "feed"):   "not JSON",
				// Malformed keys: too few or too many parts, an upper-case
				// member or session, a channel or place that is not a UUID,
				// the page's session with another member, even with a valid value.
				"ribbitto.draft.v1:x":                                      "{}",
				key(bob, later, "feed") + ":x":                             "{}",
				key(strings.ToUpper(bob), later, "feed"):                   "{}",
				key(bob, strings.ToUpper(later), "feed"):                   "{}",
				"ribbitto.draft.v1:" + bob + ":" + later + ":general:feed": "{}",
				key(bob, later, "Feed"):                                    "{}",
				key(bob, session, "feed"):                                  value(expires, now, nil),
				// Malformed or expired own values.
				own(10): "not JSON",
				own(11): value(expires, now, map[string]any{"text": nil}),
				own(19): value(expires, now, map[string]any{"text": []string{}}),
				own(12): value(expires, now, map[string]any{"at": fmt.Sprint(now.UnixMilli())}),
				own(13): value(expires, now, map[string]any{"member": bob}),
				own(14): value(expires, now, map[string]any{"session": later}),
				own(15): value(expires.Add(time.Millisecond), now, nil),
				own(16): value(expires, now, map[string]any{"text": strings.Repeat("🐸", 4001)}),
				own(17): value(expires, now.Add(day+time.Minute), nil),
				own(18): value(expires, now.Add(-7*day-time.Minute), nil),
			}
			seed := maps.Clone(kept)
			maps.Copy(seed, removed)
			tab := open(t, path, expires, seed)
			expect(t, tab, kept)
			if longest := tab.MustEval(`() => longestTimer`).Num(); longest <= 0 || longest > 1<<31-1 {
				t.Fatalf("longest timer %v ms", longest)
			}
		})
	}

	// With no sign-in page, an open page removes its own session's entries
	// at the deadline, unread, even one written after its load.
	t.Run("deadline", func(t *testing.T) {
		expires := time.Now().Add(5 * time.Second)
		other := map[string]string{key(bob, later, "feed"): "not JSON"}
		seed := map[string]string{own(1): value(expires, time.Now(), nil)}
		maps.Copy(seed, other)
		tab := open(t, "/organizations/acme/channels", expires, seed)
		expect(t, tab, seed)
		tab.MustEval(`k => localStorage.setItem(k, "not JSON")`, own(2))
		tab.MustWait(`() => Object.keys(localStorage).every(k => !k.includes("` + session + `"))`)
		expect(t, tab, other)
	})
}
