package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type feedLatestReader struct{ fakeMessages }

func (f feedLatestReader) Page(ctx context.Context, m org.Membership, id kernel.ID, topic *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	// Match historyReader.Before: latest PageSize entries, oldest first,
	// retaining Older when another page exists. PageSize changes move this guard.
	if before == nil && len(f.entries) > conversation.PageSize {
		f.entries = f.entries[len(f.entries)-conversation.PageSize:]
		f.older = true
	}
	return f.fakeMessages.Page(ctx, m, id, topic, before)
}

func feedFirstRender(t *testing.T) feedMeasurement {
	t.Helper()
	fixture := feedFixture(t, 120)
	reader := feedLatestReader{}
	for _, msg := range fixture.Messages {
		reader.entries = append(reader.entries, conversation.Entry{Message: conversation.Message{
			ID: msg.ID, MemberID: msg.MemberID, ChannelID: fixture.Current.ID, TopicID: msg.TopicID,
			EventSeq: msg.EventSeq, Body: msg.Body, CreatedAt: msg.CreatedAt,
		}, DisplayName: msg.DisplayName, Handle: msg.Handle, DefaultTopic: true})
	}
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = reader }))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", view.ChannelURL("acme", kernel.ID{1}), nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first render status %d, want 200", w.Code)
	}
	measured := measureFeed(t, w.Body.String())
	if got := countElements(measured.doc, feedMessageItem); got != conversation.PageSize {
		t.Errorf("first render messages: actual %d, want PageSize %d", got, conversation.PageSize)
	}
	return measured
}

func TestFeedGuardFirstRender(t *testing.T) {
	measured := feedFirstRender(t)
	for i, limit := range feedLimits() {
		t.Logf("first render %s: %d (budget %d)", limit.metric, measured.values[i], limit.latest)
		for _, violation := range checkFeedValue(limit.metric, "first render", float64(measured.values[i]), float64(limit.latest), i == 5) {
			t.Error(violation)
		}
	}
}
