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
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

type fakeSignUp struct{ *fakeSetup }

func (f fakeSignUp) SignUp(ctx context.Context, name, handle, email, password string) (domain.ID, error) {
	result, err := f.Complete(ctx, "", setup.Input{DisplayName: name, Handle: handle, Email: email, Password: password})
	return result.AccountID, err
}

func TestSignUp(t *testing.T) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"token": {"secret-token"}, "organization_name": {"Example"}, "slug": {"example"}, "display_name": {"Owner"}, "handle": {"owner"}, "email": {"owner@example.com"}, "password": {"secret-password"}}
	for _, tt := range []struct {
		name    string
		service *fakeSetup
		status  int
		message string
	}{
		{"success", &fakeSetup{open: true}, 303, ""},
		{"disabled", nil, 404, ""},
		{"completed", &fakeSetup{}, 404, ""},
		{"completed concurrently", &fakeSetup{open: true, err: fmt.Errorf("wrapped: %w", signup.ErrClosed)}, 404, ""},
		{"name", &fakeSetup{open: true, err: setup.ValidationErrors{"display_name": errors.New("private detail")}}, 422, "Enter a display name of 1–50 printable characters, at least one of them visible."},
		{"email", &fakeSetup{open: true, err: setup.ValidationErrors{"email": errors.New("private detail")}}, 422, "Enter a valid email address."},
		{"password", &fakeSetup{open: true, err: setup.ValidationErrors{"password": errors.New("private detail")}}, 422, "Use a password of 15–128 characters."},
		{"handle", &fakeSetup{open: true, err: setup.ValidationErrors{"handle": errors.New("private detail")}}, 422, "Use 2–32 characters: lowercase letters, digits, _, . or -"},
		{"duplicate email", &fakeSetup{open: true, err: signup.ErrEmailTaken}, 422, "This email address is already registered."},
		{"duplicate handle", &fakeSetup{open: true, err: fmt.Errorf("wrapped: %w", signup.ErrHandleTaken)}, 422, "This handle is already taken in this organisation."},
		{"busy", &fakeSetup{open: true, err: fmt.Errorf("wrapped: %w", auth.ErrBusy)}, 503, ""},
		{"availability failure", &fakeSetup{openErr: errors.New("private detail")}, 500, ""},
		{"completion failure", &fakeSetup{open: true, err: errors.New("private detail")}, 500, ""},
		{"session failure", &fakeSetup{open: true, sessionErr: errors.New("private detail")}, 500, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			services := Services{Channels: &fakeChannels{}, Authz: noOrganisations{}, Sessions: noSessions{}, SetupSessions: tt.service, SignIn: &fakeSignIn{err: auth.ErrInvalidInput}}
			if tt.service != nil {
				services.SignUp = fakeSignUp{tt.service}
			}
			handler, err := NewHandler("", catalogues, services)
			if err != nil {
				t.Fatal(err)
			}
			if tt.service == nil || tt.service.openErr == nil {
				for _, method := range []string{"GET", "POST"} {
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, httptest.NewRequest(method, "/signin", nil))
					if strings.Contains(w.Body.String(), `href="/signup"`) != (tt.service != nil && tt.service.open) {
						t.Fatal("incorrect sign-up link availability")
					}
				}
			}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				r := httptest.NewRequest(method, "/signup", strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				// The browser is already signed in: the new session must replace it.
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "previous-token"})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				want := tt.status
				if method == http.MethodGet && tt.service != nil && tt.service.open {
					want = 200
				}
				body := w.Body.String()
				if w.Code != want {
					t.Fatalf("%s: status %d, want %d", method, w.Code, want)
				}
				for _, secret := range []string{"secret-token", "secret-password", "private detail"} {
					if strings.Contains(body, secret) {
						t.Fatalf("%s: echoed %s", method, secret)
					}
				}
				if want == 200 && !strings.Contains(body, `action="/signup"`) {
					t.Fatal("missing form")
				}
				if want == 422 {
					if !strings.Contains(body, tt.message) || !strings.Contains(body, `role="alert"`) {
						t.Fatal("missing error")
					}
					for _, field := range []string{"display_name", "handle", "email"} {
						if !strings.Contains(body, `value="`+form.Get(field)+`"`) {
							t.Fatalf("lost %s", field)
						}
					}
				}
				cookies := w.Result().Cookies()
				if want == 303 {
					if w.Header().Get("Location") != "/" || len(cookies) != 1 {
						t.Fatal("missing redirect or cookie")
					}
					c := cookies[0]
					if c.Name != middleware.SessionCookie || c.Value != "owner-session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge <= 0 {
						t.Fatalf("cookie: %+v", c)
					}
				} else if len(cookies) != 0 {
					t.Fatal("unexpected session cookie")
				}
			}
			if f := tt.service; f != nil {
				if f.completed != (f.open && f.openErr == nil) || f.created != (tt.name == "success" || tt.name == "session failure") {
					t.Fatal("unexpected service calls")
				}
				if f.completed && (f.token != "" || f.input != (setup.Input{DisplayName: "Owner", Handle: "owner", Email: "owner@example.com", Password: "secret-password"})) {
					t.Fatal("incorrect signup input")
				}
				if f.created && f.account != (domain.ID{42}) {
					t.Fatal("session created for wrong account")
				}
				if f.created && f.previous != "previous-token" {
					t.Fatalf("the browser's previous session was not replaced: %q", f.previous)
				}
			}
		})
	}
}

func TestSignUpAvailabilityBeforeSession(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		signUp SignUpService // nil: no sign-up service wired
		status int
	}{
		{"not wired", nil, http.StatusNotFound},
		{"closed", fakeSignUp{&fakeSetup{open: false}}, http.StatusNotFound},
		{"open", fakeSignUp{&fakeSetup{open: true}}, http.StatusOK},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(tt.name+" "+method, func(t *testing.T) {
				// Every session lookup fails, as during a database outage.
				resolver := &countingResolver{}
				handler, err := NewHandler("", catalogues, Services{
					Authz:    noOrganisations{},
					Channels: &fakeChannels{},
					Sessions: resolver, SignIn: &fakeSignIn{}, SignUp: tt.signUp, SetupSessions: &fakeSetup{},
				})
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(method, "/signup", strings.NewReader(""))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "some-token"})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				want := tt.status
				if tt.name == "open" && method == http.MethodPost {
					want = http.StatusSeeOther // the fake accepts any form
				}
				if w.Code != want || resolver.calls != 0 {
					t.Fatalf("status %d with %d session lookups; want %d with 0", w.Code, resolver.calls, want)
				}
			})
		}
	}
}
