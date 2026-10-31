package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type postingFake struct {
	t                 *testing.T
	calls             []string
	fail              string
	err               error
	topic             kernel.ID
	stored            conversation.Message
	active, committed bool
}

func (f *postingFake) step(name string) error {
	f.calls = append(f.calls, name)
	if name == f.fail {
		return f.err
	}
	return nil
}
func (f *postingFake) InTx(_ context.Context, fn func(platform.Tx) error) error {
	if err := f.step("begin"); err != nil {
		return err
	}
	f.active = true
	err := fn(platform.Tx{})
	f.active = false
	if err != nil {
		return err
	}
	if err := f.step("commit"); err != nil {
		return err
	}
	f.committed = true
	return nil
}
func (f *postingFake) bound(tx platform.Tx) {
	f.t.Helper()
	if !f.active || tx != (platform.Tx{}) {
		f.t.Fatal("factory outside the runner's transaction")
	}
}
func (f *postingFake) scope(organizationID, channelID kernel.ID) {
	f.t.Helper()
	if !f.active || organizationID != (kernel.ID{1}) || channelID != (kernel.ID{3}) {
		f.t.Fatal("wrong scope or outside transaction")
	}
}
func (f *postingFake) NextEventSeq(_ context.Context, organizationID kernel.ID) (int64, error) {
	f.scope(organizationID, kernel.ID{3})
	return 7, f.step("sequence")
}
func (f *postingFake) GetDefaultTopic(_ context.Context, organizationID, channelID kernel.ID) (conversation.Topic, error) {
	f.scope(organizationID, channelID)
	return conversation.Topic{ID: kernel.ID{6}}, f.step("default")
}
func (f *postingFake) GetTopic(_ context.Context, organizationID, channelID, id kernel.ID) (conversation.Topic, error) {
	f.scope(organizationID, channelID)
	if id != f.topic {
		f.t.Fatal("wrong selected topic")
	}
	return conversation.Topic{ID: id}, f.step("selected")
}
func (f *postingFake) InsertMessage(_ context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, seq int64, _ *conversation.IdempotencyKey, _ conversation.TypedBodyProof, _ *conversation.Quote) (conversation.Message, error) {
	f.scope(organizationID, channelID)
	if topicID != f.topic || memberID != (kernel.ID{2}) || seq != 7 {
		f.t.Fatal("wrong insert arguments")
	}
	return conversation.Message{ID: kernel.ID{5}, OrganizationID: organizationID, ChannelID: channelID, TopicID: topicID, MemberID: memberID, Body: body, EventSeq: seq}, f.step("insert")
}
func (f *postingFake) KeyedMessage(_ context.Context, organizationID, memberID kernel.ID, _ conversation.IdempotencyKey) (conversation.Message, error) {
	f.scope(organizationID, f.stored.ChannelID)
	if f.stored.ID == (kernel.ID{}) || memberID != f.stored.MemberID {
		f.t.Fatal("unexpected KeyedMessage or wrong author")
	}
	return f.stored, f.step("keyed")
}
func (f *postingFake) Append(_ context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error {
	f.scope(organizationID, kernel.ID{3})
	posted, err := conversation.DecodePosted(payload)
	if err != nil || posted.AuthorID == nil || *posted.AuthorID != (kernel.ID{2}) || posted.ChannelID != (kernel.ID{3}) || posted.MessageID != (kernel.ID{5}) || posted.TopicID == nil || *posted.TopicID != f.topic || seq != 7 || kind != conversation.KindPosted || audience != nil {
		f.t.Fatalf("wrong event: %+v, %v", posted, err)
	}
	return f.step("append")
}
func (f *postingFake) Raise(organizationID kernel.ID, seq int64) {
	if !f.committed || f.active || organizationID != (kernel.ID{1}) || seq != 7 {
		f.t.Fatal("notification before commit or wrong arguments")
	}
	_ = f.step("raise")
}
func (f *postingFake) CreateTopic(context.Context, kernel.ID, kernel.ID, string) (conversation.Topic, error) {
	panic("unexpected CreateTopic")
}
func (f *postingFake) QuotedMessage(context.Context, kernel.ID, kernel.ID, kernel.ID) (conversation.Message, error) {
	panic("unexpected QuotedMessage")
}
func (f *postingFake) InsertNotice(context.Context, kernel.ID, kernel.ID, kernel.ID, kernel.ID, string, int64) (conversation.Message, error) {
	panic("unexpected InsertNotice")
}
func (f *postingFake) MoveMessages(context.Context, kernel.ID, kernel.ID, kernel.ID, kernel.ID, []kernel.ID, int64) (int64, error) {
	panic("unexpected MoveMessages")
}

func (f *postingFake) posting(notifier conversation.Notifier) *conversation.Posting {
	return conversation.NewPosting(f,
		func(tx platform.Tx) conversation.Writer { f.bound(tx); return f },
		func(tx platform.Tx) conversation.EventSequence { f.bound(tx); return f },
		func(tx platform.Tx) conversation.EventAppender { f.bound(tx); return f }, notifier)
}

func TestPosting(t *testing.T) {
	failure := errors.New("statement or commit failed")
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}, OrganizationID: kernel.ID{1}}}
	for _, tt := range []struct {
		name, body, wantBody, fail string
		err, wantErr               error
	}{
		{name: "normalised", body: "  hello\r\nworld\t", wantBody: "hello\nworld"},
		{name: "empty", body: " \n ", wantErr: conversation.ErrInvalidBody},
		{name: "too long", body: strings.Repeat("界", 4001), wantErr: conversation.ErrInvalidBody},
		{name: "control character", body: "a\x00b", wantErr: conversation.ErrInvalidBody},
		{name: "foreign channel or missing default", body: "hi", fail: "default", err: conversation.ErrTopicNotFound, wantErr: conversation.ErrChannelNotFound},
		{name: "channel foreign key", body: "hi", fail: "insert", err: conversation.ErrChannelNotFound, wantErr: conversation.ErrChannelNotFound},
		{name: "member foreign key", body: "hi", fail: "insert", err: org.ErrNotFound, wantErr: org.ErrNotFound},
		{name: "unknown organisation", body: "hi", fail: "sequence", err: org.ErrNotFound, wantErr: org.ErrNotFound},
		{name: "topic outside channel", body: "hi", fail: "selected", err: conversation.ErrTopicNotFound, wantErr: conversation.ErrTopicNotFound},
		{name: "begin failure", body: "hi", fail: "begin", err: failure, wantErr: failure},
		{name: "sequence failure", body: "hi", fail: "sequence", err: failure, wantErr: failure},
		{name: "default failure", body: "hi", fail: "default", err: failure, wantErr: failure},
		{name: "selected failure", body: "hi", fail: "selected", err: failure, wantErr: failure},
		{name: "insert failure", body: "hi", fail: "insert", err: failure, wantErr: failure},
		{name: "read failure", body: "hi", fail: "read", err: failure, wantErr: failure},
		{name: "previous message failure", body: "hi", fail: "previous", err: failure, wantErr: failure},
		{name: "append failure", body: "hi", fail: "append", err: failure, wantErr: failure},
		{name: "commit failure", body: "hi", fail: "commit", err: failure, wantErr: failure},
	} {
		for _, mode := range []string{"Post", "PostToTopic", "PostToTopic default"} {
			if tt.fail == "selected" && mode != "PostToTopic" {
				continue
			}
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				f := &postingFake{t: t, fail: tt.fail, err: tt.err, topic: kernel.ID{6}}
				order := []string{"begin", "sequence", "default", "insert", "previous", "read", "append", "commit", "raise"}
				var topicID *kernel.ID
				if mode == "PostToTopic" {
					f.topic = kernel.ID{4}
					topicID = &f.topic
					order = []string{"begin", "sequence", "default", "selected", "insert", "previous", "read", "append", "commit", "raise"}
				}
				// Under #1486 (decision 32, amended), every posting mode passes only the own-message bounds.
				p := conversation.NewPagePosting(f, func(tx platform.Tx) conversation.Writer { f.bound(tx); return f },
					func(tx platform.Tx) conversation.EventSequence { f.bound(tx); return f },
					func(tx platform.Tx) conversation.EventAppender { f.bound(tx); return f },
					func(tx platform.Tx) conversation.PostReadWriter { f.bound(tx); return f }, nil, nil, nil, f)
				var got conversation.Message
				var err error
				if mode == "Post" {
					got, err = p.Post(t.Context(), m, kernel.ID{3}, tt.body)
				} else {
					got, err = p.PostToTopic(t.Context(), m, kernel.ID{3}, topicID, tt.body)
				}
				if !errors.Is(err, tt.wantErr) || err == nil && (got.Body != tt.wantBody || got.TopicID != f.topic || got.EventSeq != 7) || err != nil && got != (conversation.Message{}) {
					t.Fatalf("post = %+v, %v; want body %q, error %v", got, err, tt.wantBody, tt.wantErr)
				}
				if errors.Is(tt.wantErr, conversation.ErrInvalidBody) {
					order = nil
				} else if tt.fail != "" {
					for i, step := range order {
						if step == tt.fail {
							order = order[:i+1]
							break
						}
					}
				}
				if !reflect.DeepEqual(f.calls, order) {
					t.Fatalf("calls = %v; want %v", f.calls, order)
				}
			})
		}
	}
}

