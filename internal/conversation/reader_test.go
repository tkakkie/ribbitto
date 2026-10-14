package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// historyData is ReadStore's message and topic-batch reads, which the reader
// tests' fakes supply.
type historyData interface {
	GetMessages(ctx context.Context, organizationID, channelID kernel.ID, ids []kernel.ID) ([]conversation.Message, error)
	ListMessagesBefore(ctx context.Context, organizationID, channelID kernel.ID, topicID *kernel.ID, before *int64, limit int32) ([]conversation.Message, error)
	GetMessage(ctx context.Context, organizationID, channelID kernel.ID, eventSeq int64) (conversation.Message, error)
	LookupTopics(ctx context.Context, organizationID, channelID kernel.ID, topicIDs []kernel.ID) (map[kernel.ID]conversation.Topic, error)
}
type readerData interface {
	historyData
	conversation.MemberDirectory
	conversation.AccountDirectory
}
type snapshotFake struct {
	*postingFake
	historyData
	organizationID kernel.ID
	// Sentinel lists, empty unless a test needs to see them reach its page.
	channels []conversation.Channel
	topics   []conversation.Topic
}

func (f *snapshotFake) InSnapshot(_ context.Context, fn func(platform.Snapshot) error) error {
	_ = f.step("snapshot")
	f.active = true
	defer func() { f.active = false }()
	return fn(platform.Snapshot{})
}
func (f *snapshotFake) bound(s platform.Snapshot, name string) {
	f.t.Helper()
	if !f.active || s != (platform.Snapshot{}) {
		f.t.Fatal("factory outside runner or wrong snapshot")
	}
	_ = f.step(name)
}
func (f *snapshotFake) GetChannel(context.Context, kernel.ID, kernel.ID) (conversation.Channel, error) {
	return conversation.Channel{}, f.step("channel")
}
func (f *snapshotFake) ListChannels(_ context.Context, organizationID kernel.ID) ([]conversation.Channel, error) {
	f.t.Helper()
	if organizationID != f.organizationID {
		f.t.Fatalf("ListChannels organisation = %x, want membership's %x", organizationID, f.organizationID)
	}
	return f.channels, f.step("channels")
}
func (f *snapshotFake) GetTopic(_ context.Context, _, _, id kernel.ID) (conversation.Topic, error) {
	return conversation.Topic{ID: id}, f.step("selected")
}
func (f *snapshotFake) ListTopics(_ context.Context, _, _ kernel.ID, limit int) ([]conversation.Topic, error) {
	if limit != 50 {
		f.t.Fatal("wrong sidebar limit:", limit)
	}
	return f.topics, f.step("topics")
}
func (f *snapshotFake) ListMessagesBefore(ctx context.Context, orgID, ch kernel.ID, topic *kernel.ID, before *int64, limit int32) ([]conversation.Message, error) {
	if err := f.step("history"); err != nil {
		return nil, err
	}
	return f.historyData.ListMessagesBefore(ctx, orgID, ch, topic, before, limit)
}
func (f *snapshotFake) EventSeq(context.Context, kernel.ID) (int64, error) {
	return 7, f.step("cursor")
}
func testReader(t *testing.T, data readerData) (*conversation.Reader, *snapshotFake) {
	t.Helper()
	f := &snapshotFake{postingFake: &postingFake{t: t, err: conversation.ErrMessageNotFound}, historyData: data}
	reader := conversation.NewReader(f,
		func(s platform.Snapshot) conversation.ReadStore { f.bound(s, "reads"); return f },
		func(s platform.Snapshot) conversation.MemberDirectory { f.bound(s, "members"); return data },
		func(s platform.Snapshot) conversation.AccountDirectory { f.bound(s, "accounts"); return data },
		func(s platform.Snapshot) conversation.EventCursor { f.bound(s, "bind cursor"); return f },
		func(s platform.Snapshot) conversation.PageCounter { f.bound(s, "bind counts"); return f })
	return reader, f
}
func TestReaderPageSnapshot(t *testing.T) {
	membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	order := []string{"snapshot", "reads", "channel", "channels", "selected", "topics", "members", "accounts", "history", "bind cursor", "cursor", "bind counts", "counts"}
	for _, fail := range []string{"", "channel", "channels", "selected", "topics", "history", "cursor", "counts"} {
		t.Run("failure="+fail, func(t *testing.T) {
			reader, f := testReader(t, fullHistory{})
			f.fail = fail
			f.organizationID = membership.Organization.ID
			page, err := reader.Page(t.Context(), membership, kernel.ID{2}, &kernel.ID{7}, nil)
			want, wantErr := order, error(nil)
			if fail != "" {
				want, wantErr = order[:slices.Index(order, fail)+1], f.err
			}
			if !reflect.DeepEqual(f.calls, want) || !errors.Is(err, wantErr) {
				t.Fatalf("calls/error = %v/%v, want %v/%v", f.calls, err, want, wantErr)
			}
			if fail == "" && (page.EventCursor == nil || *page.EventCursor != 7) {
				t.Fatal("missing latest cursor")
			}
		})
	}
	reader, f := testReader(t, fullHistory{})
	f.organizationID = membership.Organization.ID
	before := int64(9)
	page, err := reader.Page(t.Context(), membership, kernel.ID{2}, nil, &before)
	if err != nil || page.EventCursor == nil || *page.EventCursor != 7 || !reflect.DeepEqual(f.calls, []string{"snapshot", "reads", "channel", "channels", "topics", "members", "accounts", "history", "bind cursor", "cursor", "bind counts", "counts"}) {
		t.Fatalf("older page = %+v, %v, calls %v", page, err, f.calls)
	}
}

