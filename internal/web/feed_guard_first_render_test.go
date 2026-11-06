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
	page, err := f.fakeMessages.Page(ctx, m, id, topic, before)
	if err != nil {
		return page, err
	}
	if page.Topic != nil {
		page.Topic.ConclusionMessageID = page.Entries[0].ID
		page.Conclusion = &page.Entries[0]
	}
	return page, nil
}

func feedFirstRender(t *testing.T, page feedGuardPage) feedMeasurement {
	t.Helper()
	fixture := page.fixture(t, 120)
	reader := feedLatestReader{}
	for _, msg := range fixture.Messages {
		reader.entries = append(reader.entries, conversation.Entry{Message: conversation.Message{
			ID: msg.ID, MemberID: msg.MemberID, ChannelID: fixture.Current.ID, TopicID: msg.TopicID,
			EventSeq: msg.EventSeq, Body: msg.Body, CreatedAt: msg.CreatedAt,
		}, DisplayName: msg.DisplayName, Handle: msg.Handle, TopicName: msg.TopicName, DefaultTopic: fixture.Topic == nil})
	}
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = reader }))
	if err != nil {
		t.Fatal(err)
	}
	var topicID *kernel.ID
	if fixture.Topic != nil {
		topicID = &fixture.Topic.ID
	}
	req := httptest.NewRequest("GET", view.ConversationURL("acme", fixture.Current.ID, topicID), nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first render status %d, want 200", w.Code)
	}
	return measureFeed(t, w.Body.String())
}

func checkFeedFirstRender(values [6]int, limits []feedLimit) []string {
	var violations []string
	for i, limit := range limits {
		violations = append(violations, checkFeedValue(limit.metric, "first render", float64(values[i]), float64(limit.latest), i == 5)...)
	}
	return violations
}

func TestFeedGuardFirstRender(t *testing.T) {
	for _, page := range feedGuardPages() {
		t.Run(page.name, func(t *testing.T) {
			measured := feedFirstRender(t, page)
			if got := countElements(measured.doc, feedMessageItem); got != conversation.PageSize {
				t.Errorf("first render messages: actual %d, want PageSize %d", got, conversation.PageSize)
			}
			for i, limit := range page.limits {
				t.Logf("first render %s: %d (budget %d)", limit.metric, measured.values[i], limit.latest)
			}
			for _, violation := range checkFeedFirstRender(measured.values, page.limits) {
				t.Error(violation)
			}
		})
	}
}