func TestPostingRepeat(t *testing.T) {
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	f := &postingFake{t: t, topic: kernel.ID{6}, fail: "insert", err: conversation.ErrPostKeyTaken,
		stored: conversation.Message{ID: kernel.ID{5}, OrganizationID: m.Organization.ID, ChannelID: kernel.ID{3}, TopicID: kernel.ID{6}, MemberID: m.Member.ID, Body: "hello", EventSeq: 7}}
	p := conversation.NewPagePosting(f, func(tx platform.Tx) conversation.Writer { f.bound(tx); return f },
		func(tx platform.Tx) conversation.EventSequence { f.bound(tx); return f },
		func(tx platform.Tx) conversation.EventAppender { f.bound(tx); return f },
		func(tx platform.Tx) conversation.PostReadWriter { f.bound(tx); return f }, nil, nil, nil, f)
	got, err := p.Send(t.Context(), m, f.stored.ChannelID, conversation.Send{Body: "hello", Key: new(conversation.NewIdempotencyKey())})
	if err != nil || !got.Repeat || got.Message != f.stored || got.Topic != f.topic {
		t.Fatalf("repeat = %+v, %v", got, err)
	}
	if slices.Contains(f.calls, "read") {
		t.Fatalf("repeat called PostReadWriter.Read: %v", f.calls)
	}
	want := []string{"begin", "sequence", "default", "insert", "begin", "keyed", "commit"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("repeat calls = %v; want %v", f.calls, want)
	}
}

func TestPostingNilNotifier(t *testing.T) {
	f := &postingFake{t: t, topic: kernel.ID{6}}
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	if _, err := f.posting(nil).Post(t.Context(), m, kernel.ID{3}, "hello"); err != nil || !f.committed || f.calls[len(f.calls)-1] != "commit" {
		t.Fatalf("nil notifier: %v, calls %v", err, f.calls)
	}
}

func (f *postingFake) LastMessageBefore(context.Context, kernel.ID, kernel.ID, int64) (int64, error) {
	return 4, f.step("previous")
}

func (f *postingFake) Read(_ context.Context, m org.Membership, channel kernel.ID, lo, hi int64) error {
	f.scope(m.Organization.ID, channel)
	if m.Member.ID != (kernel.ID{2}) || lo != 5 || hi != 8 {
		f.t.Fatal("incorrect own-message bounds or member")
	}
	return f.step("read")
}
