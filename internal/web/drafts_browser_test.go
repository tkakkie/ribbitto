package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// draftsSignIn rejects every sign-in, renders the horizon the test sets and
// signs the browser out unless failOut is set.
type draftsSignIn struct {
	signedIn, failOut atomic.Bool
	horizon           atomic.Pointer[kernel.ID]
}

func (f *draftsSignIn) SignIn(context.Context, string, string, string) (string, time.Time, error) {
	return "", time.Time{}, identity.ErrInvalidCredentials
}

func (f *draftsSignIn) SignOut(context.Context, string) error {
	if f.failOut.Load() {
		return errors.New("connection refused")
	}
	f.signedIn.Store(false)
	return nil
}

func (f *draftsSignIn) SessionHorizon(context.Context) (kernel.ID, error) {
	return *f.horizon.Load(), nil
}

func (f *draftsSignIn) SignOutOthers(context.Context, kernel.ID, kernel.ID) error {
	return nil
}

// testDraftsBrowser covers the sign-in page's drafts cleanup and notice and
// sign-out's Clear-Site-Data through the real sign-in routes (decision 37 (d)).
func testDraftsBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service := &draftsSignIn{}
	handler, err := NewHandler("", catalogues, testServices(func(s *Services) { s.Sessions, s.SignIn = oneSession{}, service }))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service.signedIn.Load() {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	// state sets who is signed in, whether sign-out fails and the horizon
	// the next sign-in page renders.
	state := func(signedIn, failOut bool, horizon string) {
		id, _ := pathID(horizon)
		service.signedIn.Store(signedIn)
		service.failOut.Store(failOut)
		service.horizon.Store(&id)
	}
	const (
		notice  = "ribbitto.signed-out.v1"
		member  = "0190a000-0000-7000-8000-00000000000a"
		channel = "0190a000-0000-7000-8000-00000000000c"
		horizon = "019a1b2c-3d4e-7800-8800-000000000000"
	)
	key := func(session, place string) string {
		return "ribbitto.draft.v1:" + member + ":" + session + ":" + channel + ":" + place
	}
	var (
		older     = key("019a1b2c-3d4d-7fff-bfff-ffffffffffff", "feed") // one millisecond before, every later bit set
		sameBelow = key("019a1b2c-3d4e-7000-8000-000000000000", "feed")
		sameAbove = key("019a1b2c-3d4e-7fff-bfff-ffffffffffff", channel)
		later     = key("019a1b2c-3d4f-7000-8000-000000000000", "feed")
		// Malformed although later: no fields, an upper-case session or topic.
		malformed = []string{"ribbitto.draft.v1:x", key("019b0000-0000-7000-8000-00000000000A", "feed"), key("019b0000-0000-7000-8000-000000000000", strings.ToUpper(channel))}
		all       = append([]string{older, sameBelow, sameAbove, later, "ribbitto.other"}, malformed...)
	)
	newTab := func(t *testing.T, path string) *rod.Page {
		tab := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
		t.Cleanup(tab.MustClose)
		tab.MustNavigate(server.URL + path).MustWaitLoad()
		return tab
	}
	seed := func(tab *rod.Page, keys []string) {
		tab.MustEval(`keys => { localStorage.clear(); for (const k of keys) localStorage.setItem(k, "{}"); }`, keys)
	}
	// expect compares the whole of localStorage with keys holding "{}" and
	// the notice's value, if any.
	expect := func(t *testing.T, tab *rod.Page, keys []string, noticeValue string) {
		t.Helper()
		var want []string
		for _, k := range keys {
			want = append(want, k+"={}")
		}
		if noticeValue != "" {
			want = append(want, notice+"="+noticeValue)
		}
		slices.Sort(want)
		got := tab.MustEval(`() => Object.entries(localStorage).map(([k, v]) => k + "=" + v).sort().join("\n")`).Str()
		if got != strings.Join(want, "\n") {
			t.Fatalf("localStorage:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
		}
	}
	submit := func(tab *rod.Page, form string) {
		wait := tab.WaitNavigation(proto.PageLifecycleEventNameLoad)
		tab.MustElement(form).MustEval(`function () { this.requestSubmit(); }`)
		wait()
	}

	t.Run("cleanup", func(t *testing.T) {
		state(false, false, horizon)
		tab := newTab(t, "/")
		seed(tab, all)
		listener := newTab(t, "/")
		listener.MustEval(`() => { window.notices = []; addEventListener("storage", e => notices.push(e.key + "=" + e.newValue)); }`)
		tab.MustNavigate(server.URL + "/signin").MustWaitLoad()
		expect(t, tab, []string{sameBelow, sameAbove, later, "ribbitto.other"}, horizon)
		listener.MustWait(`() => notices.includes("` + notice + `=` + horizon + `")`)
	})

	t.Run("sign-out without scripts", func(t *testing.T) {
		state(true, false, "019a1b2c-3d4e-7900-8000-000000000000")
		tab := newTab(t, "/")
		router := tab.HijackRequests()
		router.MustAdd("*.js", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) })
		go router.Run()
		defer router.MustStop()
		seed(tab, all)
		submit(tab, `form[action="/signout"]`)
		if !strings.HasSuffix(tab.MustInfo().URL, "/signin") {
			t.Fatalf("not signed out: %s", tab.MustInfo().URL)
		}
		// No script ran, so there is no notice: the header alone cleared storage.
		expect(t, tab, nil, "")
	})

	t.Run("failed sign-out", func(t *testing.T) {
		state(true, true, "019a1b2c-3d4e-7a00-8000-000000000000")
		tab := newTab(t, "/")
		seed(tab, all)
		submit(tab, `form[action="/signout"]`)
		if !strings.HasSuffix(tab.MustInfo().URL, "/signout") {
			t.Fatalf("sign-out did not fail: %s", tab.MustInfo().URL)
		}
		expect(t, tab, all, "")
	})

	// A stale sign-in form submitted after another tab signed in: rejected
	// credentials leave that session, so its drafts stay and no notice goes out.
	t.Run("signed-in rejected credentials", func(t *testing.T) {
		state(false, false, "019a1b2c-3d4e-7b00-8000-000000000000")
		tab := newTab(t, "/signin")
		state(true, false, "019a1b2c-3d4f-7c00-8000-000000000000")
		// Then with malformed keys alone, which a cleanup would remove first.
		for _, keys := range [][]string{all, malformed} {
			seed(tab, keys)
			tab.MustEval(`() => { document.querySelector("#email").value = "alice@example.com"; document.querySelector("#password").value = "wrong password 123"; }`)
			submit(tab, `form[action="/signin"]`)
			if !tab.MustEval(`() => !!document.querySelector("[role=alert]") && !document.querySelector("[data-drafts-horizon]")`).Bool() {
				t.Fatal("no re-rendered form without a horizon")
			}
			expect(t, tab, keys, "")
		}
	})
}
