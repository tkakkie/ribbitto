package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type pinsReadFake struct {
	fullHistory
	t                                                 *testing.T
	organizationID, channelID                         kernel.ID
	topics                                            map[kernel.ID]conversation.Topic
	members                                           map[kernel.ID]org.DirectoryEntry
	names                                             map[kernel.ID]string
	channelErr, pinsErr, topicErr, memberErr, nameErr error
	cursorErr                                         error
	cursorSeq                                         int64
	gotTopicIDs, gotMemberIDs                         []kernel.ID
	calls                                             []string
}

func (f *pinsReadFake) LookupTopics(_ context.Context, organizationID, channelID kernel.ID, topicIDs []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	f.t.Helper()
	f.calls = append(f.calls, "topics")
	if organizationID != f.organizationID || channelID != f.channelID {
		f.t.Errorf("LookupTopics(%x, %x), want (%x, %x)", organizationID, channelID, f.organizationID, f.channelID)
	}
	f.gotTopicIDs = slices.Clone(topicIDs)
	return f.topics, f.topicErr
}

func (f *pinsReadFake) LookupMembers(_ context.Context, organizationID kernel.ID, memberIDs []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	f.t.Helper()
	f.calls = append(f.calls, "members")
	if organizationID != f.organizationID {
		f.t.Errorf("LookupMembers organisation = %x, want membership's %x", organizationID, f.organizationID)
	}
	f.gotMemberIDs = slices.Clone(memberIDs)
	return f.members, f.memberErr
}

func (f *pinsReadFake) LookupDisplayNames(_ context.Context, accountIDs []kernel.ID) (map[kernel.ID]string, error) {
	f.t.Helper()
	f.calls = append(f.calls, "names")
	if len(accountIDs) != len(f.members) || slices.Contains(accountIDs, kernel.ID{18}) {
		f.t.Errorf("LookupDisplayNames account IDs = %v, want %d resolved member accounts", accountIDs, len(f.members))
	}
	return f.names, f.nameErr
}

type pinsReadStore struct {
	*snapshotFake
	f *pinsReadFake
}

func (r pinsReadStore) GetChannel(ctx context.Context, organizationID, channelID kernel.ID) (conversation.Channel, error) {
	r.t.Helper()
	r.f.calls = append(r.f.calls, "channel")
	if organizationID != r.organizationID || channelID != r.f.channelID {
		r.t.Errorf("GetChannel(%x, %x), want (%x, %x)", organizationID, channelID, r.organizationID, r.f.channelID)
	}
	ch, err := r.snapshotFake.GetChannel(ctx, organizationID, channelID)
	if r.f.channelErr != nil {
		return conversation.Channel{}, r.f.channelErr
	}
	return ch, err
}

func (r pinsReadStore) ListChannelPins(ctx context.Context, organizationID, channelID kernel.ID) ([]conversation.PinnedMessage, error) {
	r.t.Helper()
	r.f.calls = append(r.f.calls, "pins")
	if organizationID != r.organizationID || channelID != r.f.channelID {
		r.t.Errorf("ListChannelPins(%x, %x), want (%x, %x)", organizationID, channelID, r.organizationID, r.f.channelID)
	}
	pins, err := r.snapshotFake.ListChannelPins(ctx, organizationID, channelID)
	if r.f.pinsErr != nil {
		return nil, r.f.pinsErr
	}
	return pins, err
}

func (r pinsReadStore) EventSeq(ctx context.Context, organizationID kernel.ID) (int64, error) {
	r.t.Helper()
	r.f.calls = append(r.f.calls, "cursor")
	if organizationID != r.organizationID {
		r.t.Errorf("EventSeq organisation = %x, want membership's %x", organizationID, r.organizationID)
	}
	seq, err := r.snapshotFake.EventSeq(ctx, organizationID)
	if r.f.cursorErr != nil {
		return 0, r.f.cursorErr
	}
	if r.f.cursorSeq != 0 {
		return r.f.cursorSeq, err
	}
	return seq, err
}

