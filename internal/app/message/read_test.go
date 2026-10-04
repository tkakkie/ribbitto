package message_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

type singleMessage struct {
	t                           *testing.T
	msg                         conversation.Message
	readErr, memberErr, nameErr error
	missingMember, missingName  bool
	calls                       []string
}

func (f *singleMessage) ListMessagesBefore(context.Context, kernel.ID, kernel.ID, *kernel.ID, *int64, int32) ([]conversation.Message, error) {
	f.t.Fatal("unexpected history page read")
	return nil, nil
}

func (f *singleMessage) GetMessage(_ context.Context, org, ch kernel.ID, seq int64) (conversation.Message, error) {
	f.calls = append(f.calls, "message")
	if org != f.msg.OrganizationID || ch != f.msg.ChannelID || seq != f.msg.EventSeq {
		f.t.Fatal("wrong message scope or sequence")
	}
	return f.msg, f.readErr
}

func (f *singleMessage) LookupMembers(_ context.Context, orgID kernel.ID, ids []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	f.calls = append(f.calls, "members")
	if orgID != f.msg.OrganizationID || !reflect.DeepEqual(ids, []kernel.ID{f.msg.MemberID}) {
		f.t.Fatal("wrong author scope")
	}
	if f.missingMember {
		return nil, nil
	}
	return map[kernel.ID]org.DirectoryEntry{f.msg.MemberID: {AccountID: kernel.ID{6}, Handle: "current-handle"}}, f.memberErr
}

func (f *singleMessage) LookupDisplayNames(_ context.Context, ids []kernel.ID) (map[kernel.ID]string, error) {
	f.calls = append(f.calls, "names")
	if f.missingMember {
		if len(ids) != 0 {
			f.t.Fatal("accounts did not come from org lookup")
		}
		return nil, nil
	}
	if !reflect.DeepEqual(ids, []kernel.ID{{6}}) {
		f.t.Fatal("accounts did not come from org lookup")
	}
	if f.missingName {
		return nil, nil
	}
	return map[kernel.ID]string{{6}: "Current Name"}, f.nameErr
}

func (f *singleMessage) LookupTopics(context.Context, kernel.ID, kernel.ID, []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	return map[kernel.ID]conversation.Topic{f.msg.TopicID: {}}, nil
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
		{name: "missing", readErr: conversation.ErrMessageNotFound, wantError: "reading message", wantCalls: []string{"message"}},
		{name: "store error", readErr: failure, wantError: "reading message", wantCalls: []string{"message"}},
		{name: "member error", memberErr: failure, wantError: "reading authors", wantCalls: []string{"message", "members"}},
		{name: "account error", nameErr: failure, wantError: "reading author names", wantCalls: []string{"message", "members", "names"}},
		{name: "missing member", missingMember: true, wantError: "missing author", wantCalls: []string{"message", "members", "names"}},
		{name: "missing account", missingName: true, wantError: "missing author", wantCalls: []string{"message", "members", "names"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := conversation.Message{ID: kernel.ID{4}, OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}, Body: "body", EventSeq: 9}
			f := &singleMessage{t: t, msg: msg, readErr: tt.readErr, memberErr: tt.memberErr, nameErr: tt.nameErr, missingMember: tt.missingMember, missingName: tt.missingName}
			membership := org.Membership{Organization: org.Organization{ID: msg.OrganizationID}}
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

func (singleMessage) GetMessages(context.Context, kernel.ID, kernel.ID, []kernel.ID) ([]conversation.Message, error) {
	return nil, errors.New("unexpected batch read")
}

// partialBatch returns only the messages it holds, as the store omits missing
// or out-of-scope IDs, and fails the test if any directory is read.
type partialBatch struct {
	t     *testing.T
	found []conversation.Message
	calls []string
}

func (f *partialBatch) GetMessages(_ context.Context, orgID, ch kernel.ID, ids []kernel.ID) ([]conversation.Message, error) {
	f.calls = append(f.calls, "messages")
	if orgID != (kernel.ID{1}) || ch != (kernel.ID{2}) || !reflect.DeepEqual(ids, []kernel.ID{{4}, {5}}) {
		f.t.Fatal("wrong batch scope or IDs")
	}
	return f.found, nil
}

func (f *partialBatch) ListMessagesBefore(context.Context, kernel.ID, kernel.ID, *kernel.ID, *int64, int32) ([]conversation.Message, error) {
	f.t.Fatal("unexpected history page read")
	return nil, nil
}

func (f *partialBatch) GetMessage(context.Context, kernel.ID, kernel.ID, int64) (conversation.Message, error) {
	f.t.Fatal("unexpected single message read")
	return conversation.Message{}, nil
}

func (f *partialBatch) LookupMembers(context.Context, kernel.ID, []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	f.t.Fatal("unexpected member lookup")
	return nil, nil
}

func (f *partialBatch) LookupDisplayNames(context.Context, []kernel.ID) (map[kernel.ID]string, error) {
	f.t.Fatal("unexpected display-name lookup")
	return nil, nil
}

func (f *partialBatch) LookupTopics(context.Context, kernel.ID, kernel.ID, []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	f.t.Fatal("unexpected topic lookup")
	return nil, nil
}

// An incomplete batch fails replay rather than rendering part of a move.
func TestManyIncompleteBatch(t *testing.T) {
	f := &partialBatch{t: t, found: []conversation.Message{{ID: kernel.ID{5}, OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}}}}
	membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	got, err := (message.Reader{History: f, Members: f, Accounts: f, Topics: f}).Many(t.Context(), membership, kernel.ID{2}, []kernel.ID{{4}, {5}})
	if !errors.Is(err, conversation.ErrMessageNotFound) || got != nil {
		t.Fatalf("entries = %+v, error = %v; want nil and ErrMessageNotFound", got, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"messages"}) {
		t.Fatalf("calls = %v, want only the batch read", f.calls)
	}
}
