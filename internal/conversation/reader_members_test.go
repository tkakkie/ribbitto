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
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type membersFake struct {
	fullHistory
	t                              *testing.T
	organizationID                 kernel.ID
	channelID                      kernel.ID
	wantAfter                      *kernel.ID
	members                        []org.ListedMember
	names                          map[kernel.ID]string
	channelErr, memberErr, nameErr error
	gotLimit                       int32
	gotAccountIDs                  []kernel.ID
	calls                          []string
}

func (f *membersFake) ListMembers(_ context.Context, organizationID kernel.ID, after *kernel.ID, limit int32) ([]org.ListedMember, error) {
	f.t.Helper()
	f.calls = append(f.calls, "members")
	if organizationID != f.organizationID {
		f.t.Fatalf("ListMembers organisation = %x, want membership's %x", organizationID, f.organizationID)
	}
	if !reflect.DeepEqual(after, f.wantAfter) {
		f.t.Fatalf("ListMembers after = %v, want %v", after, f.wantAfter)
	}
	f.gotLimit = limit
	return f.members, f.memberErr
}

func (f *membersFake) LookupDisplayNames(_ context.Context, accountIDs []kernel.ID) (map[kernel.ID]string, error) {
	f.t.Helper()
	f.calls = append(f.calls, "names")
	f.gotAccountIDs = slices.Clone(accountIDs)
	return f.names, f.nameErr
}

type membersReadStore struct {
	*snapshotFake
	f *membersFake
}

func (r membersReadStore) GetChannel(ctx context.Context, organizationID, channelID kernel.ID) (conversation.Channel, error) {
	r.t.Helper()
	r.f.calls = append(r.f.calls, "channel")
	if organizationID != r.organizationID || channelID != r.f.channelID {
		r.t.Fatalf("GetChannel(%x, %x), want (%x, %x)", organizationID, channelID, r.organizationID, r.f.channelID)
	}
	ch, err := r.snapshotFake.GetChannel(ctx, organizationID, channelID)
	if r.f.channelErr != nil {
		return conversation.Channel{}, r.f.channelErr
	}
	return ch, err
}

func memberRows(n int) ([]org.ListedMember, map[kernel.ID]string, []conversation.ChannelMember, []kernel.ID) {
	listed := make([]org.ListedMember, n)
	names := make(map[kernel.ID]string, n)
	members := make([]conversation.ChannelMember, n)
	accounts := make([]kernel.ID, n)
	for i := range n {
		id := kernel.ID{1, byte(i + 1)}
		accountID := kernel.ID{2, byte(i + 1)}
		handle := fmt.Sprintf("member-%d", i+1)
		name := fmt.Sprintf("Member %d", i+1)
		listed[i] = org.ListedMember{ID: id, DirectoryEntry: org.DirectoryEntry{AccountID: accountID, Handle: handle}}
		names[accountID] = name
		members[i] = conversation.ChannelMember{ID: id, DisplayName: name, Handle: handle}
		accounts[i] = accountID
	}
	return listed, names, members, accounts
}

