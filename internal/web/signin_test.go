package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// fakeSignIn records its calls and answers with err.
type fakeSignIn struct {
	err       error
	previous  string
	signedOut []string
}

func (f *fakeSignIn) SignIn(_ context.Context, _, _, previous string) (string, time.Time, error) {
	f.previous = previous
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return "new-token", time.Now().Add(time.Hour), nil
}

func (f *fakeSignIn) SignOut(_ context.Context, token string) error {
	f.signedOut = append(f.signedOut, token)
	return f.err
}

// oneSession signs in whoever sends the cookie value "live".
type oneSession struct{}

func (oneSession) Resolve(_ context.Context, token string) (identity.Account, identity.Session, error) {
	if token == "live" {
		return identity.Account{ID: kernel.ID{1}, DisplayName: "Alice"}, identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	return identity.Account{}, identity.Session{}, identity.ErrNoSession
}

func newSignInHandler(t *testing.T, service *fakeSignIn) http.Handler {
	t.Helper()
	var logs bytes.Buffer
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(func(s *Services) { s.Sessions, s.SignIn = oneSession{}, service }))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func postSignIn(handler http.Handler, form url.Values, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/signin", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestSignInPage(t *testing.T) {
	handler := newSignInHandler(t, &fakeSignIn{})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/signin", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="/signin"`) {
		t.Fatalf("GET /signin: %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/signin", nil)
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("signed-in GET /signin: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestSignInPost(t *testing.T) {
	form := url.Values{"email": {"alice@example.com"}, "password": {"secret password 123"}}
	t.Run("success", func(t *testing.T) {
		service := &fakeSignIn{}
		w := postSignIn(newSignInHandler(t, service), form, "stale")
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
			t.Fatalf("status %d, Location %q", w.Code, w.Header().Get("Location"))
		}
		// The middleware clears the dead "stale" cookie, then the new one is set.
		cookies := w.Header().Values("Set-Cookie")
		if len(cookies) == 0 || !strings.HasPrefix(cookies[len(cookies)-1], "__Host-session=new-token;") {
			t.Fatalf("Set-Cookie = %q", cookies)
		}
		if service.previous != "stale" {
			t.Fatalf("previous token passed to SignIn = %q", service.previous)
		}
	})
	// Unknown email and wrong password both arrive as ErrInvalidCredentials
	// (identity tests prove that); the page must not differ by input either.
	var bodies []string
	for _, email := range []string{"alice@example.com", "nobody@example.com"} {
		f := url.Values{"email": {email}, "password": {"wrong password 123"}}
		w := postSignIn(newSignInHandler(t, &fakeSignIn{err: identity.ErrInvalidCredentials}), f, "")
		body := w.Body.String()
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "The email address or password is incorrect.") ||
			!strings.Contains(body, `value="`+email+`"`) || strings.Contains(body, "wrong password 123") {
			t.Fatalf("invalid credentials for %s: %d %s", email, w.Code, body)
		}
		// The CSP nonce differs per response by design.
		body = regexp.MustCompile(`nonce="[^"]*"`).ReplaceAllString(body, `nonce=""`)
		bodies = append(bodies, strings.ReplaceAll(body, email, "EMAIL"))
	}
	if bodies[0] != bodies[1] {
		t.Error("responses for a known and an unknown email differ")
	}
	for _, tt := range []struct {
		name   string
		err    error
		status int
		text   string
	}{
		{"invalid input", identity.ErrInvalidInput, http.StatusUnprocessableEntity, "Enter your email address and password."},
		{"busy hasher", identity.ErrBusy, http.StatusServiceUnavailable, "Service Unavailable"},
		{"store error", errors.New("connection refused"), http.StatusInternalServerError, "Internal Server Error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := postSignIn(newSignInHandler(t, &fakeSignIn{err: tt.err}), form, "")
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.text) || strings.Contains(w.Body.String(), "secret password 123") {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSignOut(t *testing.T) {
	service := &fakeSignIn{}
	handler := newSignInHandler(t, service)
	r := httptest.NewRequest(http.MethodPost, "/signout", nil)
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	cookies := w.Header().Values("Set-Cookie")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/signin" ||
		len(cookies) != 1 || !strings.Contains(cookies[0], "Max-Age=0") {
		t.Fatalf("status %d, Location %q, Set-Cookie %q", w.Code, w.Header().Get("Location"), cookies)
	}
	if len(service.signedOut) != 1 || service.signedOut[0] != "live" {
		t.Fatalf("SignOut calls = %q", service.signedOut)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/signout", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /signout: %d, want 405", w.Code)
	}
	// The signed-in home page offers sign-out as a POST form.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `<form method="post" action="/signout">`) || !strings.Contains(w.Body.String(), "Alice") {
		t.Fatalf("signed-in home page lacks the sign-out form: %s", w.Body.String())
	}
}
