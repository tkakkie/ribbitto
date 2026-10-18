package conversation_test

import (
	"context"
	"errors"
	"maps"
	"slices"
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
func (s *keyedStore) InsertMessage(_ context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, seq int64, key *conversation.IdempotencyKey, proof conversation.TypedBodyProof, _ *conversation.Quote) (conversation.Message, error) {
	s.tx.inserted++
	m := conversation.Message{ID: kernel.ID{byte(s.tx.inserted)}, OrganizationID: organizationID, ChannelID: channelID, TopicID: topicID, MemberID: memberID, Body: body, Format: conversation.FormatChatV1, EventSeq: seq}
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
