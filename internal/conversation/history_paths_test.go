package conversation_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

type historyPathsBatch struct {
	partialBatch
	readErr                                          error
	members                                          map[kernel.ID]org.DirectoryEntry
	names                                            map[kernel.ID]string
	topics                                           map[kernel.ID]conversation.Topic
	gotOrgID, gotChannelID                           kernel.ID
	gotMemberOrgID, gotTopicOrgID, gotTopicChannelID kernel.ID
	gotIDs, gotTopicIDs                              []kernel.ID
	memberBatches, accountBatches                    [][]kernel.ID
}

func (f *historyPathsBatch) GetMessages(_ context.Context, organizationID, channelID kernel.ID, ids []kernel.ID) ([]conversation.Message, error) {
	f.calls = append(f.calls, "messages")
	f.gotOrgID, f.gotChannelID, f.gotIDs = organizationID, channelID, slices.Clone(ids)
	return f.found, f.readErr
}

func (f *historyPathsBatch) LookupMembers(ctx context.Context, organizationID kernel.ID, memberIDs []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	if f.readErr != nil {
		return f.partialBatch.LookupMembers(ctx, organizationID, memberIDs)
	}
	f.calls = append(f.calls, "members")
	f.gotMemberOrgID = organizationID
	f.memberBatches = append(f.memberBatches, slices.Clone(memberIDs))
	return f.members, nil
}

func (f *historyPathsBatch) LookupDisplayNames(ctx context.Context, accountIDs []kernel.ID) (map[kernel.ID]string, error) {
	if f.readErr != nil {
		return f.partialBatch.LookupDisplayNames(ctx, accountIDs)
	}
	f.calls = append(f.calls, "names")
	f.accountBatches = append(f.accountBatches, slices.Clone(accountIDs))
	return f.names, nil
}

func (f *historyPathsBatch) LookupTopics(ctx context.Context, organizationID, channelID kernel.ID, topicIDs []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	if f.readErr != nil {
		return f.partialBatch.LookupTopics(ctx, organizationID, channelID, topicIDs)
	}
	f.calls = append(f.calls, "topics")
	f.gotTopicOrgID, f.gotTopicChannelID, f.gotTopicIDs = organizationID, channelID, slices.Clone(topicIDs)
	return f.topics, nil
}

