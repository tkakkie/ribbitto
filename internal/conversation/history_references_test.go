package conversation_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// referencedMessage is a chat_v1 message naming a member of its organisation
// and one the organisation's directory omits; the account directory would
// name anyone it is asked about, even the zero account.
type referencedMessage struct {
	*singleMessage
	local       kernel.ID
	memberCalls [][]kernel.ID
	nameCalls   [][]kernel.ID
}

func (f *referencedMessage) LookupMembers(_ context.Context, orgID kernel.ID, ids []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	if orgID != f.msg.OrganizationID {
		f.t.Fatal("references looked up outside the organisation")
	}
	f.memberCalls = append(f.memberCalls, slices.Clone(ids))
	return map[kernel.ID]org.DirectoryEntry{f.msg.MemberID: {AccountID: kernel.ID{6}, Handle: "author"}, f.local: {AccountID: kernel.ID{7}, Handle: "bob"}}, nil
}

func (f *referencedMessage) LookupDisplayNames(_ context.Context, ids []kernel.ID) (map[kernel.ID]string, error) {
	f.nameCalls = append(f.nameCalls, slices.Clone(ids))
	return map[kernel.ID]string{{6}: "Author", {7}: "Bob", {8}: "Foreign", {}: "Nobody"}, nil
}

// References join the author's member and name batches, one each per
// render, and resolve only to members the organisation's directory returns.
func TestEntryResolvesReferencesInOneScopedBatch(t *testing.T) {
	local, foreign := kernel.ID{0x11}, kernel.ID{0x22}
	msg := conversation.Message{ID: kernel.ID{4}, OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}, EventSeq: 9, Format: conversation.FormatChatV1,
		Body: "hi <@" + realtime.FormatPayloadID(local) + "> and **<@" + realtime.FormatPayloadID(foreign) + ">**"}
	f := &referencedMessage{singleMessage: &singleMessage{t: t, msg: msg}, local: local}
	reader, _ := testReader(t, f)
	got, err := reader.One(t.Context(), org.Membership{Organization: org.Organization{ID: msg.OrganizationID}}, msg.ChannelID, msg.EventSeq)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.memberCalls, [][]kernel.ID{{msg.MemberID, local, foreign}}) {
		t.Fatalf("member lookups = %v, want one batch of the author and both references", f.memberCalls)
	}
	if len(f.nameCalls) != 1 || len(f.nameCalls[0]) != 2 || !slices.Contains(f.nameCalls[0], kernel.ID{6}) || !slices.Contains(f.nameCalls[0], kernel.ID{7}) {
		t.Fatalf("name lookups = %v, want one batch of the accounts org returned", f.nameCalls)
	}
	if got.Blocks == nil || !reflect.DeepEqual(got.References, map[kernel.ID]conversation.Reference{local: {DisplayName: "Bob", Handle: "bob"}}) {
		t.Fatalf("blocks %v, references %v; want the parsed body and only the local member", got.Blocks, got.References)
	}
}