func TestReaderMembers(t *testing.T) {
	fullListed, fullNames, fullWant, fullAccounts := memberRows(conversation.MemberPageSize)
	extraListed, _, _, _ := memberRows(conversation.MemberPageSize + 1)
	lastShown := fullWant[conversation.MemberPageSize-1].ID
	afterCursor := kernel.ID{9}
	failure := errors.New("directory or store failed")
	fullSnapshotCalls := []string{"snapshot", "reads", "channel", "members", "accounts"}
	fullCalls := []string{"channel", "members", "names"}

	for _, tt := range []struct {
		name                           string
		orgID, channelID               kernel.ID
		after                          *kernel.ID
		members                        []org.ListedMember
		names                          map[kernel.ID]string
		channelErr, memberErr, nameErr error
		wantLimit                      int32
		wantAccounts                   []kernel.ID
		wantPage                       conversation.MembersPage
		checkPage                      bool
		wantErr                        error
		wantError                      string
		wantSnapshotCalls              []string
		wantCalls                      []string
	}{
		{name: "limit is page size plus one", wantLimit: conversation.MemberPageSize + 1},
		{name: "exact page has nil next", members: fullListed, names: fullNames, wantPage: conversation.MembersPage{Members: fullWant}, checkPage: true},
		{name: "extra row trims page and sets next", members: extraListed, names: fullNames, wantPage: conversation.MembersPage{Members: fullWant, Next: &lastShown}, checkPage: true},
		{name: "only trimmed account IDs reach display-name lookup", members: extraListed, names: fullNames, wantAccounts: fullAccounts},
		{name: "forwards organisation and after cursor", orgID: kernel.ID{5}, channelID: kernel.ID{6}, after: &afterCursor, members: fullListed[:1], names: fullNames, wantPage: conversation.MembersPage{Members: fullWant[:1]}, checkPage: true},
		{name: "missing display name", members: fullListed[:2], names: map[kernel.ID]string{fullAccounts[0]: fullWant[0].DisplayName}, wantError: "missing name for member"},
		{name: "get channel error is wrapped", channelErr: failure, wantErr: failure, wantError: "finding channel", wantSnapshotCalls: []string{"snapshot", "reads", "channel"}, wantCalls: []string{"channel"}},
		{name: "list members error is wrapped", memberErr: failure, wantErr: failure, wantError: "listing channel members", wantSnapshotCalls: []string{"snapshot", "reads", "channel", "members"}, wantCalls: []string{"channel", "members"}},
		{name: "lookup display names error is wrapped", members: extraListed, nameErr: failure, wantErr: failure, wantError: "reading member names"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			orgID, channelID := tt.orgID, tt.channelID
			if orgID == (kernel.ID{}) {
				orgID = kernel.ID{1}
			}
			if channelID == (kernel.ID{}) {
				channelID = kernel.ID{2}
			}
			f := &membersFake{t: t, organizationID: orgID, channelID: channelID, wantAfter: tt.after, members: tt.members, names: tt.names, channelErr: tt.channelErr, memberErr: tt.memberErr, nameErr: tt.nameErr}
			_, snapshot := testReader(t, f)
			snapshot.organizationID = orgID
			reads := membersReadStore{snapshotFake: snapshot, f: f}
			reader := conversation.NewReader(snapshot, nil,
				func(s platform.Snapshot) conversation.ReadStore { snapshot.bound(s, "reads"); return reads },
				func(s platform.Snapshot) conversation.MemberDirectory { snapshot.bound(s, "members"); return f },
				func(s platform.Snapshot) conversation.AccountDirectory { snapshot.bound(s, "accounts"); return f },
				nil, nil)

			// Member.OrganizationID differs from Organization.ID so a read from the
			// wrong field fails the organisation checks in GetChannel and ListMembers.
			membership := org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{ID: kernel.ID{99}, OrganizationID: kernel.ID{98}}}
			got, err := reader.Members(t.Context(), membership, channelID, tt.after)

			wantSnapshotCalls := tt.wantSnapshotCalls
			if wantSnapshotCalls == nil {
				wantSnapshotCalls = fullSnapshotCalls
			}
			if !reflect.DeepEqual(snapshot.calls, wantSnapshotCalls) {
				t.Fatalf("snapshot calls = %v, want %v", snapshot.calls, wantSnapshotCalls)
			}
			wantCalls := tt.wantCalls
			if wantCalls == nil {
				wantCalls = fullCalls
			}
			if !reflect.DeepEqual(f.calls, wantCalls) {
				t.Fatalf("calls = %v, want %v", f.calls, wantCalls)
			}
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || !reflect.DeepEqual(got, conversation.MembersPage{}) {
					t.Fatalf("Members = %+v, %v; want zero page and error containing %q", got, err, tt.wantError)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantLimit != 0 && f.gotLimit != tt.wantLimit {
				t.Fatalf("ListMembers limit = %d, want %d", f.gotLimit, tt.wantLimit)
			}
			if tt.wantAccounts != nil && !reflect.DeepEqual(f.gotAccountIDs, tt.wantAccounts) {
				t.Fatalf("LookupDisplayNames account IDs = %v, want %v", f.gotAccountIDs, tt.wantAccounts)
			}
			if tt.checkPage && !reflect.DeepEqual(got, tt.wantPage) {
				t.Fatalf("Members = %+v, want %+v", got, tt.wantPage)
			}
		})
	}
}
