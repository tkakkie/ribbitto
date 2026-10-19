package conversation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type quoteRetryStore struct {
	*keyedStore
	original conversation.Message
}

func (s *quoteRetryStore) QuotedMessage(_ context.Context, organization, channel, id kernel.ID) (conversation.Message, error) {
	if s.original.OrganizationID != organization || s.original.ChannelID != channel || s.original.ID != id {
		return conversation.Message{}, conversation.ErrQuoteNotFound
	}
	return s.original, nil
}

type quoteRetryReads struct {
	conversation.ReadStore
	store *quoteRetryStore
}

func (r quoteRetryReads) GetMessages(ctx context.Context, organization, channel kernel.ID, ids []kernel.ID) ([]conversation.Message, error) {
	original, err := r.store.QuotedMessage(ctx, organization, channel, ids[0])
	if errors.Is(err, conversation.ErrQuoteNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []conversation.Message{original}, nil
}

func TestPostingQuoteRetry(t *testing.T) {
	for _, body := range []string{"reply", "reply @unknown"} {
		t.Run(body, func(t *testing.T) {
			for _, tc := range []struct {
				name        string
				first, next *string
				foreign     bool
				want        error
			}{
				{name: "whole to whole"},
				{name: "whole to part", next: new("whole"), want: conversation.ErrPostKeyReused},
				{name: "part to whole", first: new("whole"), want: conversation.ErrPostKeyReused},
				{name: "whole to equal part", next: new("whole text")},
				{name: "equal part to whole", first: new("whole text")},
				{name: "normalized same part", first: new("whole"), next: new(" whole\r\n")},
				{name: "changed part", first: new("whole"), next: new("text"), want: conversation.ErrPostKeyReused},
				{name: "scope before comparison", foreign: true, next: new("absent"), want: conversation.ErrQuoteNotFound},
				{name: "substring before comparison", next: new("absent"), want: conversation.ErrInvalidBody},
				{name: "empty before comparison", next: new(""), want: conversation.ErrInvalidBody},
			} {
				t.Run(tc.name, func(t *testing.T) {
					m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
					channel := kernel.ID{3}
					s := &quoteRetryStore{keyedStore: &keyedStore{state: keyedState{seq: map[kernel.ID]int64{}, keyed: map[keyScope]conversation.Message{}}}, original: conversation.Message{ID: kernel.ID{4}, OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID, Format: conversation.FormatChatV1, Body: "**whole** text"}}
					d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{}}
					p := conversation.NewPagePosting(s,
						func(platform.Tx) conversation.Writer { return s },
						func(platform.Tx) conversation.EventSequence { return s },
						func(platform.Tx) conversation.EventAppender { return s }, nil,
						d, func(platform.Snapshot) conversation.MemberDirectory { return d },
						func(platform.Snapshot) conversation.ReadStore { return quoteRetryReads{store: s} }, nil)
					key := conversation.NewIdempotencyKey()
					send := conversation.Send{Body: body, Key: &key, Quote: &s.original.ID, QuoteText: tc.first}
					first, err := p.Send(t.Context(), m, channel, send)
					if err != nil {
						t.Fatal(err)
					}
					before := s.state.seq[m.Organization.ID]
					send.QuoteText = tc.next
					if tc.foreign {
						send.Quote = &kernel.ID{9}
					}
					got, err := p.Send(t.Context(), m, channel, send)
					if !errors.Is(err, tc.want) {
						t.Fatalf("retry: %v, want %v", err, tc.want)
					}
					if tc.want == nil || tc.want == conversation.ErrPostKeyReused {
						if !got.Repeat || got.Message.ID != first.Message.ID {
							t.Fatalf("not the stored message: %+v", got)
						}
					}
					if s.state.seq[m.Organization.ID] != before || s.state.events != 1 || len(s.state.keyed) != 1 {
						t.Fatal("retry wrote a message or event")
					}
				})
			}
		})
	}
}