func TestHistoryPathsMany(t *testing.T) {
	orgID, decoyOrgID, callerMemberID := kernel.ID{1}, kernel.ID{98}, kernel.ID{99}
	channelID, firstTopicID, secondTopicID := kernel.ID{2}, kernel.ID{7}, kernel.ID{8}
	firstAuthorID, secondAuthorID, quotedAuthorID := kernel.ID{3}, kernel.ID{11}, kernel.ID{12}
	firstAccountID, secondAccountID, quotedAccountID := kernel.ID{6}, kernel.ID{16}, kernel.ID{17}
	olderMsgID, newerMsgID := kernel.ID{4}, kernel.ID{5}
	// A missing member resolves to the zero DirectoryEntry; naming the zero
	// account ID ensures the missing-author row fails on !ok rather than !named.
	zeroAccountID := kernel.ID{}

	olderMsg := conversation.Message{ID: olderMsgID, OrganizationID: orgID, ChannelID: channelID, TopicID: firstTopicID, MemberID: firstAuthorID, Body: "older", EventSeq: 10}
	newerMsg := conversation.Message{ID: newerMsgID, OrganizationID: orgID, ChannelID: channelID, TopicID: secondTopicID, MemberID: secondAuthorID, Body: "newer", EventSeq: 20, Quote: &conversation.Quote{MemberID: quotedAuthorID, Text: "quoted"}}
	wantEntries := []conversation.Entry{
		{Message: olderMsg, DisplayName: "First Author", Handle: "first", TopicName: "General", DefaultTopic: true},
		{Message: newerMsg, DisplayName: "Second Author", Handle: "second", TopicName: "Design", QuoteAuthor: conversation.Reference{DisplayName: "Quoted Author", Handle: "quoted"}},
	}
	failure := errors.New("batch store failed")
	wantIDs := []kernel.ID{olderMsgID, newerMsgID}
	fullCalls := []string{"messages", "members", "names", "topics"}

	for _, tt := range []struct {
		name                            string
		readErr                         error
		omitMember, omitName, omitTopic kernel.ID
		checkScope, checkQuoteBatch     bool
		wantErr                         error
		wantError                       string
		wantCalls                       []string
	}{
		{name: "complete batch oldest first"},
		{name: "organisation and channel reach GetMessages", checkScope: true},
		{name: "quote authors join member batch", checkQuoteBatch: true},
		{name: "reading messages error is wrapped", readErr: failure, wantErr: failure, wantError: "reading messages", wantCalls: []string{"messages"}},
		{name: "missing author", omitMember: firstAuthorID, wantError: fmt.Sprintf("missing author for message %x", olderMsgID)},
		{name: "missing display name", omitName: firstAccountID, wantError: fmt.Sprintf("missing author for message %x", olderMsgID)},
		{name: "missing topic", omitTopic: firstTopicID, wantError: fmt.Sprintf("missing topic for message %x", olderMsgID)},
		{name: "missing quoted author", omitMember: quotedAuthorID, wantError: fmt.Sprintf("missing quoted author for message %x", newerMsgID)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			members := map[kernel.ID]org.DirectoryEntry{
				firstAuthorID:  {AccountID: firstAccountID, Handle: "first"},
				secondAuthorID: {AccountID: secondAccountID, Handle: "second"},
				quotedAuthorID: {AccountID: quotedAccountID, Handle: "quoted"},
			}
			delete(members, tt.omitMember)
			names := map[kernel.ID]string{
				zeroAccountID:   "Zero",
				firstAccountID:  "First Author",
				secondAccountID: "Second Author",
				quotedAccountID: "Quoted Author",
			}
			delete(names, tt.omitName)
			topics := map[kernel.ID]conversation.Topic{
				firstTopicID:  {ID: firstTopicID, Name: "General", IsDefault: true},
				secondTopicID: {ID: secondTopicID, Name: "Design"},
			}
			delete(topics, tt.omitTopic)

			// GetMessages returns rows newest first; entries reverses them to oldest first.
			f := &historyPathsBatch{
				partialBatch: partialBatch{t: t, found: []conversation.Message{newerMsg, olderMsg}},
				readErr:      tt.readErr,
				members:      members,
				names:        names,
				topics:       topics,
			}
			membership := org.Membership{
				Organization: org.Organization{ID: orgID},
				Member:       org.Member{ID: callerMemberID, OrganizationID: decoyOrgID},
			}
			reader, snapshot := testReader(t, f)
			got, err := reader.Many(t.Context(), membership, channelID, wantIDs)
			if !reflect.DeepEqual(snapshot.calls, []string{"snapshot", "reads", "members", "accounts"}) {
				t.Fatalf("snapshot calls = %v", snapshot.calls)
			}
			wantCalls := tt.wantCalls
			if wantCalls == nil {
				wantCalls = fullCalls
			}
			if !reflect.DeepEqual(f.calls, wantCalls) {
				t.Fatalf("calls = %v, want %v", f.calls, wantCalls)
			}
			if tt.checkScope {
				wantTopicIDs := []kernel.ID{secondTopicID, firstTopicID}
				if f.gotOrgID != orgID || f.gotChannelID != channelID || !reflect.DeepEqual(f.gotIDs, wantIDs) ||
					f.gotMemberOrgID != orgID || f.gotTopicOrgID != orgID || f.gotTopicChannelID != channelID || !reflect.DeepEqual(f.gotTopicIDs, wantTopicIDs) {
					t.Fatalf("scope = messages(%x, %x, %v) members(%x) topics(%x, %x, %v), want org %x channel %x ids %v topics %v",
						f.gotOrgID, f.gotChannelID, f.gotIDs, f.gotMemberOrgID, f.gotTopicOrgID, f.gotTopicChannelID, f.gotTopicIDs, orgID, channelID, wantIDs, wantTopicIDs)
				}
			}
			if tt.checkQuoteBatch {
				wantMemberBatch := [][]kernel.ID{{secondAuthorID, quotedAuthorID, firstAuthorID}}
				if !reflect.DeepEqual(f.memberBatches, wantMemberBatch) || len(f.accountBatches) != 1 || len(f.accountBatches[0]) != 3 {
					t.Fatalf("member batches = %v, account batches = %v; want %v and one 3-account batch", f.memberBatches, f.accountBatches, wantMemberBatch)
				}
			}
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || got != nil {
					t.Fatalf("Many = %+v, %v; want nil and error containing %q", got, err, tt.wantError)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || !tt.checkScope && !tt.checkQuoteBatch && !reflect.DeepEqual(got, wantEntries) {
				t.Fatalf("Many = %+v, %v; want %+v", got, err, wantEntries)
			}
		})
	}
}

