package web

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// TestM2AcceptanceAgainstPostgreSQL proves two people can talk after a reload,
// from first-run setup through sign-up, channel creation and older history.
func TestM2AcceptanceAgainstPostgreSQL(t *testing.T) {
	pool := pgtest.New(t)
	ctx := t.Context()
	sessions := auth.NewSessions(postgres.NewSessionStore(pool), time.Now)
	var logs bytes.Buffer
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	const setupToken = "m2-acceptance-setup-token-0123456789"
	services := postgresServices(t, pool, sessions, setupToken, true)
	// The real limits, as in production: the flow stays within them.
	services.Limits = middleware.NewAuthLimits(nil, time.Now)
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, cookie string, form url.Values, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := serveForm(handler, method, path, cookie, form)
		if w.Code != status {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	redirect := func(w *httptest.ResponseRecorder, want string) {
		t.Helper()
		if got := w.Header().Get("Location"); got != want {
			t.Fatalf("redirect %q, want %q", got, want)
		}
	}
	people := []struct{ name, handle, cookie string }{
		{name: "Alice Owner", handle: "alice"},
		{name: "Bob Member", handle: "bob"},
	}
	var defaultURL string
	for i := range people {
		person := &people[i]
		path := "/signup"
		form := url.Values{"display_name": {person.name}, "handle": {person.handle},
			"email": {person.handle + "@example.com"}, "password": {"acceptance password 123"}}
		if i == 0 {
			path = "/setup"
			form.Set("token", setupToken)
			form.Set("organization_name", "Acceptance Team")
			form.Set("slug", "acceptance")
		}
		request("GET", path, "", nil, http.StatusOK)
		w := request("POST", path, "", form, http.StatusSeeOther)
		redirect(w, "/")
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == middleware.SessionCookie {
				person.cookie = cookie.Value
			}
		}
		if person.cookie == "" {
			t.Fatalf("%s did not issue a session", path)
		}
		redirect(request("GET", "/", person.cookie, nil, http.StatusSeeOther), "/organizations/acceptance/")
		w = request("GET", "/organizations/acceptance/", person.cookie, nil, http.StatusSeeOther)
		if i == 0 {
			defaultURL = w.Header().Get("Location")
			if !strings.HasPrefix(defaultURL, "/organizations/acceptance/channels/") {
				t.Fatalf("default channel redirect: %q", defaultURL)
			}
		}
		redirect(w, defaultURL)
		w = request("GET", defaultURL, person.cookie, nil, http.StatusOK)
		if !strings.Contains(w.Body.String(), "general") {
			t.Fatal("default channel missing")
		}
	}
	created := request("POST", "/organizations/acceptance/channels", people[1].cookie,
		url.Values{"name": {"Project discussion"}}, http.StatusSeeOther)
	createdURL := created.Header().Get("Location")
	if createdURL == defaultURL || !strings.HasPrefix(createdURL, "/organizations/acceptance/channels/") {
		t.Fatalf("new channel redirect: %q", createdURL)
	}

	secrets := []string{"Acceptance Team", "general", "Project discussion"}
	var historyURLs []string
	for _, conversation := range []struct{ path, name string }{
		{defaultURL, "general"}, {createdURL, "Project discussion"},
	} {
		for _, person := range people {
			w := request("GET", conversation.path, person.cookie, nil, http.StatusOK)
			doc, err := html.Parse(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			if heading := find(doc, atom.H1); heading == nil || text(heading) != conversation.name {
				t.Fatalf("%s did not open %s", person.handle, conversation.name)
			}
		}
		bodies := make([]string, message.PageSize+2)
		for i := range bodies {
			bodies[i] = fmt.Sprintf("%s message %03d", conversation.name, i)
			person := people[i%len(people)]
			w := request("POST", conversation.path, person.cookie, url.Values{"body": {bodies[i]}}, http.StatusSeeOther)
			redirect(w, conversation.path)
		}
		secrets = append(secrets, bodies...)
		for _, person := range people {
			// Fresh GETs use only the session cookie, never the POST response.
			path := conversation.path
			for _, bounds := range [][2]int{{2, len(bodies)}, {0, 2}} {
				w := request("GET", path, person.cookie, nil, http.StatusOK)
				doc, err := html.Parse(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				var items []*html.Node
				var older string
				for n := range doc.Descendants() {
					if n.DataAtom == atom.Li && attr(n.Parent, "id") == "message-items" {
						items = append(items, n)
					}
					if attr(n, "id") == "load-older" {
						if link := find(n, atom.A); link != nil {
							older = attr(link, "href")
						}
					}
				}
				if len(items) != bounds[1]-bounds[0] {
					t.Fatalf("%s: got %d messages, want %d", path, len(items), bounds[1]-bounds[0])
				}
				for j, item := range items {
					i := bounds[0] + j
					author := people[i%len(people)]
					body, name := find(item, atom.P), find(item, atom.Span)
					if body == nil || text(body) != bodies[i] || name == nil || text(name) != author.name+" @"+author.handle {
						t.Fatalf("%s reading %s: message %d body/author mismatch: %s", person.handle, path, i, text(item))
					}
				}
				if bounds[0] == 0 {
					if older != "" {
						t.Fatalf("oldest page still links to %q", older)
					}
				} else {
					if !strings.HasPrefix(older, conversation.path+"?before=") {
						t.Fatalf("missing older history link: %q", older)
					}
					historyURLs = append(historyURLs, older)
					path = older
				}
			}
		}
	}

	// These accounts cannot be created by public sign-up, which always joins
	// the setup organisation. Only the adversarial fixtures use direct SQL.
	outsiders := []struct{ name, cookie string }{{name: "signed out"}}
	for _, foreign := range []bool{false, true} {
		var account domain.ID
		if err := pool.QueryRow(ctx, `INSERT INTO account (email, display_name, password_hash)
			VALUES ($1, 'Outsider', '$argon2id$x') RETURNING id`, fmt.Sprintf("outsider-%t@example.com", foreign)).Scan(&account); err != nil {
			t.Fatal(err)
		}
		name := "account without a membership"
		if foreign {
			name = "member of another organisation"
			if _, err := pool.Exec(ctx, `WITH other AS (
				INSERT INTO organization (slug, name) VALUES ('other', 'Other Team') RETURNING id
			) INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle)
				SELECT id, $1, 'member', 1, 'outsider' FROM other`, account); err != nil {
				t.Fatal(err)
			}
		}
		token, _, err := sessions.Create(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		outsiders = append(outsiders, struct{ name, cookie string }{name, token})
	}
	routes := orgRoutes(&pageRenderer{}, services.Channels, services.Messages, services.Posting)
	if len(routes) == 0 {
		t.Fatal("no organisation routes")
	}
	for _, route := range routes {
		seen := make(map[string]bool)
		for _, channelURL := range append([]string{defaultURL, createdURL}, historyURLs...) {
			path := "/organizations/acceptance" + strings.ReplaceAll(route.path, "{$}", "")
			path = strings.ReplaceAll(path, "{channelID}", strings.TrimPrefix(channelURL, "/organizations/acceptance/channels/"))
			if strings.ContainsAny(path, "{}") {
				t.Fatalf("route needs a concrete acceptance fixture: %s", path)
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			for _, outsider := range outsiders {
				t.Run(route.method+" "+path+" "+outsider.name, func(t *testing.T) {
					w := serveForm(handler, route.method, path, outsider.cookie,
						url.Values{"name": {"Forbidden channel"}, "body": {"Forbidden message"}})
					if w.Code != http.StatusNotFound {
						t.Fatalf("status %d, want 404: %s", w.Code, w.Body.String())
					}
					for _, secret := range secrets {
						if strings.Contains(w.Body.String(), secret) {
							t.Errorf("response disclosed %q", secret)
						}
					}
				})
			}
		}
	}
	if logs.Len() != 0 {
		t.Errorf("unexpected message fallback: %s", logs.String())
	}
}
