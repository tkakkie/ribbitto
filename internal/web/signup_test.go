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

	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
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
	for _, tt := range []registrationCase{
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
		{"busy", &fakeSetup{open: true, err: fmt.Errorf("wrapped: %w", identity.ErrBusy)}, 503, ""},
		{"availability failure", &fakeSetup{openErr: errors.New("private detail")}, 500, ""},
		{"completion failure", &fakeSetup{open: true, err: errors.New("private detail")}, 500, ""},
		{"session failure", &fakeSetup{open: true, sessionErr: errors.New("private detail")}, 500, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			services := testServices(func(s *Services) { s.SetupSessions, s.SignIn = tt.service, &fakeSignIn{err: identity.ErrInvalidInput} })
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
			checkRegistration(t, handler, tt, "/signup", form,
				[]string{"display_name", "handle", "email"}, "",
				setup.Input{DisplayName: "Owner", Handle: "owner", Email: "owner@example.com", Password: "secret-password"})
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
				handler, err := NewHandler("", catalogues, testServices(func(s *Services) {
					s.Sessions, s.SignUp, s.SetupSessions = resolver, tt.signUp, &fakeSetup{}
				}))
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