func TestReaderMembersSnapshot(t *testing.T) {
	membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	order := []string{"snapshot", "reads", "channel", "channels", "topics", "members", "accounts", "bind cursor", "cursor", "bind counts", "counts"}
	for _, fail := range []string{"", "channel", "channels", "topics", "cursor", "counts"} {
		t.Run("failure="+fail, func(t *testing.T) {
			reader, f := testReader(t, fullHistory{})
			f.fail, f.organizationID = fail, membership.Organization.ID
			page, err := reader.Members(t.Context(), membership, kernel.ID{2}, nil)
			want, wantErr := order, error(nil)
			if fail != "" {
				want, wantErr = order[:slices.Index(order, fail)+1], f.err
			}
			if !reflect.DeepEqual(f.calls, want) || !errors.Is(err, wantErr) {
				t.Fatalf("calls/error = %v/%v, want %v/%v", f.calls, err, want, wantErr)
			}
			if fail == "" && (page.EventCursor == nil || *page.EventCursor != 7) {
				t.Fatal("missing members page cursor")
			}
		})
	}
}

func (f *snapshotFake) Read(context.Context, org.Membership, kernel.ID, []kernel.ID, []kernel.ID, *kernel.ID) (conversation.PageCounts, error) {
	return conversation.PageCounts{}, f.step("counts")
}

func TestReaderSidebarSnapshot(t *testing.T) {
	membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	for _, selected := range []*kernel.ID{nil, {7}} {
		order := []string{"snapshot", "reads", "channel", "channels"}
		if selected != nil {
			order = append(order, "selected")
		}
		order = append(order, "topics", "bind counts", "counts")
		failures := []string{"", "channel", "channels", "topics", "counts"}
		if selected != nil {
			failures = append(failures, "selected")
		}
		for _, fail := range failures {
			t.Run("failure="+fail, func(t *testing.T) {
				reader, f := testReader(t, nil)
				f.fail, f.organizationID = fail, membership.Organization.ID
				_, err := reader.Sidebar(t.Context(), membership, kernel.ID{2}, selected)
				want, wantErr := order, error(nil)
				if fail != "" {
					want, wantErr = order[:slices.Index(order, fail)+1], f.err
				}
				if !reflect.DeepEqual(f.calls, want) || !errors.Is(err, wantErr) {
					t.Fatalf("calls/error = %v/%v, want %v/%v", f.calls, err, want, wantErr)
				}
			})
		}
	}
}

// sidebarCountsFake adds the counts-only read, so a page that asked for first
// unread sequences would record "counts" instead of "counts only".
type sidebarCountsFake struct{ *snapshotFake }

func (f sidebarCountsFake) ReadCounts(context.Context, org.Membership, kernel.ID, []kernel.ID, []kernel.ID, *kernel.ID) (conversation.PageCounts, error) {
	counts := conversation.PageCounts{ChannelCounts: map[kernel.ID]int64{{3}: 4}, TopicCounts: map[kernel.ID]int64{{5}: 6}}
	return counts, f.step("counts only")
}

func TestReaderChannelListSnapshot(t *testing.T) {
	membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	order := []string{"snapshot", "reads", "channel", "channels", "topics", "bind cursor", "cursor", "bind counts", "counts only"}
	for _, fail := range []string{"", "channel", "channels", "topics", "cursor", "counts only"} {
		t.Run("failure="+fail, func(t *testing.T) {
			f := &snapshotFake{postingFake: &postingFake{t: t, err: conversation.ErrMessageNotFound}, organizationID: membership.Organization.ID,
				channels: []conversation.Channel{{ID: kernel.ID{3}}}, topics: []conversation.Topic{{ID: kernel.ID{5}}}}
			f.fail = fail
			reader := conversation.NewReader(f,
				func(s platform.Snapshot) conversation.ReadStore { f.bound(s, "reads"); return f },
				nil, nil,
				func(s platform.Snapshot) conversation.EventCursor { f.bound(s, "bind cursor"); return f },
				func(s platform.Snapshot) conversation.PageCounter {
					f.bound(s, "bind counts")
					return sidebarCountsFake{f}
				})
			page, err := reader.ChannelList(t.Context(), membership, kernel.ID{2})
			want, wantErr := order, error(nil)
			if fail != "" {
				want, wantErr = order[:slices.Index(order, fail)+1], f.err
			}
			if !reflect.DeepEqual(f.calls, want) || !errors.Is(err, wantErr) {
				t.Fatalf("calls/error = %v/%v, want %v/%v", f.calls, err, want, wantErr)
			}
			if fail == "" && (page.EventCursor != 7 || page.Topic != nil || !reflect.DeepEqual(page.Channels, f.channels) || !reflect.DeepEqual(page.Topics, f.topics) ||
				page.ChannelCounts[kernel.ID{3}] != 4 || page.TopicCounts[kernel.ID{5}] != 6) {
				t.Fatalf("page = %+v, want the snapshot's lists, counts and cursor 7", page)
			}
		})
	}
}
