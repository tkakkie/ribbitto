package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

type fakeBranchWriter struct {
	fakePostingWriter
	got conversation.Branch
}

func (s *fakeBranchWriter) GetTopic(_ context.Context, _, _, id kernel.ID) (conversation.Topic, error) {
	if errors.Is(s.err, conversation.ErrTopicNotFound) {
		return conversation.Topic{}, s.err
	}
	return conversation.Topic{ID: id}, nil
}
func (s *fakeBranchWriter) CreateTopic(_ context.Context, _, _ kernel.ID, name string) (conversation.Topic, error) {
	if errors.Is(s.err, conversation.ErrBranchConflict) {
		return conversation.Topic{ID: kernel.ID{0x32}, Name: name}, nil
	}
	return conversation.Topic{ID: kernel.ID{0x32}, Name: name}, s.err
}
func (s *fakeBranchWriter) MoveMessages(_ context.Context, _, _, from, to kernel.ID, ids []kernel.ID) (int64, error) {
	s.got = conversation.Branch{From: from, To: &to, Messages: ids}
	if errors.Is(s.err, conversation.ErrBranchConflict) {
		return 0, nil
	}
	return int64(len(ids)), s.err
}
func (s *fakeBranchWriter) InsertNotice(context.Context, kernel.ID, kernel.ID, kernel.ID, kernel.ID, string, int64) (conversation.Message, error) {
	return conversation.Message{}, nil
}
func testBrancher(writer conversation.Writer) *conversation.Brancher {
	return conversation.NewBrancher(fakeTxRunner{},
		func(platform.Tx) conversation.Writer { return writer },
		func(platform.Tx) conversation.EventSequence { return fakeEventSequence{} },
		func(platform.Tx) conversation.EventAppender { return fakeEventAppender{} }, nil)
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
		{"stale", []string{selection}, conversation.ErrBranchConflict, 409, "topic.branch_conflict"},
		{"name", []string{selection}, nil, 422, "topic.branch_name_invalid"},
		{"duplicate", []string{selection}, conversation.ErrTopicNameTaken, 422, "topic.branch_name_taken"},
		{"too many", strings.Split(strings.Repeat(selection+",", 100)+selection, ","), nil, 422, "topic.branch_invalid"},
		{"out of scope", []string{selection}, conversation.ErrTopicNotFound, 404, ""},
	} {
		for _, hx := range []bool{false, true} {
			for _, lang := range []string{"en", "ja"} {
				t.Run(tt.name+"/"+lang+"/hx="+map[bool]string{false: "false", true: "true"}[hx], func(t *testing.T) {
					store := &fakeBranchWriter{fakePostingWriter: fakePostingWriter{err: tt.err}}
					handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Branching = testBrancher(store); s.Messages = populatedMessages() }))
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
