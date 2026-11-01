package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// fakeSignIn records its calls and answers with err; SessionHorizon
// answers with horizon and horizonErr.
type fakeSignIn struct {
	err        error
	previous   string
	signedOut  []string
	horizon    kernel.ID
	horizonErr error
	otherIDs   []kernel.ID
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

func (f *fakeSignIn) SignOutOthers(_ context.Context, accountID, currentID kernel.ID) error {
	f.otherIDs = append(f.otherIDs, accountID, currentID)
	return f.err
}

func TestSignOutOthers(t *testing.T) {
	for _, tt := range []struct {
		name, cookie, origin, location string
		err                            error
		status                         int
		called                         bool
	}{
		{"signed in", "live", "", "/", nil, 303, true},
		{"signed out", "", "", "/signin", nil, 303, false},
		{"stale", "stale", "", "/signin", nil, 303, false},
		{"store error", "live", "", "", errors.New("store unavailable"), 500, true},
		{"cross origin", "live", "https://attacker.example", "", nil, 403, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeSignIn{err: tt.err}
			r := httptest.NewRequest(http.MethodPost, "/signout/others", nil)
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: tt.cookie})
			}
			r.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			newSignInHandler(t, service).ServeHTTP(w, r)
			if w.Code != tt.status || w.Header().Get("Location") != tt.location {
				t.Fatalf("status %d, Location %q", w.Code, w.Header().Get("Location"))
			}
			if _, sent := w.Header()["Clear-Site-Data"]; sent {
				t.Fatal("sent Clear-Site-Data")
			}
			if tt.called {
				if len(service.otherIDs) != 2 || service.otherIDs[0] != (kernel.ID{1}) || service.otherIDs[1] != (kernel.ID{0x51}) {
					t.Fatalf("SignOutOthers ids = %v", service.otherIDs)
				}
			} else if len(service.otherIDs) != 0 {
				t.Fatal("called SignOutOthers")
			}
		})
	}
}

func (f *fakeSignIn) SessionHorizon(context.Context) (kernel.ID, error) {
	return f.horizon, f.horizonErr
}

// uuidv7At is a UUIDv7 whose millisecond prefix is at's.
func uuidv7At(at time.Time) string {
	ms := fmt.Sprintf("%012x", at.UnixMilli())
	return ms[:8] + "-" + ms[8:] + "-7abc-8000-000000000000"
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
		// The middleware keeps the dead "stale" cookie; only the new one is set.
		cookies := w.Header().Values("Set-Cookie")
		if len(cookies) != 1 || !strings.HasPrefix(cookies[0], "__Host-session=new-token;") {
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

// The sign-in page renders identity's horizon as it is, so a web server
// clock skewed against PostgreSQL's cannot move it, and only for signed-out
// requests (decision 37 (d)).
func TestSignInHorizon(t *testing.T) {
	rejected := url.Values{"email": {"alice@example.com"}, "password": {"wrong password 123"}}
	for _, skew := range []time.Duration{5 * time.Second, -5 * time.Second} {
		t.Run(skew.String(), func(t *testing.T) {
			// The database clock is 5 s ahead of or behind this server's.
			horizon := uuidv7At(time.Now().Add(skew))
			id, _ := pathID(horizon)
			handler := newSignInHandler(t, &fakeSignIn{err: identity.ErrInvalidCredentials, horizon: id})
			get := httptest.NewRecorder()
			handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/signin", nil))
			want := `data-drafts-horizon="` + horizon + `"`
			for name, w := range map[string]*httptest.ResponseRecorder{"GET": get, "failed POST": postSignIn(handler, rejected, "")} {
				if body := w.Body.String(); !strings.Contains(body, want) || strings.Count(body, "data-drafts-horizon") != 1 {
					t.Errorf("%s: want one %s", name, want)
				}
			}
			// A stale form in a signed-in browser: rejected credentials leave
			// the session, so its drafts stay.
			if w := postSignIn(handler, rejected, "live"); w.Code != http.StatusUnprocessableEntity || strings.Contains(w.Body.String(), "data-drafts-horizon") {
				t.Errorf("signed-in failed POST: %d, horizon rendered", w.Code)
			}
		})
	}
	w := httptest.NewRecorder()
	newSignInHandler(t, &fakeSignIn{horizonErr: errors.New("connection refused")}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/signin", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "data-drafts-horizon") {
		t.Errorf("failed horizon read: %d, horizon rendered", w.Code)
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
	// Storage only: "cookies" would reach the whole registrable domain.
	if got := w.Header().Values("Clear-Site-Data"); len(got) != 1 || got[0] != `"storage"` {
		t.Fatalf("Clear-Site-Data = %q", got)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/signout", nil))
	if got := w.Header().Values("Clear-Site-Data"); w.Code != http.StatusSeeOther || len(got) != 1 || got[0] != `"storage"` {
		t.Fatalf("without a cookie: %d, Clear-Site-Data = %q", w.Code, got)
	}
	// A failed sign-out keeps the session, so the drafts stay too.
	r = httptest.NewRequest(http.MethodPost, "/signout", nil)
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w = httptest.NewRecorder()
	newSignInHandler(t, &fakeSignIn{err: errors.New("connection refused")}).ServeHTTP(w, r)
	if _, sent := w.Header()["Clear-Site-Data"]; w.Code != http.StatusInternalServerError || sent {
		t.Fatalf("failed sign-out: %d, headers %v", w.Code, w.Header())
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

// Authentication and cancellation use the real store, middleware and hub.
func TestSignOutOthersAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	hub := realtime.NewHub()
	sessions := identitypg.NewSessions(pool, time.Now, hub)
	accountID := identitytest.Account(t, pool, "sessions@example.com", "Sessions")
	tokens := make([]string, 2)
	streams := make([]context.Context, 2)
	for i := range tokens {
		token, _, err := sessions.Create(t.Context(), accountID)
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = token
		_, session, err := sessions.Resolve(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}
		stream, unregister, err := hub.Register(t.Context(), realtime.Connection{Account: accountID, Session: session.ID}, 2)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(unregister)
		streams[i] = stream
	}
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(func(s *Services) {
		s.Sessions, s.SignIn = sessions, identity.NewSignIn(nil, nil, sessions)
	}))
	if err != nil {
		t.Fatal(err)
	}
	w := serveForm(handler, http.MethodPost, "/signout/others", tokens[0], nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || len(w.Header().Values("Set-Cookie")) != 0 || len(w.Header().Values("Clear-Site-Data")) != 0 {
		t.Fatalf("sign-out response: %d, %v", w.Code, w.Header())
	}
	if streams[0].Err() != nil || !errors.Is(context.Cause(streams[1]), realtime.ErrSessionEnded) {
		t.Fatalf("stream causes: current %v, other %v", context.Cause(streams[0]), context.Cause(streams[1]))
	}
	for i, want := range []int{http.StatusSeeOther, http.StatusOK} {
		w := serveForm(handler, http.MethodGet, "/signin", tokens[i], nil)
		if w.Code != want {
			t.Fatalf("session %d next request: %d, want %d", i, w.Code, want)
		}
	}
}
