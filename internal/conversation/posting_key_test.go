package conversation_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type keyScope struct {
	organization, member kernel.ID
	key                  conversation.IdempotencyKey
}

// keyedState is what keyedStore commits: sequences, messages by key scope
// (the unique index), unkeyed messages and appended events.
type keyedState struct {
	seq      map[kernel.ID]int64
	keyed    map[keyScope]conversation.Message
	unkeyed  int
	events   int
	inserted int
}

// keyedStore is posting's store with the message index's key rule and
// transaction rollback. Methods posting does not call stay nil and panic.
type keyedStore struct {
	conversation.Writer
	state, tx keyedState
	lookups   []keyScope
}

func (s *keyedStore) InTx(_ context.Context, fn func(platform.Tx) error) error {
	s.tx = keyedState{seq: maps.Clone(s.state.seq), keyed: maps.Clone(s.state.keyed), unkeyed: s.state.unkeyed, events: s.state.events, inserted: s.state.inserted}
	if err := fn(platform.Tx{}); err != nil {
		return err
	}
	s.state = s.tx
	return nil
}
func (s *keyedStore) NextEventSeq(_ context.Context, organizationID kernel.ID) (int64, error) {
	s.tx.seq[organizationID]++
	return s.tx.seq[organizationID], nil
}
func (s *keyedStore) GetDefaultTopic(_ context.Context, _, channelID kernel.ID) (conversation.Topic, error) {
	return conversation.Topic{ID: kernel.ID{channelID[0] + 0x60}, ChannelID: channelID, IsDefault: true}, nil
}
func (s *keyedStore) GetTopic(_ context.Context, _, channelID, id kernel.ID) (conversation.Topic, error) {
	return conversation.Topic{ID: id, ChannelID: channelID}, nil
}
func (s *keyedStore) QuotedMessage(context.Context, kernel.ID, kernel.ID, kernel.ID) (conversation.Message, error) {
	panic("unexpected QuotedMessage")
}
func (s *keyedStore) InsertMessage(_ context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, seq int64, key *conversation.IdempotencyKey, proof conversation.TypedBodyProof, quote *conversation.Quote) (conversation.Message, error) {
	s.tx.inserted++
	m := conversation.Message{ID: kernel.ID{byte(s.tx.inserted)}, OrganizationID: organizationID, ChannelID: channelID, TopicID: topicID, MemberID: memberID, Body: body, Format: conversation.FormatChatV1, EventSeq: seq, Quote: quote}
	if key == nil {
		s.tx.unkeyed++
		return m, nil
	}
	m.TypedBodyProof = proof
	scope := keyScope{organizationID, memberID, *key}
	if _, taken := s.tx.keyed[scope]; taken {
		return conversation.Message{}, conversation.ErrPostKeyTaken
	}
	s.tx.keyed[scope] = m
	return m, nil
}
func (s *keyedStore) KeyedMessage(_ context.Context, organizationID, memberID kernel.ID, key conversation.IdempotencyKey) (conversation.Message, error) {
	scope := keyScope{organizationID, memberID, key}
	s.lookups = append(s.lookups, scope)
	m, ok := s.tx.keyed[scope]
	if !ok {
		return conversation.Message{}, conversation.ErrMessageNotFound
	}
	return m, nil
}
func (s *keyedStore) Append(context.Context, kernel.ID, int64, realtime.EventKind, *kernel.ID, []byte) error {
	s.tx.events++
	return nil
}

