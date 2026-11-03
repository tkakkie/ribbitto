package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type actionsMessages struct {
	fakeMessages
	entry   conversation.Entry
	manyErr error
}

func (f actionsMessages) Many(_ context.Context, m org.Membership, channel kernel.ID, ids []kernel.ID) ([]conversation.Entry, error) {
	if m.Organization.Slug != "acme" || channel != f.entry.ChannelID || len(ids) != 1 || ids[0] != f.entry.ID {
		return nil, conversation.ErrMessageNotFound
	}
	return []conversation.Entry{f.entry}, f.manyErr
}

func actionsFixture() actionsMessages {
	entry := populatedMessages().entries[0]
	return actionsMessages{fakeMessages: fakeMessages{located: &conversation.Location{ChannelID: entry.ChannelID, TopicID: entry.TopicID}}, entry: entry}
}

func TestMessageActionsPage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*actionsMessages)
		path   string
		status int
	}{
		{"visible", func(*actionsMessages) {}, "", 200},
		{"invalid ID", func(*actionsMessages) {}, "/organizations/acme/messages/invalid/actions", 404},
		{"not found", func(f *actionsMessages) { f.located = nil }, "", 404},
		{"locate failed", func(f *actionsMessages) { f.err = errors.New("unavailable") }, "", 500},
		{"moved after locate", func(f *actionsMessages) { f.manyErr = conversation.ErrMessageNotFound }, "", 404},
		{"channel removed", func(f *actionsMessages) { f.manyErr = conversation.ErrChannelNotFound }, "", 404},
		{"scope denied", func(f *actionsMessages) { f.manyErr = org.ErrNotFound }, "", 404},
		{"read failed", func(f *actionsMessages) { f.manyErr = errors.New("unavailable") }, "", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := actionsFixture()
			tc.change(&fixture)
			handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Messages = fixture })
			path := tc.path
			if path == "" {
				path = view.MessageURL("acme", fixture.entry.ID) + "/actions"
			}
			w := serveForm(handler, http.MethodGet, path, "live", nil)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if tc.status != 200 && (strings.Contains(w.Body.String(), "<form") || strings.Contains(w.Body.String(), "author")) {
				t.Fatal("failed actions read exposed message actions")
			}
		})
	}
}
