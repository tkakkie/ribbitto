package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

type fakeBrancher struct {
	membership org.Membership
	channel    kernel.ID
	got        conversation.Branch
	err        error
}

func (f *fakeBrancher) Branch(_ context.Context, m org.Membership, channel kernel.ID, b conversation.Branch, notice func(conversation.Topic) string) (conversation.Topic, error) {
	f.membership, f.channel, f.got = m, channel, b
	return conversation.Topic{ID: kernel.ID{0x32}}, f.err
}

func TestBranchSelection(t *testing.T) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const source = "31000000-0000-0000-0000-000000000000"
	const selection = "08000000-0000-0000-0000-000000000000/" + source
	path := view.ChannelURL("acme", kernel.ID{1})
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
		{"stale", []string{selection}, conversation.ErrBranchConflict, 409, "topic.branch_conflict"},
		{"name", []string{selection}, conversation.ErrInvalidTopicName, 422, "topic.branch_name_invalid"},
		{"duplicate", []string{selection}, conversation.ErrTopicNameTaken, 422, "topic.branch_name_taken"},
		{"too many", strings.Split(strings.Repeat(selection+",", 100)+selection, ","), conversation.ErrInvalidBranch, 422, "topic.branch_invalid"},
		{"out of scope", []string{selection}, conversation.ErrTopicNotFound, 404, ""},
	} {
		for _, hx := range []bool{false, true} {
			for _, lang := range []string{"en", "ja"} {
				t.Run(tt.name+"/"+lang+"/hx="+map[bool]string{false: "false", true: "true"}[hx], func(t *testing.T) {
					store := &fakeBrancher{err: tt.err}
					handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Branching = store; s.Messages = populatedMessages() }))
					if err != nil {
						t.Fatal(err)
					}
					form := url.Values{"message": tt.selected, "name": {"design"}, "return_topic": {source}, "before": {"9"}}
					if tt.name == "name" {
						form.Set("name", strings.Repeat("x", 81))
					}
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
					if tt.name != "mixed" {
						wantBranch := conversation.Branch{From: kernel.ID{0x31}, Messages: []kernel.ID{{8}}, NewName: form.Get("name")}
						if tt.name == "existing" {
							wantBranch.To = &kernel.ID{0x32}
						}
						if tt.name == "too many" {
							wantBranch.Messages = make([]kernel.ID, len(tt.selected))
							for i := range wantBranch.Messages {
								wantBranch.Messages[i] = kernel.ID{8}
							}
						}
						if !reflect.DeepEqual(store.got, wantBranch) || store.channel != (kernel.ID{1}) || store.membership.Member.Handle != "alice" || store.membership.Organization.Slug != "acme" {
							t.Fatalf("branch call: %+v", store)
						}
					}
					if tt.status == 303 {
						header := "Location"
						if hx {
							header = "HX-Redirect"
						}
						if w.Header().Get(header) != view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{0x32}) || store.got.From != (kernel.ID{0x31}) || len(store.got.Messages) != 1 {
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
					if !hx && (!strings.Contains(body, `id="branch-form"`) || !strings.Contains(body, `value="`+form.Get("name")+`"`) || !strings.Contains(body, `name="return_topic" value="`+source+`"`) || !strings.Contains(body, `name="before" value="9"`)) {
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