func TestPostingIdempotencyKey(t *testing.T) {
	acme, globex := kernel.ID{1}, kernel.ID{9}
	alice := org.Membership{Organization: org.Organization{ID: acme}, Member: org.Member{ID: kernel.ID{2}}}
	bob := org.Membership{Organization: org.Organization{ID: acme}, Member: org.Member{ID: kernel.ID{5}}}
	// The same member ID in another organisation: the scope is the pair.
	foreign := org.Membership{Organization: org.Organization{ID: globex}, Member: org.Member{ID: kernel.ID{2}}}
	feed, other := kernel.ID{3}, kernel.ID{8}
	topic := kernel.ID{4}
	for _, tt := range []struct {
		name          string
		unkeyed       bool
		second        org.Membership
		channel       kernel.ID
		topic         *kernel.ID
		body          string
		moved         bool
		wantRepeat    bool
		wantErr       error
		wantMessages  int
		wantRequested kernel.ID
	}{
		{name: "repeat", second: alice, channel: feed, body: "hello", wantRepeat: true, wantMessages: 1, wantRequested: kernel.ID{0x63}},
		{name: "repeat, normalised body", second: alice, channel: feed, body: " hello\r\n", wantRepeat: true, wantMessages: 1, wantRequested: kernel.ID{0x63}},
		{name: "repeat after a move", second: alice, channel: feed, body: "hello", moved: true, wantRepeat: true, wantMessages: 1, wantRequested: kernel.ID{0x63}},
		{name: "repeat from a topic", second: alice, channel: feed, topic: &topic, body: "hello", wantRepeat: true, wantMessages: 1, wantRequested: topic},
		{name: "same length different text", second: alice, channel: feed, body: "world", wantRepeat: true, wantErr: conversation.ErrPostKeyReused, wantMessages: 1, wantRequested: kernel.ID{0x63}},
		{name: "case differs", second: alice, channel: feed, body: "Hello", wantRepeat: true, wantErr: conversation.ErrPostKeyReused, wantMessages: 1, wantRequested: kernel.ID{0x63}},
		{name: "prefix only", second: alice, channel: feed, body: "hello more", wantRepeat: true, wantErr: conversation.ErrPostKeyReused, wantMessages: 1, wantRequested: kernel.ID{0x63}},
		{name: "another body", second: alice, channel: feed, body: "changed", wantRepeat: true, wantErr: conversation.ErrPostKeyReused, wantMessages: 1, wantRequested: kernel.ID{0x63}},
		{name: "another channel", second: alice, channel: other, body: "hello", wantRepeat: true, wantErr: conversation.ErrPostKeyReused, wantMessages: 1, wantRequested: kernel.ID{0x68}},
		{name: "another member", second: bob, channel: feed, body: "hello", wantMessages: 2, wantRequested: kernel.ID{0x63}},
		{name: "another organisation", second: foreign, channel: feed, body: "hello", wantMessages: 2, wantRequested: kernel.ID{0x63}},
		{name: "no key", unkeyed: true, second: alice, channel: feed, body: "hello", wantMessages: 2, wantRequested: kernel.ID{0x63}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &keyedStore{state: keyedState{seq: map[kernel.ID]int64{acme: 1, globex: 1}, keyed: map[keyScope]conversation.Message{}}}
			p := conversation.NewPosting(s,
				func(platform.Tx) conversation.Writer { return s },
				func(platform.Tx) conversation.EventSequence { return s },
				func(platform.Tx) conversation.EventAppender { return s }, nil)
			key := conversation.NewIdempotencyKey()
			var sendKey *conversation.IdempotencyKey
			if !tt.unkeyed {
				sendKey = &key
			}
			first, err := p.Send(t.Context(), alice, feed, conversation.Send{Body: " hello\r\n", Key: sendKey})
			if err != nil || first.Repeat {
				t.Fatalf("first send: %+v, %v", first, err)
			}
			stored := first.Message
			if tt.moved {
				stored.TopicID = kernel.ID{0x77}
				s.state.keyed[keyScope{acme, alice.Member.ID, key}] = stored
			}
			before := s.state
			second, err := p.Send(t.Context(), tt.second, tt.channel, conversation.Send{Topic: tt.topic, Body: tt.body, Key: sendKey})
			if !errors.Is(err, tt.wantErr) || second.Repeat != tt.wantRepeat || second.Topic != tt.wantRequested {
				t.Fatalf("second send: %+v, %v; want repeat %t, error %v, requested %v", second, err, tt.wantRepeat, tt.wantErr, tt.wantRequested)
			}
			if tt.wantRepeat {
				// Nothing written: no message, no sequence, no event.
				if second.Message != stored || s.state.seq[tt.second.Organization.ID] != before.seq[tt.second.Organization.ID] || s.state.events != before.events {
					t.Fatalf("repeat wrote or returned another message: %+v", second.Message)
				}
			} else if second.Message.ID == first.Message.ID || second.Message.MemberID != tt.second.Member.ID || second.Message.OrganizationID != tt.second.Organization.ID {
				t.Fatalf("another author got the first message: %+v", second.Message)
			}
			if got := len(s.state.keyed) + s.state.unkeyed; got != tt.wantMessages || s.state.events != tt.wantMessages {
				t.Fatalf("messages %d, events %d; want %d", got, s.state.events, tt.wantMessages)
			}
			// The stored message is looked up only by the session's organisation and author.
			for _, scope := range s.lookups {
				if scope.organization != tt.second.Organization.ID || scope.member != tt.second.Member.ID {
					t.Fatalf("lookup outside the session's scope: %+v", s.lookups)
				}
			}
			if tt.wantRepeat != (len(s.lookups) == 1) || slices.ContainsFunc(s.lookups, func(k keyScope) bool { return k.key != key }) {
				t.Fatalf("lookups %+v", s.lookups)
			}
		})
	}
}

