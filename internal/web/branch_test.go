package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

type fakeBranchStore struct {
	err error
	got topic.Branch
}

func (s *fakeBranchStore) Branch(_ context.Context, _, _, _ domain.ID, b topic.Branch, _ func(domain.Topic) string) (domain.Topic, int64, error) {
	s.got = b
	return domain.Topic{ID: domain.ID{0x32}}, 1, s.err
}

func TestBranchSelection(t *testing.T) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const source = "31000000-0000-0000-0000-000000000000"
	const selection = "08000000-0000-0000-0000-000000000000/" + source
	path := view.ChannelURL("acme", domain.ID{1})
	for _, tt := range []struct {
		name     string
		selected []string
		err      error
		status   int
		key      string
	}{
		{"new", []string{selection}, nil, 303, ""},
		{"existing", []string{selection}, nil, 303, ""},
		{"mixed", []string{selection, strings.Replace(selection, source, "32000000-0000-0000-0000-000000000000", 1)}, nil, 422, "topic.branch_mixed"},
		{"stale", []string{selection}, topic.ErrConflict, 409, "topic.branch_conflict"},
		{"name", []string{selection}, topic.ErrInvalidName, 422, "topic.branch_name_invalid"},
		{"duplicate", []string{selection}, topic.ErrNameTaken, 422, "topic.branch_name_taken"},
		{"too many", strings.Split(strings.Repeat(selection+",", 100)+selection, ","), nil, 422, "topic.branch_invalid"},
		{"out of scope", []string{selection}, topic.ErrNotFound, 404, ""},
	} {
		for _, hx := range []bool{false, true} {
			for _, lang := range []string{"en", "ja"} {
				t.Run(tt.name+"/"+lang+"/hx="+map[bool]string{false: "false", true: "true"}[hx], func(t *testing.T) {
					store := &fakeBranchStore{err: tt.err}
					handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Branching = topic.NewBrancher(store, nil); s.Messages = populatedMessages() }))
					if err != nil {
						t.Fatal(err)
					}
					form := url.Values{"message": tt.selected, "name": {"design"}, "return_topic": {source}, "before": {"9"}}
					if tt.name == "existing" {
						form.Set("name", "")
						form.Set("to", "32000000-0000-0000-0000-000000000000")
					}
					r := httptest.NewRequest("POST", path+"/branch", strings.NewReader(form.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.Header.Set("Accept-Language", lang)
					if hx {
						r.Header.Set("HX-Request", "true")
					}
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					status := tt.status
					if hx && status == 303 {
						status = 200
					}
					if w.Code != status {
						t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body)
					}
					if tt.status == 303 {
						header := "Location"
						if hx {
							header = "HX-Redirect"
						}
						if w.Header().Get(header) != view.ConversationURL("acme", domain.ID{1}, &domain.ID{0x32}) || store.got.From != (domain.ID{0x31}) || len(store.got.Messages) != 1 {
							t.Fatal("lost destination or expected source selection")
						}
					}
					if tt.key == "" {
						return
					}
					body := w.Body.String()
					catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
						if !strings.Contains(body, html.EscapeString(i18n.T(r.Context(), tt.key))) {
							t.Fatal("missing localized error")
						}
					})).ServeHTTP(httptest.NewRecorder(), r)
					doc, err := html.Parse(strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					for _, problem := range checkMarkup(doc, !hx) {
						t.Error(problem)
					}
					if hx && strings.Contains(body, "message-composer") {
						t.Fatal("enhanced error replaced conversation")
					}
					if !hx && (!strings.Contains(body, `id="branch-form"`) || !strings.Contains(body, `value="design"`) || !strings.Contains(body, `name="return_topic" value="`+source+`"`) || !strings.Contains(body, `name="before" value="9"`)) {
						t.Fatal("plain error lost usable form or return context")
					}
					// checkMarkup resolves the reference; each checkbox names its message.
					if !hx && !strings.Contains(body, `aria-describedby="body-message-`) {
						t.Fatal("branch checkboxes are not described by their messages")
					}
				})
			}
		}
	}
}
