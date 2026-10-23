package conversation_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func TestReaderSummary(t *testing.T) {
	failure := errors.New("directory or store failed")
	pinned := func(n int) []conversation.PinnedMessage {
		return make([]conversation.PinnedMessage, n)
	}
	fullListed, _, _, _ := memberRows(conversation.MemberPageSize)
	extraListed, _, _, _ := memberRows(conversation.MemberPageSize + 1)
	for _, tt := range []struct {
		name      string
		members   []org.ListedMember
		pins      []conversation.PinnedMessage
		memberErr error
		want      conversation.ChannelSummary
		wantErr   error
		wantLimit int32
	}{
		{name: "empty channel", wantLimit: conversation.MemberPageSize + 1},
		{name: "members and pins", members: fullListed[:3], pins: pinned(2), want: conversation.ChannelSummary{Members: 3, Pins: 2}},
		{name: "exactly one page", members: fullListed, want: conversation.ChannelSummary{Members: conversation.MemberPageSize}},
		{name: "more than a page", members: extraListed, pins: pinned(1), want: conversation.ChannelSummary{Members: conversation.MemberPageSize, MoreMembers: true, Pins: 1}},
		{name: "member listing error is wrapped", memberErr: failure, wantErr: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			orgID, channelID := kernel.ID{1}, kernel.ID{2}
			f := &membersFake{t: t, organizationID: orgID, channelID: channelID, members: tt.members, memberErr: tt.memberErr}
			_, snapshot := testReader(t, f)
			snapshot.organizationID = orgID
			snapshot.pins = tt.pins
			reads := membersReadStore{snapshotFake: snapshot, f: f}
			reader := conversation.NewReader(snapshot, nil,
				func(s platform.Snapshot) conversation.ReadStore { snapshot.bound(s, "reads"); return reads },
				func(s platform.Snapshot) conversation.MemberDirectory { snapshot.bound(s, "members"); return f },
				func(s platform.Snapshot) conversation.AccountDirectory { snapshot.bound(s, "accounts"); return f },
				nil, nil)

			// Member.OrganizationID differs from Organization.ID so a read from the
			// wrong field fails the organisation check in ListMembers.
			membership := org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: kernel.ID{99}, OrganizationID: kernel.ID{98}}}
			got, err := reader.Summary(t.Context(), membership, channelID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) || got != (conversation.ChannelSummary{}) {
					t.Fatalf("Summary = %+v, %v; want zero summary and %v", got, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Summary = %+v, want %+v", got, tt.want)
			}
			if tt.wantLimit != 0 && f.gotLimit != tt.wantLimit {
				t.Fatalf("ListMembers limit = %d, want %d", f.gotLimit, tt.wantLimit)
			}
		})
	}
}