func TestTypedReplayBeforeExpansion(t *testing.T) {
	s := &keyedStore{state: keyedState{seq: map[kernel.ID]int64{{1}: 1}, keyed: map[keyScope]conversation.Message{}}}
	d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{}}
	p := conversation.NewPagePosting(s,
		func(platform.Tx) conversation.Writer { return s },
		func(platform.Tx) conversation.EventSequence { return s },
		func(platform.Tx) conversation.EventAppender { return s },
		nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	key := conversation.NewIdempotencyKey()
	topic := kernel.ID{4}
	body := "@tomoya " + strings.Repeat("界", conversation.MaxMessageBodyLength-8)
	first, err := p.Send(t.Context(), m, kernel.ID{3}, conversation.Send{Body: body, Topic: &topic, Key: &key})
	if err != nil {
		t.Fatal(err)
	}
	// A repeat stays in the requested topic even after branching moved it.
	first.Message.TopicID = kernel.ID{5}
	s.state.keyed[keyScope{m.Organization.ID, m.Member.ID, key}] = first.Message
	// A formerly literal token now resolves and would exceed the stored limit.
	d.members[m.Organization.ID] = []kernel.ID{{0xa1}}
	again, err := p.Send(t.Context(), m, kernel.ID{3}, conversation.Send{Body: body, Topic: &topic, Key: &key})
	if err != nil || !again.Repeat || again.Message != first.Message || again.Topic != topic || d.lists != 1 {
		t.Fatalf("retry resolved again: %+v, %v, lists %d", again, err, d.lists)
	}
	if _, err := p.Send(t.Context(), m, kernel.ID{3}, conversation.Send{Body: body}); !errors.Is(err, conversation.ErrInvalidBody) {
		t.Fatalf("new post escaped expanded length check: %v", err)
	}
}

