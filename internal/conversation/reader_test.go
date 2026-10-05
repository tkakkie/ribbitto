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

type readerData interface {
	conversation.History
	conversation.TopicDirectory
	conversation.MemberDirectory
	conversation.AccountDirectory
}
type snapshotFake struct {
	*postingFake
	conversation.History
	conversation.TopicDirectory
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
func (f *snapshotFake) ListChannels(context.Context, kernel.ID) ([]conversation.Channel, error) {
	return nil, f.step("channels")
}
func (f *snapshotFake) GetTopic(_ context.Context, _, _, id kernel.ID) (conversation.Topic, error) {
	return conversation.Topic{ID: id}, f.step("selected")
}
func (f *snapshotFake) ListTopics(_ context.Context, _, _ kernel.ID, limit int) ([]conversation.Topic, error) {
	if limit != 50 {
		f.t.Fatal("wrong sidebar limit:", limit)
	}
	return nil, f.step("topics")
}
func (f *snapshotFake) ListMessagesBefore(ctx context.Context, orgID, ch kernel.ID, topic *kernel.ID, before *int64, limit int32) ([]conversation.Message, error) {
	if err := f.step("history"); err != nil {
		return nil, err
	}
	return f.History.ListMessagesBefore(ctx, orgID, ch, topic, before, limit)
}
func (f *snapshotFake) EventSeq(context.Context, kernel.ID) (int64, error) {
	return 7, f.step("cursor")
}
func testReader(t *testing.T, data readerData) (*conversation.Reader, *snapshotFake) {
	t.Helper()
	f := &snapshotFake{postingFake: &postingFake{t: t, err: conversation.ErrMessageNotFound}, History: data, TopicDirectory: data}
	reader := conversation.NewReader(f,
		func(s platform.Snapshot) conversation.ReadStore { f.bound(s, "reads"); return f },
		func(s platform.Snapshot) conversation.MemberDirectory { f.bound(s, "members"); return data },
		func(s platform.Snapshot) conversation.AccountDirectory { f.bound(s, "accounts"); return data },
		func(s platform.Snapshot) conversation.EventCursor { f.bound(s, "bind cursor"); return f })
	return reader, f
}
func TestReaderPageSnapshot(t *testing.T) {
	order := []string{"snapshot", "reads", "channel", "channels", "selected", "topics", "members", "accounts", "history", "bind cursor", "cursor"}
	for _, fail := range []string{"", "channel", "channels", "selected", "topics", "history", "cursor"} {
		t.Run("failure="+fail, func(t *testing.T) {
			reader, f := testReader(t, fullHistory{})
			f.fail = fail
			page, err := reader.Page(t.Context(), org.Membership{}, kernel.ID{2}, &kernel.ID{7}, nil)
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
	before := int64(9)
	page, err := reader.Before(t.Context(), org.Membership{}, kernel.ID{2}, &before)
	if err != nil || page.EventCursor != nil || !reflect.DeepEqual(f.calls, []string{"snapshot", "reads", "channel", "channels", "topics", "members", "accounts", "history"}) {
		t.Fatalf("older page = %+v, %v, calls %v", page, err, f.calls)
	}
}
