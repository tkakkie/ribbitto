package message_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
)

type store struct {
	err                  error
	calls                int
	org, channel, member domain.ID
	body                 string
}

func (s *store) Post(_ context.Context, org, ch, member domain.ID, body string) (domain.Message, error) {
	s.calls++
	s.org, s.channel, s.member, s.body = org, ch, member, body
	return domain.Message{OrganizationID: org, ChannelID: ch, MemberID: member, Body: body, EventSeq: 7}, s.err
}

func TestPost(t *testing.T) {
	m := authz.Membership{Organization: domain.Organization{ID: domain.ID{1}}, Member: domain.Member{ID: domain.ID{2}, OrganizationID: domain.ID{1}}}
	for _, tt := range []struct {
		name, body, want  string
		storeErr, wantErr error
	}{
		{name: "normalised", body: "  hello\r\nworld\t", want: "hello\nworld"},
		{name: "empty", body: " \n ", wantErr: message.ErrInvalidBody},
		{name: "too long", body: strings.Repeat("界", 4001), wantErr: message.ErrInvalidBody},
		{name: "control character", body: "a\x00b", wantErr: message.ErrInvalidBody},
		{name: "channel outside the organisation", body: "hi", storeErr: channel.ErrNotFound, wantErr: channel.ErrNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &store{err: tt.storeErr}
			got, err := message.New(s).Post(t.Context(), m, domain.ID{3}, tt.body)
			if !errors.Is(err, tt.wantErr) || err == nil && got.Body != tt.want {
				t.Fatalf("got %+v, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
			// An invalid body never reaches the store (and takes no sequence);
			// the organisation and author always come from the membership.
			reached := !errors.Is(tt.wantErr, message.ErrInvalidBody)
			if reached != (s.calls == 1) || reached && (s.org != m.Organization.ID || s.member != m.Member.ID || s.channel != (domain.ID{3})) {
				t.Fatalf("store: %+v", s)
			}
		})
	}
}
