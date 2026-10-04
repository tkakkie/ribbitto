package message_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

type singleMessage struct {
	t                           *testing.T
	msg                         domain.Message
	readErr, memberErr, nameErr error
	missingMember, missingName  bool
	calls                       []string
}

func (f *singleMessage) ListMessagesBefore(context.Context, domain.ID, domain.ID, *domain.ID, *int64, int32) ([]domain.Message, error) {
	f.t.Fatal("unexpected history page read")
	return nil, nil
}

func (f *singleMessage) GetMessage(_ context.Context, org, ch domain.ID, seq int64) (domain.Message, error) {
	f.calls = append(f.calls, "message")
	if org != f.msg.OrganizationID || ch != f.msg.ChannelID || seq != f.msg.EventSeq {
		f.t.Fatal("wrong message scope or sequence")
	}
	return f.msg, f.readErr
}

func (f *singleMessage) LookupMembers(_ context.Context, org domain.ID, ids []domain.ID) (map[domain.ID]member.Identity, error) {
	f.calls = append(f.calls, "members")
	if org != f.msg.OrganizationID || !reflect.DeepEqual(ids, []domain.ID{f.msg.MemberID}) {
		f.t.Fatal("wrong author scope")
	}
	if f.missingMember {
		return nil, nil
	}
	return map[domain.ID]member.Identity{f.msg.MemberID: {AccountID: domain.ID{6}, Handle: "current-handle"}}, f.memberErr
}

func (f *singleMessage) LookupDisplayNames(_ context.Context, ids []domain.ID) (map[domain.ID]string, error) {
	f.calls = append(f.calls, "names")
	if f.missingMember {
		if len(ids) != 0 {
			f.t.Fatal("accounts did not come from org lookup")
		}
		return nil, nil
	}
	if !reflect.DeepEqual(ids, []domain.ID{{6}}) {
		f.t.Fatal("accounts did not come from org lookup")
	}
	if f.missingName {
		return nil, nil
	}
	return map[domain.ID]string{{6}: "Current Name"}, f.nameErr
}

func (f *singleMessage) LookupTopics(context.Context, domain.ID, domain.ID, []domain.ID) (map[domain.ID]domain.Topic, error) {
	return map[domain.ID]domain.Topic{f.msg.TopicID: {}}, nil
}

func TestOne(t *testing.T) {
	failure := errors.New("directory or store failed")
	for _, tt := range []struct {
		name, wantError             string
		readErr, memberErr, nameErr error
		missingMember, missingName  bool
		wantCalls                   []string
	}{
		{name: "hydrated", wantCalls: []string{"message", "members", "names"}},
		{name: "missing", readErr: message.ErrNotFound, wantError: "reading message", wantCalls: []string{"message"}},
		{name: "store error", readErr: failure, wantError: "reading message", wantCalls: []string{"message"}},
		{name: "member error", memberErr: failure, wantError: "reading authors", wantCalls: []string{"message", "members"}},
		{name: "account error", nameErr: failure, wantError: "reading author names", wantCalls: []string{"message", "members", "names"}},
		{name: "missing member", missingMember: true, wantError: "missing author", wantCalls: []string{"message", "members", "names"}},
		{name: "missing account", missingName: true, wantError: "missing author", wantCalls: []string{"message", "members", "names"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := domain.Message{ID: domain.ID{4}, OrganizationID: domain.ID{1}, ChannelID: domain.ID{2}, MemberID: domain.ID{3}, Body: "body", EventSeq: 9}
			f := &singleMessage{t: t, msg: msg, readErr: tt.readErr, memberErr: tt.memberErr, nameErr: tt.nameErr, missingMember: tt.missingMember, missingName: tt.missingName}
			membership := org.Membership{Organization: domain.Organization{ID: msg.OrganizationID}}
			got, err := (message.Reader{History: f, Members: f, Accounts: f, Topics: f}).One(t.Context(), membership, msg.ChannelID, msg.EventSeq)
			if !reflect.DeepEqual(f.calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", f.calls, tt.wantCalls)
			}
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || got != (message.Entry{}) {
					t.Fatalf("entry = %+v, error = %v; want zero entry and %q", got, err, tt.wantError)
				}
				for _, cause := range []error{tt.readErr, tt.memberErr, tt.nameErr} {
					if cause != nil && !errors.Is(err, cause) {
						t.Fatalf("error %v does not wrap %v", err, cause)
					}
				}
				return
			}
			if err != nil || got != (message.Entry{Message: msg, DisplayName: "Current Name", Handle: "current-handle"}) {
				t.Fatalf("entry = %+v, error = %v", got, err)
			}
		})
	}
}

func (singleMessage) GetMessages(context.Context, domain.ID, domain.ID, []domain.ID) ([]domain.Message, error) {
	return nil, errors.New("unexpected batch read")
}
