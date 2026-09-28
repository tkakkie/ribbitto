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
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

type fakeSetup struct {
	open                     bool
	err, openErr, sessionErr error
	input                    setup.Input
	token                    string
	account                  domain.ID
	completed, created       bool
	previous                 string
}

func (f *fakeSetup) Open(context.Context) (bool, error) { return f.open, f.openErr }
func (f *fakeSetup) Complete(_ context.Context, token string, input setup.Input) (setup.Result, error) {
	f.completed, f.token, f.input = true, token, input
	return setup.Result{AccountID: domain.ID{42}}, f.err
}
func (f *fakeSetup) Replace(_ context.Context, previous string, account domain.ID) (string, time.Time, error) {
	f.created, f.previous, f.account = true, previous, account
	return "owner-session", time.Now().Add(time.Hour), f.sessionErr
}

func TestSetup(t *testing.T) {
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
		{"completed concurrently", &fakeSetup{open: true, err: fmt.Errorf("wrapped: %w", setup.ErrCompleted)}, 404, ""},
		{"wrong token", &fakeSetup{open: true, err: setup.ErrToken}, 422, "The setup token is incorrect."},
		{"organisation", &fakeSetup{open: true, err: setup.ValidationErrors{"organization_name": errors.New("private detail")}}, 422, "Enter an organisation name of 1–100 printable characters."},
		{"slug", &fakeSetup{open: true, err: setup.ValidationErrors{"slug": errors.New("private detail")}}, 422, "Use 1–63 lowercase letters, digits or hyphens, starting and ending with a letter or digit."},
		{"name", &fakeSetup{open: true, err: setup.ValidationErrors{"display_name": errors.New("private detail")}}, 422, "Enter a display name of 1–50 printable characters, at least one of them visible."},
		{"handle", &fakeSetup{open: true, err: setup.ValidationErrors{"handle": errors.New("private detail")}}, 422, "Use 2–32 characters: lowercase letters, digits, _, . or -"},
		{"email", &fakeSetup{open: true, err: setup.ValidationErrors{"email": errors.New("private detail")}}, 422, "Enter a valid email address."},
		{"password", &fakeSetup{open: true, err: setup.ValidationErrors{"password": errors.New("private detail")}}, 422, "Use a password of 15–128 characters."},
		{"busy", &fakeSetup{open: true, err: fmt.Errorf("wrapped: %w", auth.ErrBusy)}, 503, ""},
		{"availability failure", &fakeSetup{openErr: errors.New("private detail")}, 500, ""},
		{"completion failure", &fakeSetup{open: true, err: errors.New("private detail")}, 500, ""},
		{"session failure", &fakeSetup{open: true, sessionErr: errors.New("private detail")}, 500, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			services := Services{Channels: &fakeChannels{}, Authz: noOrganisations{}, Sessions: noSessions{}, SignIn: &fakeSignIn{}, SetupSessions: tt.service}
			if tt.service != nil {
				services.Setup = tt.service
			}
			handler, err := NewHandler("", catalogues, services)
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				r := httptest.NewRequest(method, "/setup", strings.NewReader(form.Encode()))
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
				if want == 200 && !strings.Contains(body, `action="/setup"`) {
					t.Fatal("missing form")
				}
				if want == 422 {
					if !strings.Contains(body, tt.message) || !strings.Contains(body, `role="alert"`) {
						t.Fatal("missing error")
					}
					for _, field := range []string{"organization_name", "slug", "display_name", "handle", "email"} {
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
				if f.completed && (f.token != form.Get("token") || f.input != (setup.Input{OrganizationName: "Example", Slug: "example", DisplayName: "Owner", Handle: "owner", Email: "owner@example.com", Password: "secret-password"})) {
					t.Fatal("incorrect submitted input")
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

func TestSetupAvailabilityBeforeSession(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		setup  SetupService // nil: RIBBITTO_SETUP_TOKEN unset or empty
		status int
	}{
		{"disabled", nil, http.StatusNotFound},
		{"completed", &fakeSetup{open: false}, http.StatusNotFound},
		{"open", &fakeSetup{open: true}, http.StatusOK},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(tt.name+" "+method, func(t *testing.T) {
				// Every session lookup fails, as during a database outage.
				resolver := &countingResolver{}
				services := Services{Sessions: resolver, SignIn: &fakeSignIn{}, Channels: &fakeChannels{}, Authz: noOrganisations{}, Setup: tt.setup}
				if tt.setup != nil {
					services.SetupSessions = tt.setup.(*fakeSetup)
				}
				handler, err := NewHandler("", catalogues, services)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(method, "/setup", strings.NewReader(""))
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
