package message_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

type store struct {
	err                  error
	calls                int
	org, channel, member domain.ID
	topicID              *domain.ID
	body                 string
	returned             bool
}

func (s *store) PostToTopic(_ context.Context, org, ch, member domain.ID, topicID *domain.ID, body string) (domain.Message, error) {
	defer func() { s.returned = true }()
	s.calls++
	s.topicID = topicID
	s.org, s.channel, s.member, s.body = org, ch, member, body
	return domain.Message{OrganizationID: org, ChannelID: ch, MemberID: member, Body: body, EventSeq: 7}, s.err
}

type notifierFunc func(domain.ID, int64)

func (n notifierFunc) Raise(org domain.ID, seq int64) { n(org, seq) }

func TestPostNotification(t *testing.T) {
	storeErr := errors.New("commit failed")
	m := org.Membership{Organization: org.Organization{ID: domain.ID{1}}, Member: org.Member{ID: domain.ID{2}}}
	for _, tt := range []struct {
		name, body        string
		storeErr, wantErr error
		wantCalls         int
	}{
		{name: "committed", body: "hello", wantCalls: 1},
		{name: "invalid body", body: " ", wantErr: message.ErrInvalidBody},
		{name: "store error", body: "hello", storeErr: storeErr, wantErr: storeErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &store{err: tt.storeErr}
			calls := 0
			n := notifierFunc(func(org domain.ID, seq int64) {
				calls++
				if !s.returned || s.calls != 1 || org != m.Organization.ID || seq != 7 {
					t.Fatalf("notification (%v, %d), store: %+v", org, seq, s)
				}
			})
			_, err := message.NewWithNotifier(s, n).Post(t.Context(), m, domain.ID{3}, tt.body)
			if !errors.Is(err, tt.wantErr) || calls != tt.wantCalls {
				t.Fatalf("Post: %v, notifications: %d; want %v, %d", err, calls, tt.wantErr, tt.wantCalls)
			}
		})
	}
}

func TestPost(t *testing.T) {
	m := org.Membership{Organization: org.Organization{ID: domain.ID{1}}, Member: org.Member{ID: domain.ID{2}, OrganizationID: domain.ID{1}}}
	for _, tt := range []struct {
		name, body, want  string
		storeErr, wantErr error
	}{
		{name: "normalised", body: "  hello\r\nworld\t", want: "hello\nworld"},
		{name: "empty", body: " \n ", wantErr: message.ErrInvalidBody},
		{name: "too long", body: strings.Repeat("界", 4001), wantErr: message.ErrInvalidBody},
		{name: "control character", body: "a\x00b", wantErr: message.ErrInvalidBody},
		{name: "channel outside the organisation", body: "hi", storeErr: conversation.ErrChannelNotFound, wantErr: conversation.ErrChannelNotFound},
	} {
		for _, topicID := range []*domain.ID{nil, {4}} {
			t.Run(tt.name, func(t *testing.T) {
				s := &store{err: tt.storeErr}
				var got domain.Message
				var err error
				if topicID == nil {
					got, err = message.New(s).Post(t.Context(), m, domain.ID{3}, tt.body)
				} else {
					got, err = message.New(s).PostToTopic(t.Context(), m, domain.ID{3}, topicID, tt.body)
				}
				if !errors.Is(err, tt.wantErr) || err == nil && got.Body != tt.want {
					t.Fatalf("got %+v, %v; want %q, %v", got, err, tt.want, tt.wantErr)
				}
				// An invalid body never reaches the store (and takes no sequence);
				// the organisation and author always come from the membership.
				reached := !errors.Is(tt.wantErr, message.ErrInvalidBody)
				if reached != (s.calls == 1) || reached && (s.org != m.Organization.ID || s.member != m.Member.ID || s.channel != (domain.ID{3}) || s.topicID != topicID) {
					t.Fatalf("store: %+v", s)
				}
			})
		}
	}
}
