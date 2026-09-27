package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// countingSignIn counts the attempts that reach the use case.
type countingSignIn struct {
	fakeSignIn
	calls int
}

func (c *countingSignIn) SignIn(ctx context.Context, email, password, previous string) (string, time.Time, error) {
	c.calls++
	return c.fakeSignIn.SignIn(ctx, email, password, previous)
}

func newLimitedHandler(t *testing.T, services Services) (http.Handler, *middleware.AuthLimits) {
	t.Helper()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	services.Limits = middleware.NewAuthLimits(nil, func() time.Time { return now })
	services.Sessions, services.Authz = noSessions{}, noOrganisations{}
	if services.SignIn == nil {
		services.SignIn = &fakeSignIn{}
	}
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	return handler, services.Limits
}

func postForm(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// drain empties a limiter's bucket for the test client.
func drain(limiter *middleware.RateLimiter) {
	for limiter.Allow(netip.MustParsePrefix("192.0.2.1/32")) {
	}
}

func TestSignInRateLimit(t *testing.T) {
	service := &countingSignIn{fakeSignIn: fakeSignIn{err: auth.ErrInvalidCredentials}}
	handler, _ := newLimitedHandler(t, Services{SignIn: service})
	form := url.Values{"email": {"a@example.com"}, "password": {"wrong password 123"}}.Encode()
	for i := range 5 {
		if w := postForm(handler, "/signin", form); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d: status %d", i+1, w.Code)
		}
	}
	w := postForm(handler, "/signin", form)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "Too many attempts") {
		t.Fatalf("sixth attempt: status %d", w.Code)
	}
	if service.calls != 5 {
		t.Fatalf("a limited attempt reached the use case: %d calls", service.calls)
	}
}

func TestClosedRoutesIgnoreLimits(t *testing.T) {
	huge := "a=" + strings.Repeat("x", 2*middleware.MaxBodyBytes)
	for _, tt := range []struct {
		name string
		path string
		open bool
	}{
		{"setup", "/setup", false},
		{"sign-up", "/signup", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup := &fakeSetup{open: tt.open}
			handler, limits := newLimitedHandler(t, Services{Setup: setup, SignUp: fakeSignUp{setup}, SetupSessions: setup})
			drain(limits.Setup)
			drain(limits.SignUp)
			for _, body := range []string{"a=b", huge} {
				if w := postForm(handler, tt.path, body); w.Code != http.StatusNotFound {
					t.Fatalf("closed %s with an empty bucket and a %d-byte body: status %d", tt.path, len(body), w.Code)
				}
			}
		})
	}
}

func TestOpenRoutesLimitedBeforeWork(t *testing.T) {
	for _, path := range []string{"/setup", "/signup"} {
		t.Run(path, func(t *testing.T) {
			setup := &fakeSetup{open: true}
			handler, limits := newLimitedHandler(t, Services{Setup: setup, SignUp: fakeSignUp{setup}, SetupSessions: setup})
			drain(limits.Setup)
			drain(limits.SignUp)
			w := postForm(handler, path, "email=a%40example.com&password=long+enough+password")
			if w.Code != http.StatusTooManyRequests || setup.completed {
				t.Fatalf("status %d, reached the use case: %v", w.Code, setup.completed)
			}
		})
	}
}

// readRecorder fails the test's expectation if anyone reads the body.
type readRecorder struct{ reads int }

func (r *readRecorder) Read([]byte) (int, error) {
	r.reads++
	return 0, io.EOF
}

func TestLimitedFormsAreNotParsed(t *testing.T) {
	for _, tt := range []struct {
		path  string
		drain func(*middleware.AuthLimits)
	}{
		{"/signin", func(l *middleware.AuthLimits) { drain(l.SignIn) }},
		{"/setup", func(l *middleware.AuthLimits) { drain(l.Setup) }},
		{"/signup", func(l *middleware.AuthLimits) { drain(l.SignUp) }},
	} {
		t.Run(tt.path, func(t *testing.T) {
			setup := &fakeSetup{open: true}
			handler, limits := newLimitedHandler(t, Services{SignIn: &fakeSignIn{}, Setup: setup, SignUp: fakeSignUp{setup}, SetupSessions: setup})
			tt.drain(limits)
			body := &readRecorder{}
			r := httptest.NewRequest(http.MethodPost, tt.path, body)
			r.RemoteAddr = "192.0.2.1:1234"
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusTooManyRequests || body.reads != 0 {
				t.Fatalf("status %d, body read %d times", w.Code, body.reads)
			}
		})
	}
}
