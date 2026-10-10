package conversation_test

import (
	"context"
	"reflect"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type branchFake struct {
	postingFake
	seq   int64
	short bool
}

func (f *branchFake) NextEventSeq(_ context.Context, organizationID kernel.ID) (int64, error) {
	f.scope(organizationID, kernel.ID{3})
	f.seq++
	step := "move sequence"
	if f.seq == 2 {
		step = "notice sequence"
	}
	return f.seq + 40, f.step(step)
}
func (f *branchFake) GetTopic(_ context.Context, organizationID, channelID, id kernel.ID) (conversation.Topic, error) {
	f.scope(organizationID, channelID)
	step := "source"
	if id == (kernel.ID{9}) {
		step = "destination"
	} else if id != (kernel.ID{4}) {
		f.t.Fatal("wrong topic")
	}
	return conversation.Topic{ID: id, Name: "設計"}, f.step(step)
}
func (f *branchFake) CreateTopic(_ context.Context, organizationID, channelID kernel.ID, name string) (conversation.Topic, error) {
	f.scope(organizationID, channelID)
	if name != "設計" {
		f.t.Fatal("new name was not normalised")
	}
	return conversation.Topic{ID: kernel.ID{9}, Name: name}, f.step("create")
}
func (f *branchFake) MoveMessages(_ context.Context, organizationID, channelID, from, to kernel.ID, ids []kernel.ID, movedEventSeq int64) (int64, error) {
	f.scope(organizationID, channelID)
	if movedEventSeq != 41 || from != (kernel.ID{4}) || to != (kernel.ID{9}) || !reflect.DeepEqual(ids, []kernel.ID{{5}}) {
		f.t.Fatal("wrong move arguments")
	}
	count := int64(len(ids))
	if f.short {
		count--
	}
	return count, f.step("move")
}
func (f *branchFake) InsertNotice(_ context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, seq int64) (conversation.Message, error) {
	if body != "to 設計" || seq != 42 {
		f.t.Fatal("wrong notice")
	}
	f.scope(organizationID, channelID)
	if topicID != (kernel.ID{4}) || memberID != (kernel.ID{2}) {
		f.t.Fatal("wrong notice scope")
	}
	return conversation.Message{ID: kernel.ID{6}, TopicID: topicID}, f.step("notice insert")
}
func (f *branchFake) InsertMessage(context.Context, kernel.ID, kernel.ID, kernel.ID, kernel.ID, string, int64) (conversation.Message, error) {
	f.t.Fatal("branching called posting's mapped insert")
	return conversation.Message{}, nil
}
func (f *branchFake) Append(_ context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error {
	f.scope(organizationID, kernel.ID{3})
	if audience != nil {
		f.t.Fatal("unexpected audience")
	}
	if kind == conversation.KindMessagesMoved {
		got, err := conversation.DecodeMoved(payload)
		want := conversation.Moved{ChannelID: kernel.ID{3}, FromTopicID: kernel.ID{4}, ToTopicID: kernel.ID{9}, MessageIDs: []kernel.ID{{5}}}
		if err != nil || !reflect.DeepEqual(got, want) || seq != 41 {
			f.t.Fatalf("move event: %+v, %v", got, err)
		}
		return f.step("moved append")
	}
	got, err := conversation.DecodePosted(payload)
	if err != nil || kind != conversation.KindPosted || seq != 42 || got.ChannelID != (kernel.ID{3}) || got.MessageID != (kernel.ID{6}) || got.TopicID == nil || *got.TopicID != (kernel.ID{4}) {
		f.t.Fatalf("notice event: %+v, %v", got, err)
	}
	return f.step("posted append")
}
func (f *branchFake) Raise(organizationID kernel.ID, seq int64) {
	if !f.committed || f.active || organizationID != (kernel.ID{1}) || seq != 42 {
		f.t.Fatal("notification before commit or wrong notice sequence")
	}
	_ = f.step("raise")
}
func (f *branchFake) brancher(notifier conversation.Notifier) *conversation.Brancher {
	return conversation.NewBrancher(f,
		func(tx platform.Tx) conversation.Writer { f.bound(tx); return f },
		func(tx platform.Tx) conversation.EventSequence { f.bound(tx); return f },
		func(tx platform.Tx) conversation.EventAppender { f.bound(tx); return f },
		func(tx platform.Tx) conversation.ReadRangeWriter { f.bound(tx); return f }, notifier)
}

func (f *branchFake) LastMessageBefore(_ context.Context, organizationID, channelID kernel.ID, seq int64) (int64, error) {
	f.scope(organizationID, channelID)
	if seq != 42 {
		f.t.Fatal("wrong notice sequence")
	}
	return 39, f.step("previous message")
}

func (f *branchFake) Merge(_ context.Context, organizationID, channelID, memberID kernel.ID, joined, lo, hi int64) error {
	f.scope(organizationID, channelID)
	if memberID != (kernel.ID{2}) || joined != 11 || lo != 40 || hi != 43 {
		f.t.Fatal("wrong notice read bounds or member")
	}
	return f.step("notice read")
}
