package web

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestViewQuote(t *testing.T) {
	quotingMessageID := kernel.ID{1}
	organizationID := kernel.ID{2}
	channelID := kernel.ID{3}
	topicID := kernel.ID{4}
	quotingMemberID := kernel.ID{5}
	quotedMemberID := kernel.ID{6}
	quotedMessageID := kernel.ID{7}

	baseEntry := func() conversation.Entry {
		id := quotedMessageID
		return conversation.Entry{
			Message: conversation.Message{
				ID:             quotingMessageID,
				OrganizationID: organizationID,
				ChannelID:      channelID,
				TopicID:        topicID,
				MemberID:       quotingMemberID,
				Body:           "Quoting message body",
				Quote: &conversation.Quote{
					MessageID: &id,
					MemberID:  quotedMemberID,
					Text:      "Quoted excerpt",
				},
			},
			DisplayName: "Alice Example",
			Handle:      "alice",
			TopicName:   "general",
			QuoteAuthor: conversation.Reference{
				DisplayName: "Bob Example",
				Handle:      "bob",
			},
		}
	}

	const verbatimText = "  first line\n<b>**literal**</b> & <@01000000-0000-0000-0000-000000000000>  "

	for _, tc := range []struct {
		name   string
		slug   string
		mutate func(*conversation.Entry)
		check  func(*testing.T, *view.Quote)
	}{
		{
			name: "nil quote",
			slug: "acme",
			mutate: func(e *conversation.Entry) {
				e.Quote = nil
			},
			check: func(t *testing.T, got *view.Quote) {
				if got != nil {
					t.Fatalf("viewQuote = %+v, want nil", got)
				}
			},
		},
		{
			name:   "url from slug and message ID",
			slug:   "acme",
			mutate: func(*conversation.Entry) {},
			check: func(t *testing.T, got *view.Quote) {
				want := view.MessageURL("acme", quotedMessageID)
				if got == nil || got.URL != want {
					t.Fatalf("viewQuote URL = %v, want %q", got, want)
				}
			},
		},
		{
			name: "deleted original leaves no URL",
			slug: "acme",
			mutate: func(e *conversation.Entry) {
				e.Quote.MessageID = nil
			},
			check: func(t *testing.T, got *view.Quote) {
				if got == nil || got.URL != "" {
					t.Fatalf("viewQuote URL = %v, want empty string", got)
				}
			},
		},
		{
			name: "blank-looking display name becomes empty",
			slug: "acme",
			mutate: func(e *conversation.Entry) {
				e.QuoteAuthor.DisplayName = "\u3164"
			},
			check: func(t *testing.T, got *view.Quote) {
				if got == nil || got.DisplayName != "" || got.Handle != "bob" {
					t.Fatalf("viewQuote author = %+v, want empty DisplayName and handle \"bob\"", got)
				}
			},
		},
		{
			name:   "display name and handle",
			slug:   "acme",
			mutate: func(*conversation.Entry) {},
			check: func(t *testing.T, got *view.Quote) {
				if got == nil || got.DisplayName != "Bob Example" || got.Handle != "bob" {
					t.Fatalf("viewQuote author = %+v, want DisplayName \"Bob Example\" and Handle \"bob\"", got)
				}
			},
		},
		{
			name: "text copied verbatim",
			slug: "acme",
			mutate: func(e *conversation.Entry) {
				e.Quote.Text = verbatimText
			},
			check: func(t *testing.T, got *view.Quote) {
				if got == nil || got.Text != verbatimText {
					t.Fatalf("viewQuote Text = %v, want %q", got, verbatimText)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := baseEntry()
			tc.mutate(&entry)
			tc.check(t, viewQuote(tc.slug, entry))
		})
	}
}