func TestHistoryPathsAround(t *testing.T) {
	orgID, decoyOrgID, callerMemberID := kernel.ID{1}, kernel.ID{98}, kernel.ID{99}
	channelID, topicID, authorMemberID := kernel.ID{2}, kernel.ID{7}, kernel.ID{3}
	failure := errors.New("history store failed")
	prefix := []string{"snapshot", "reads", "channel", "channels", "selected", "topics", "members", "accounts", "history after"}
	fullCalls := append(slices.Clone(prefix), "history", "bind cursor", "cursor", "bind counts", "counts")

	for _, tt := range []struct {
		name              string
		messages          int
		around            int64
		fail              string
		entriesErr        bool
		wantOldest        int64
		wantNewest        int64
		wantOlder         bool
		wantError         string
		wantSnapshotCalls []string
	}{
		{name: "newer reversed plus older", messages: 50, around: 40, wantOldest: 1, wantNewest: 50, wantOlder: false, wantSnapshotCalls: fullCalls},
		{name: "more than room sets older", messages: 60, around: 40, wantOldest: 11, wantNewest: 60, wantOlder: true, wantSnapshotCalls: fullCalls},
		{name: "reading history around a message error is wrapped", messages: 50, around: 40, fail: "history after", wantError: "reading history around a message", wantSnapshotCalls: prefix},
		{name: "reading history before a message error is wrapped", messages: 50, around: 40, fail: "history", wantError: "reading history before a message", wantSnapshotCalls: append(slices.Clone(prefix), "history")},
		{name: "entries error stops before cursor", messages: 1, around: 1, entriesErr: true, wantError: "reading authors", wantSnapshotCalls: append(slices.Clone(prefix), "history")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var data readerData = fullHistory{n: tt.messages}
			var single *singleMessage
			if tt.entriesErr {
				single = &singleMessage{t: t, msg: conversation.Message{OrganizationID: orgID, MemberID: authorMemberID}, memberErr: failure}
				data = single
			}
			reader, snapshot := testReader(t, data)
			snapshot.historyData = fullHistory{n: tt.messages}
			snapshot.organizationID = orgID
			snapshot.fail = tt.fail
			snapshot.err = failure

			membership := org.Membership{
				Organization: org.Organization{ID: orgID},
				Member:       org.Member{ID: callerMemberID, OrganizationID: decoyOrgID},
			}
			page, err := reader.Around(t.Context(), membership, channelID, &topicID, tt.around)
			if !reflect.DeepEqual(snapshot.calls, tt.wantSnapshotCalls) {
				t.Fatalf("snapshot calls = %v, want %v", snapshot.calls, tt.wantSnapshotCalls)
			}
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || !errors.Is(err, failure) || !reflect.DeepEqual(page, conversation.ChannelPage{}) {
					t.Fatalf("Around = %+v, %v; want zero page and error wrapping %v with %q", page, err, failure, tt.wantError)
				}
				if single != nil && !reflect.DeepEqual(single.calls, []string{"members"}) {
					t.Fatalf("singleMessage calls = %v, want [members]", single.calls)
				}
				return
			}
			if err != nil || page.Older != tt.wantOlder || len(page.Entries) != int(tt.wantNewest-tt.wantOldest+1) {
				t.Fatalf("Around = %d entries (older=%v), err=%v; want %d..%d (older=%v)", len(page.Entries), page.Older, err, tt.wantOldest, tt.wantNewest, tt.wantOlder)
			}
			for i, entry := range page.Entries {
				if wantSeq := tt.wantOldest + int64(i); entry.EventSeq != wantSeq || entry.DisplayName != "Author" {
					t.Fatalf("entry[%d] = seq %d name %q, want seq %d name Author", i, entry.EventSeq, entry.DisplayName, wantSeq)
				}
			}
		})
	}
}
