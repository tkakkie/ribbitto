package web

import (
	"bytes"
	"context"
	"fmt"
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

func newLimitedHandler(t *testing.T, overrides ...func(*Services)) (http.Handler, *middleware.AuthLimits) {
	t.Helper()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	services := testServices(overrides...)
	services.Limits = middleware.NewAuthLimits(nil, func() time.Time { return now })
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

// setupFrom serves setup and sign-up from one fake.
func setupFrom(setup *fakeSetup) func(*Services) {
	return func(s *Services) { s.Setup, s.SignUp, s.SetupSessions = setup, fakeSignUp{setup}, setup }
}

// drain empties a limiter's bucket for the test client.
func drain(limiter *middleware.RateLimiter) {
	for limiter.Allow(netip.MustParsePrefix("192.0.2.1/32")) {
	}
}

func TestSignInRateLimit(t *testing.T) {
	service := &countingSignIn{fakeSignIn: fakeSignIn{err: auth.ErrInvalidCredentials}}
	handler, _ := newLimitedHandler(t, func(s *Services) { s.SignIn = service })
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
			handler, limits := newLimitedHandler(t, setupFrom(setup))
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
			handler, limits := newLimitedHandler(t, setupFrom(setup))
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
			handler, limits := newLimitedHandler(t, setupFrom(setup))
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

// An exhausted IPv6 /48 bucket keeps the same order as a client bucket:
// a closed route still answers 404, and an open one answers 429 without
// reading the form or reaching the use case, even for a /64 in that /48
// that has never been seen.
func TestExhaustedNetworkLimit(t *testing.T) {
	drainNetwork := func(limiter *middleware.RateLimiter) {
		for i := range 64 {
			for limiter.Allow(netip.MustParsePrefix(fmt.Sprintf("2001:db8:0:%x::/64", i))) {
			}
		}
	}
	post := func(handler http.Handler, path string, body io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, body)
		r.RemoteAddr = "[2001:db8:0:ffff::1]:1234"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/setup", "/signup"} {
		t.Run("closed "+path, func(t *testing.T) {
			setup := &fakeSetup{}
			handler, limits := newLimitedHandler(t, setupFrom(setup))
			drainNetwork(limits.Setup)
			drainNetwork(limits.SignUp)
			if w := post(handler, path, strings.NewReader("a=b")); w.Code != http.StatusNotFound {
				t.Fatalf("status %d", w.Code)
			}
		})
	}
	for _, path := range []string{"/signin", "/setup", "/signup"} {
		t.Run("open "+path, func(t *testing.T) {
			signIn := &countingSignIn{}
			setup := &fakeSetup{open: true}
			handler, limits := newLimitedHandler(t, setupFrom(setup), func(s *Services) { s.SignIn = signIn })
			drainNetwork(limits.SignIn)
			drainNetwork(limits.Setup)
			drainNetwork(limits.SignUp)
			body := &readRecorder{}
			w := post(handler, path, body)
			if w.Code != http.StatusTooManyRequests || body.reads != 0 || signIn.calls != 0 || setup.completed {
				t.Fatalf("status %d, body read %d times, sign-in calls %d, setup completed %v", w.Code, body.reads, signIn.calls, setup.completed)
			}
		})
	}
}