func TestReaderPins(t *testing.T) {
	author1, pinner1, author2, pinner2 := kernel.ID{3}, kernel.ID{4}, kernel.ID{5}, kernel.ID{6}
	refMember, foreignRef, plainRef := kernel.ID{7}, kernel.ID{8}, kernel.ID{9}
	author1Account, pinner1Account, author2Account, pinner2Account := kernel.ID{13}, kernel.ID{14}, kernel.ID{15}, kernel.ID{16}
	refAccount, foreignAccount := kernel.ID{17}, kernel.ID{18}
	topic1, topic2 := kernel.ID{21}, kernel.ID{22}
	pinnedAt1, pinnedAt2 := time.Unix(100, 0), time.Unix(200, 0)
	chatBody := "deploy <@" + realtime.FormatPayloadID(refMember) + "> and <@" + realtime.FormatPayloadID(foreignRef) + ">"
	plainBody := "plain <@" + realtime.FormatPayloadID(plainRef) + ">"

	pin1 := conversation.PinnedMessage{
		Message:  conversation.Message{ID: kernel.ID{31}, TopicID: topic1, MemberID: author1, Body: chatBody, Format: conversation.FormatChatV1},
		PinState: conversation.PinState{PinnedByMemberID: pinner1, PinnedAt: pinnedAt1},
	}
	pin2 := conversation.PinnedMessage{
		Message:  conversation.Message{ID: kernel.ID{32}, TopicID: topic2, MemberID: author2, Body: plainBody, Format: conversation.FormatPlainText},
		PinState: conversation.PinState{PinnedByMemberID: pinner2, PinnedAt: pinnedAt2},
	}
	defaultPins := []conversation.PinnedMessage{pin1, pin2}
	defaultTopics := map[kernel.ID]conversation.Topic{
		topic1: {ID: topic1, Name: "Release", IsDefault: false},
		topic2: {ID: topic2, Name: "General", IsDefault: true},
	}
	defaultMembers := map[kernel.ID]org.DirectoryEntry{
		author1:   {AccountID: author1Account, Handle: "author-1"},
		pinner1:   {AccountID: pinner1Account, Handle: "pinner-1"},
		author2:   {AccountID: author2Account, Handle: "author-2"},
		pinner2:   {AccountID: pinner2Account, Handle: "pinner-2"},
		refMember: {AccountID: refAccount, Handle: "ref-member"},
	}
	defaultNames := map[kernel.ID]string{
		author1Account: "Author One",
		pinner1Account: "Pinner One",
		author2Account: "Author Two",
		pinner2Account: "Pinner Two",
		refAccount:     "Ref Member",
		foreignAccount: "Foreign Member",
	}
	wantPin1 := conversation.Pin{
		Entry: conversation.Entry{
			Message: pin1.Message, DisplayName: "Author One", Handle: "author-1", TopicName: "Release", DefaultTopic: false,
			Blocks:     conversation.ParseChatV1(chatBody),
			References: map[kernel.ID]conversation.Reference{refMember: {DisplayName: "Ref Member", Handle: "ref-member"}},
		},
		PinnerName: "Pinner One", PinnerHandle: "pinner-1", PinnedAt: pinnedAt1,
	}
	wantPin2 := conversation.Pin{
		Entry:      conversation.Entry{Message: pin2.Message, DisplayName: "Author Two", Handle: "author-2", TopicName: "General", DefaultTopic: true},
		PinnerName: "Pinner Two", PinnerHandle: "pinner-2", PinnedAt: pinnedAt2,
	}
	failure := errors.New("store or directory failed")
	fullSnapshotCalls := []string{"snapshot", "reads", "channel", "pins", "members", "accounts", "bind cursor", "cursor"}
	fullCalls := []string{"channel", "pins", "topics", "members", "names", "cursor"}

	for _, tt := range []struct {
		name                                              string
		orgID, channelID                                  kernel.ID
		pins                                              []conversation.PinnedMessage
		topics                                            map[kernel.ID]conversation.Topic
		names                                             map[kernel.ID]string
		channelErr, pinsErr, topicErr, memberErr, nameErr error
		cursorErr                                         error
		cursorSeq                                         int64
		pinsOnly, checkSnapshot                           bool
		wantTopicIDs, wantMemberIDs                       []kernel.ID
		wantSnapshot                                      conversation.PinsSnapshot
		wantPins                                          []conversation.Pin
		wantErr                                           error
		wantError                                         string
		wantSnapshotCalls, wantCalls                      []string
	}{
		{name: "forwards organisation and channel", orgID: kernel.ID{5}, channelID: kernel.ID{6}, wantSnapshot: conversation.PinsSnapshot{Pins: []conversation.Pin{wantPin1, wantPin2}, SnapshotSeq: 7}, checkSnapshot: true},
		{name: "member lookup includes authors pinners and only chat_v1 references", wantTopicIDs: []kernel.ID{topic1, topic2}, wantMemberIDs: []kernel.ID{author1, pinner1, refMember, foreignRef, author2, pinner2}},
		{name: "keeps pin order", pins: []conversation.PinnedMessage{pin2, pin1}, wantSnapshot: conversation.PinsSnapshot{Pins: []conversation.Pin{wantPin2, wantPin1}, SnapshotSeq: 7}, checkSnapshot: true},
		{name: "sets entry and pin fields", wantSnapshot: conversation.PinsSnapshot{Pins: []conversation.Pin{wantPin1, wantPin2}, SnapshotSeq: 7}, checkSnapshot: true},
		{name: "snapshot sequence comes from event cursor", cursorSeq: 42, wantSnapshot: conversation.PinsSnapshot{Pins: []conversation.Pin{wantPin1, wantPin2}, SnapshotSeq: 42}, checkSnapshot: true},
		{name: "Pins returns only the pins", pinsOnly: true, wantPins: []conversation.Pin{wantPin1, wantPin2}},
		{name: "get channel error is wrapped", channelErr: failure, wantErr: failure, wantError: "reading pins snapshot", wantSnapshotCalls: []string{"snapshot", "reads", "channel"}, wantCalls: []string{"channel"}},
		{name: "list pins error is wrapped", pinsErr: failure, wantErr: failure, wantError: "reading pins snapshot", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins"}, wantCalls: []string{"channel", "pins"}},
		{name: "lookup topics error is wrapped", topicErr: failure, wantErr: failure, wantError: "reading pin topics", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins"}, wantCalls: []string{"channel", "pins", "topics"}},
		{name: "lookup members error is wrapped", memberErr: failure, wantErr: failure, wantError: "reading pin members", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins", "members"}, wantCalls: []string{"channel", "pins", "topics", "members"}},
		{name: "lookup display names error is wrapped", nameErr: failure, wantErr: failure, wantError: "reading pin names", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins", "members", "accounts"}, wantCalls: []string{"channel", "pins", "topics", "members", "names"}},
		{name: "event cursor error is wrapped and zeroes snapshot", cursorErr: failure, wantErr: failure, wantError: "reading pins snapshot"},
		{name: "missing author name", pins: []conversation.PinnedMessage{pin1}, names: map[kernel.ID]string{pinner1Account: "Pinner One"}, wantError: "missing names or topic for pin", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins", "members", "accounts"}, wantCalls: []string{"channel", "pins", "topics", "members", "names"}},
		{name: "missing pinner name", pins: []conversation.PinnedMessage{pin1}, names: map[kernel.ID]string{author1Account: "Author One"}, wantError: "missing names or topic for pin", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins", "members", "accounts"}, wantCalls: []string{"channel", "pins", "topics", "members", "names"}},
		{name: "missing topic", pins: []conversation.PinnedMessage{pin1}, topics: map[kernel.ID]conversation.Topic{}, wantError: "missing names or topic for pin", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins", "members", "accounts"}, wantCalls: []string{"channel", "pins", "topics", "members", "names"}},
		{name: "Pins get channel error is wrapped", pinsOnly: true, channelErr: failure, wantErr: failure, wantError: "reading pins snapshot: store or directory failed", wantSnapshotCalls: []string{"snapshot", "reads", "channel"}, wantCalls: []string{"channel"}},
		{name: "Pins list pins error is wrapped", pinsOnly: true, pinsErr: failure, wantErr: failure, wantError: "reading pins snapshot: store or directory failed", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins"}, wantCalls: []string{"channel", "pins"}},
		{name: "Pins lookup topics error is wrapped", pinsOnly: true, topicErr: failure, wantErr: failure, wantError: "reading pin topics: store or directory failed", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins"}, wantCalls: []string{"channel", "pins", "topics"}},
		{name: "Pins lookup members error is wrapped", pinsOnly: true, memberErr: failure, wantErr: failure, wantError: "reading pin members: store or directory failed", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins", "members"}, wantCalls: []string{"channel", "pins", "topics", "members"}},
		{name: "Pins lookup display names error is wrapped", pinsOnly: true, nameErr: failure, wantErr: failure, wantError: "reading pin names: store or directory failed", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "pins", "members", "accounts"}, wantCalls: []string{"channel", "pins", "topics", "members", "names"}},
		{name: "Pins event cursor error is wrapped and drops built pins", pinsOnly: true, cursorErr: failure, wantErr: failure, wantError: "reading pins snapshot: store or directory failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			orgID, channelID := tt.orgID, tt.channelID
			if orgID == (kernel.ID{}) {
				orgID = kernel.ID{1}
			}
			if channelID == (kernel.ID{}) {
				channelID = kernel.ID{2}
			}
			pins, topics, names := tt.pins, tt.topics, tt.names
			if pins == nil {
				pins = defaultPins
			}
			if topics == nil {
				topics = defaultTopics
			}
			if names == nil {
				names = defaultNames
			}
			f := &pinsReadFake{
				t: t, organizationID: orgID, channelID: channelID,
				topics: topics, members: defaultMembers, names: names,
				channelErr: tt.channelErr, pinsErr: tt.pinsErr, topicErr: tt.topicErr, memberErr: tt.memberErr, nameErr: tt.nameErr,
				cursorErr: tt.cursorErr, cursorSeq: tt.cursorSeq,
			}
			_, snapshot := testReader(t, f)
			snapshot.organizationID = orgID
			snapshot.pins = pins
			reads := pinsReadStore{snapshotFake: snapshot, f: f}
			reader := conversation.NewReader(snapshot, nil,
				func(s platform.Snapshot) conversation.ReadStore { snapshot.bound(s, "reads"); return reads },
				func(s platform.Snapshot) conversation.MemberDirectory { snapshot.bound(s, "members"); return f },
				func(s platform.Snapshot) conversation.AccountDirectory { snapshot.bound(s, "accounts"); return f },
				func(s platform.Snapshot) conversation.EventCursor { snapshot.bound(s, "bind cursor"); return reads },
				nil)

			// Member.OrganizationID differs from Organization.ID so reading the wrong
			// field fails the organisation checks across the snapshot.
			membership := org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: kernel.ID{99}, OrganizationID: kernel.ID{98}}}
			if tt.pinsOnly {
				gotPins, err := reader.Pins(t.Context(), membership, channelID)
				if tt.wantError != "" {
					if err == nil || !strings.Contains(err.Error(), "reading pins snapshot: ") || !strings.Contains(err.Error(), tt.wantError) || gotPins != nil {
						t.Fatalf("Pins = %+v, %v; want nil and error containing %q", gotPins, err, tt.wantError)
					}
					if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
						t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
					}
				} else if err != nil || !reflect.DeepEqual(gotPins, tt.wantPins) {
					t.Fatalf("Pins = %+v, %v; want %+v, nil", gotPins, err, tt.wantPins)
				}
			} else {
				got, err := reader.ReadPins(t.Context(), membership, channelID)
				if tt.wantError != "" {
					if err == nil || !strings.Contains(err.Error(), "reading pins snapshot") || !strings.Contains(err.Error(), tt.wantError) || !reflect.DeepEqual(got, conversation.PinsSnapshot{}) {
						t.Fatalf("ReadPins = %+v, %v; want zero snapshot and error containing %q", got, err, tt.wantError)
					}
					if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
						t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
					}
				} else if err != nil {
					t.Fatalf("unexpected error: %v", err)
				} else if tt.checkSnapshot && !reflect.DeepEqual(got, tt.wantSnapshot) {
					t.Fatalf("ReadPins = %+v, want %+v", got, tt.wantSnapshot)
				}
			}

			wantSnapshotCalls, wantCalls := tt.wantSnapshotCalls, tt.wantCalls
			if wantSnapshotCalls == nil {
				wantSnapshotCalls = fullSnapshotCalls
			}
			if wantCalls == nil {
				wantCalls = fullCalls
			}
			if !reflect.DeepEqual(snapshot.calls, wantSnapshotCalls) {
				t.Fatalf("snapshot calls = %v, want %v", snapshot.calls, wantSnapshotCalls)
			}
			if !reflect.DeepEqual(f.calls, wantCalls) {
				t.Fatalf("calls = %v, want %v", f.calls, wantCalls)
			}
			if tt.wantTopicIDs != nil && !reflect.DeepEqual(f.gotTopicIDs, tt.wantTopicIDs) {
				t.Fatalf("LookupTopics topic IDs = %v, want %v", f.gotTopicIDs, tt.wantTopicIDs)
			}
			if tt.wantMemberIDs != nil && !reflect.DeepEqual(f.gotMemberIDs, tt.wantMemberIDs) {
				t.Fatalf("LookupMembers member IDs = %v, want %v", f.gotMemberIDs, tt.wantMemberIDs)
			}
		})
	}
}