// Keyed ordinary sends must retain the insert-first hot path. These candidate
// cases also guard reuse of chat_v1's token rule, including its exclusions.
func TestPostingPreflightReads(t *testing.T) {
	for _, tc := range []struct {
		body  string
		keyed bool
		reads int
	}{
		{"hello", true, 0}, {"@tomoya", false, 0}, {"hi @tomoya", true, 1},
		{"@unknown", true, 1}, {"_hi @j_.doe_", true, 1}, {"@everyone @x", true, 0},
		{"`@tomoya`", true, 0}, {"\\@tomoya", true, 0},
		{"https://example.org/@tomoya a@tomoya.dev", true, 0},
	} {
		t.Run(tc.body+fmt.Sprint(tc.keyed), func(t *testing.T) {
			s := &keyedStore{state: keyedState{seq: map[kernel.ID]int64{}, keyed: map[keyScope]conversation.Message{}}}
			d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{}}
			p := conversation.NewPagePosting(s,
				func(platform.Tx) conversation.Writer { return s },
				func(platform.Tx) conversation.EventSequence { return s },
				func(platform.Tx) conversation.EventAppender { return s },
				nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
			m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
			var key *conversation.IdempotencyKey
			if tc.keyed {
				key = new(conversation.NewIdempotencyKey())
			}
			_, err := p.Send(t.Context(), m, kernel.ID{3}, conversation.Send{Body: tc.body, Key: key})
			if err != nil || len(s.lookups) != tc.reads {
				t.Fatalf("preflight reads %d, error %v; want %d", len(s.lookups), err, tc.reads)
			}
		})
	}
}

func TestTypedReplayScope(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		organization, author, channel byte
		repeat                        bool
		wantErr                       error
	}{
		{"same scope", 1, 2, 3, true, nil},
		{"organization alone differs", 9, 2, 3, false, nil},
		{"author alone differs", 1, 5, 3, false, nil},
		{"channel alone differs", 1, 2, 8, true, conversation.ErrPostKeyReused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &keyedStore{state: keyedState{seq: map[kernel.ID]int64{}, keyed: map[keyScope]conversation.Message{}}}
			d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{}}
			p := conversation.NewPagePosting(s,
				func(platform.Tx) conversation.Writer { return s },
				func(platform.Tx) conversation.EventSequence { return s },
				func(platform.Tx) conversation.EventAppender { return s },
				nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
			m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
			key := conversation.NewIdempotencyKey()
			first, err := p.Send(t.Context(), m, kernel.ID{3}, conversation.Send{Body: "@unknown", Key: &key})
			if err != nil {
				t.Fatal(err)
			}
			m.Organization.ID, m.Member.ID = kernel.ID{tc.organization}, kernel.ID{tc.author}
			got, err := p.Send(t.Context(), m, kernel.ID{tc.channel}, conversation.Send{Body: "@unknown", Key: &key})
			if !errors.Is(err, tc.wantErr) || got.Repeat != tc.repeat || (got.Message.ID == first.Message.ID) != tc.repeat {
				t.Fatalf("scoped replay %+v, %v", got, err)
			}
			want := []keyScope{{kernel.ID{1}, kernel.ID{2}, key}, {m.Organization.ID, m.Member.ID, key}}
			if !slices.Equal(s.lookups, want) {
				t.Fatalf("lookups %+v, want %+v", s.lookups, want)
			}
		})
	}
}

func TestTypedReplayRequiresDirectory(t *testing.T) {
	s := &keyedStore{state: keyedState{seq: map[kernel.ID]int64{}, keyed: map[keyScope]conversation.Message{}}}
	d := &referenceDirectory{t: t, members: map[kernel.ID][]kernel.ID{}}
	p := conversation.NewPagePosting(s,
		func(platform.Tx) conversation.Writer { return s },
		func(platform.Tx) conversation.EventSequence { return s },
		func(platform.Tx) conversation.EventAppender { return s },
		nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	send := conversation.Send{Body: "@unknown", Key: new(conversation.NewIdempotencyKey())}
	if _, err := p.Send(t.Context(), m, kernel.ID{3}, send); err != nil {
		t.Fatal(err)
	}
	p = conversation.NewPosting(s,
		func(platform.Tx) conversation.Writer { return s },
		func(platform.Tx) conversation.EventSequence { return s },
		func(platform.Tx) conversation.EventAppender { return s }, nil)
	got, err := p.Send(t.Context(), m, kernel.ID{3}, send)
	if err == nil || errors.Is(err, conversation.ErrInvalidBody) || got != (conversation.Sent{}) || len(s.lookups) != 1 {
		t.Fatalf("directory-less replay: %+v, error %v, lookups %v", got, err, s.lookups)
	}
}
