package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// fakePinning records each desired change and answers with err.
type fakePinning struct {
	calls []string
	err   error
}

func (f *fakePinning) Pin(_ context.Context, m org.Membership, channel, message kernel.ID) error {
	f.calls = append(f.calls, fmt.Sprintf("pin %s %x %x", m.Organization.Slug, channel, message))
	return f.err
}

func (f *fakePinning) Unpin(_ context.Context, m org.Membership, channel, message kernel.ID) error {
	f.calls = append(f.calls, fmt.Sprintf("unpin %s %x %x", m.Organization.Slug, channel, message))
	return f.err
}

func TestPinRoutes(t *testing.T) {
	base := view.PinURL("acme", kernel.ID{1}, kernel.ID{8})
	for _, tt := range []struct {
		name, path string
		err        error
		hx         bool
		headers    map[string]string
		status     int
		want       []string // in the body, or the Location for a 303
		calls      int
	}{
		{name: "pin", path: base + "/pin", status: 303, want: []string{view.MessageURL("acme", kernel.ID{8})}, calls: 1},
		{name: "unpin", path: base + "/unpin", status: 303, want: []string{view.MessageURL("acme", kernel.ID{8})}, calls: 1},
		// The control flips to the other desired action, keeping its ids for focus.
		{name: "enhanced pin", path: base + "/pin", hx: true, status: 200, want: []string{`action="` + base + `/unpin"`, `id="pin-true-` + view.MessageDOMID(kernel.ID{8}) + `"`, ">Unpin</button>"}, calls: 1},
		{name: "enhanced unpin", path: base + "/unpin", hx: true, status: 200, want: []string{`action="` + base + `/pin"`, ">Pin</button>"}, calls: 1},
		{name: "limit", path: base + "/pin", err: conversation.ErrPinLimit, status: 409, want: []string{"already has 50 pinned messages", `href="` + view.MessageURL("acme", kernel.ID{8}) + `"`}, calls: 1},
		{name: "enhanced limit", path: base + "/pin", err: conversation.ErrPinLimit, hx: true, status: 409, want: []string{`role="alert"`, "already has 50 pinned messages", ">Pin</button>"}, calls: 1},
		// Out-of-scope targets are the same 404 as missing ones.
		{name: "other channel", path: base + "/pin", err: conversation.ErrChannelNotFound, status: 404, calls: 1},
		{name: "other message", path: base + "/unpin", err: conversation.ErrMessageNotFound, status: 404, calls: 1},
		{name: "not a member", path: base + "/pin", err: org.ErrNotFound, status: 404, calls: 1},
		{name: "malformed message", path: view.ChannelURL("acme", kernel.ID{1}) + "/messages/x/pin", status: 404},
		{name: "cross-site", path: base + "/pin", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, status: 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pinning := &fakePinning{err: tt.err}
			handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Pinning = pinning })
			r := httptest.NewRequest(http.MethodPost, tt.path, nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			if tt.hx {
				r.Header.Set("HX-Request", "true")
			}
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			got := w.Body.String()
			if w.Code == http.StatusSeeOther {
				got = w.Header().Get("Location")
			}
			if w.Code != tt.status || len(pinning.calls) != tt.calls || tt.status == 404 && w.Body.String() != "404 page not found\n" {
				t.Fatalf("status %d, calls %v, body %q", w.Code, pinning.calls, w.Body.String())
			}
			for _, part := range tt.want {
				if !strings.Contains(got, part) {
					t.Fatalf("%q lacks %q", got, part)
				}
			}
			// The URL names the organisation, channel and message; the route names the action.
			action := strings.TrimPrefix(tt.path[strings.LastIndex(tt.path, "/"):], "/")
			if tt.calls == 1 && pinning.calls[0] != fmt.Sprintf("%s acme %x %x", action, kernel.ID{1}, kernel.ID{8}) {
				t.Fatalf("call %q", pinning.calls[0])
			}
		})
	}
}

// A repeated submission asks for the same state again: a stale Pin form pins
// (a no-op in the use case), never toggles to unpin.
func TestPinRepeatSubmissions(t *testing.T) {
	pinning := &fakePinning{}
	handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Pinning = pinning })
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, view.PinURL("acme", kernel.ID{1}, kernel.ID{8})+"/pin", nil)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	want := fmt.Sprintf("pin acme %x %x", kernel.ID{1}, kernel.ID{8})
	if !slices.Equal(pinning.calls, []string{want, want}) {
		t.Fatalf("calls %v", pinning.calls)
	}
}

func TestPinControlMarkup(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		for _, pinned := range []bool{false, true} {
			entry := conversation.Entry{Message: conversation.Message{ID: kernel.ID{8}, ChannelID: kernel.ID{1}, Pinned: pinned}, Handle: "alice"}
			got := renderIn(t, lang, view.MessageItem(viewMessage("acme", entry)))
			action, label := "/pin", map[string]string{"en": "Pin", "ja": "ピン留め"}[lang]
			if pinned {
				action, label = "/unpin", map[string]string{"en": "Unpin", "ja": "ピン留めを外す"}[lang]
			}
			form := `<form id="pin-form-` + view.MessageDOMID(kernel.ID{8}) + `" method="post" action="` + view.PinURL("acme", kernel.ID{1}, kernel.ID{8}) + action + `"`
			if !strings.Contains(got, form) || !strings.Contains(got, `type="submit"`) || !strings.Contains(got, ">"+label+"</button>") || strings.Contains(got, `role="alert"`) {
				t.Fatalf("%s pinned=%v: %s", lang, pinned, got)
			}
		}
	}
}

// renderIn renders a component with lang's catalogue, as a request would.
func renderIn(t *testing.T, lang string, c templ.Component) string {
	t.Helper()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", lang)
	catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if err := c.Render(r.Context(), &b); err != nil {
			t.Error(err)
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
	return b.String()
}
