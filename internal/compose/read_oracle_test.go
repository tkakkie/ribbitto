package compose_test

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type readOracleKind struct {
	name, action, scope string
	refused             bool
}

// The scope column is today's page-scope posting rule. Later stages can add
// kinds here and change their model rule without copying the history harness.
func readOracleKinds() []readOracleKind {
	return []readOracleKind{
		{"feed", "read", "feed", false},
		{"topic", "read", "topic", false},
		{"post without cursor", "post", "", false},
		{"post from feed", "post", "feed", false},
		{"post from topic", "post", "topic", false},
		{"move", "move", "", false},
		{"refused feed", "read", "feed", true},
		{"refused topic", "read", "topic", true},
		{"refused feed post", "post", "feed", true},
		{"refused topic post", "post", "topic", true},
	}
}

type readOracleStep struct {
	kind, member, channel, topic int
	cursor                       int64
}

type readOracleFixture struct {
	pool     *pgxpool.Pool
	members  []org.Membership
	channels []kernel.ID
	topics   [][]kernel.ID
	other    kernel.ID
	model    []map[kernel.ID]map[int64]bool
}

func newReadOracle(t *testing.T) readOracleFixture {
	t.Helper()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "oracle", "general")
	second := conversationtest.Channel(t, pool, f.OrganizationID, "second", false)
	a := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "a")
	h := readOracleFixture{pool: pool, channels: []kernel.ID{f.Channel.ID, second.ID},
		topics: [][]kernel.ID{{f.Channel.DefaultTopicID, a.ID}, {second.DefaultTopicID}}}
	// Interleaved topics and channels also leave non-message event sequences
	// between channel messages; those make an incorrectly narrow own range visible.
	for i, topic := range []kernel.ID{f.Channel.DefaultTopicID, a.ID, second.DefaultTopicID} {
		channel := h.channels[i/2]
		_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
			SELECT $1,$2,$3,$4,'initial',n*6+$5 FROM generate_series(0,109) n`, f.OrganizationID, channel, topic, f.MemberID, int64(2+i*2))
		feedRequire(t, err)
	}
	_, err := pool.Exec(t.Context(), "UPDATE organization SET event_seq=660,event_log_boundary_seq=660 WHERE id=$1", f.OrganizationID)
	feedRequire(t, err)
	m := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "m@example.org", "M"), org.RoleMember, "reader", 5)
	for i, id := range []kernel.ID{m, f.MemberID} {
		joined := int64(5)
		if i == 1 {
			joined = 1
		}
		h.members = append(h.members, org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: id, JoinedEventSeq: joined}})
		h.model = append(h.model, make(map[kernel.ID]map[int64]bool))
		for _, channel := range h.channels {
			set := make(map[int64]bool)
			state := readOracleLoad(t, pool, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: channel, MemberID: id})
			for _, message := range state.messages {
				if message.seq <= joined {
					set[message.seq] = true
				}
			}
			h.model[i][channel] = set
		}
	}
	other := conversationtest.OrganizationWithOwner(t, pool, "isolated", "general")
	h.other = other.OrganizationID
	membership := org.Membership{Organization: org.Organization{ID: other.OrganizationID}, Member: org.Member{ID: other.MemberID, JoinedEventSeq: 1}}
	posting := conversationpg.NewPagePosting(pool, compose.PostingSequence, compose.PostingEvents, compose.PostReads, compose.LookupMembers, nil)
	for range 3 {
		_, err := posting.Post(t.Context(), membership, other.Channel.ID, "isolated")
		feedRequire(t, err)
	}
	return h
}

func (h readOracleFixture) scope(step readOracleStep) unread.Scope {
	return unread.Scope{OrganizationID: h.members[step.member].Organization.ID, ChannelID: h.channels[step.channel], MemberID: h.members[step.member].Member.ID}
}

func readOracleModel(kind readOracleKind, before []readOracleMessage, set map[int64]bool, topic kernel.ID, cursor, own int64) {
	if kind.refused {
		return
	}
	for _, message := range before {
		if message.seq <= cursor && (kind.scope == "feed" || (kind.scope == "topic" && message.topic == topic && message.moved <= cursor)) {
			set[message.seq] = true
		}
	}
	if own != 0 {
		set[own] = true
	}
}

func (h readOracleFixture) apply(t *testing.T, step readOracleStep, kind readOracleKind, rng *rand.Rand) {
	t.Helper()
	scope, member := h.scope(step), h.members[step.member]
	topic := h.topics[step.channel][step.topic]
	before := readOracleLoad(t, h.pool, scope)
	var own int64
	var err error
	switch kind.action {
	case "read":
		reading := unreadpg.NewReading(h.pool, compose.NewFeedWriter(), compose.NewTopicWriter(), nil)
		if kind.scope == "feed" {
			err = reading.Feed(t.Context(), scope, member.Member.JoinedEventSeq, step.cursor)
		} else {
			err = reading.Topic(t.Context(), unread.TopicScope{Scope: scope, TopicID: topic}, member.Member.JoinedEventSeq, step.cursor)
		}
	case "post":
		var cursor *int64
		if kind.scope != "" {
			cursor = &step.cursor
		}
		var selected *kernel.ID
		if kind.scope != "feed" {
			selected = &topic
		}
		posting := conversationpg.NewPagePosting(h.pool, compose.PostingSequence, compose.PostingEvents, compose.PostReads, compose.LookupMembers, nil)
		var sent conversation.Sent
		sent, err = posting.Send(t.Context(), member, scope.ChannelID, conversation.Send{Topic: selected, Cursor: cursor, Body: "own"})
		own = sent.Message.EventSeq
	case "move":
		// Moves are confined to the channel with two topics. Pick an actual
		// message, so repeated moves exercise both directions without conflicts.
		message := before.messages[rng.Intn(len(before.messages))]
		if step.cursor == -1 {
			message = before.messages[2] // unread 8 moves after the scripted cursor.
		}
		target := h.topics[0][0]
		if message.topic == target {
			target = h.topics[0][1]
		}
		_, err = conversationpg.NewBrancher(h.pool, compose.PostingSequence, compose.PostingEvents, compose.BranchReads, nil).Branch(t.Context(), member, scope.ChannelID,
			conversation.Branch{From: message.topic, To: &target, Messages: []kernel.ID{message.id}}, func(conversation.Topic) string { return "notice" })
		if err == nil {
			feedRequire(t, h.pool.QueryRow(t.Context(), "SELECT max(event_seq) FROM message WHERE organization_id=$1 AND channel_id=$2", scope.OrganizationID, scope.ChannelID).Scan(&own))
		}
	}
	if kind.refused {
		want := unread.ErrInvalidCursor
		if kind.action == "post" {
			want = conversation.ErrInvalidPostCursor
		}
		if !errors.Is(err, want) {
			t.Errorf("refusal error=%v, want %v", err, want)
		}
	} else {
		feedRequire(t, err)
	}
	readOracleModel(kind, before.messages, h.model[step.member][scope.ChannelID], topic, step.cursor, own)
}

func (h readOracleFixture) check(t *testing.T) []string {
	t.Helper()
	var violations []string
	reader := conversationpg.NewReader(h.pool, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, compose.PageCountsIn)
	for i, member := range h.members {
		states := make(map[kernel.ID]readOracleState)
		for _, channel := range h.channels {
			state := readOracleLoad(t, h.pool, unread.Scope{OrganizationID: member.Organization.ID, ChannelID: channel, MemberID: member.Member.ID})
			states[channel] = state
			violations = append(violations, readOracleCheckState(state, member.Member.JoinedEventSeq, h.model[i][channel])...)
		}
		for c, channel := range h.channels {
			// Both feed and every topic use Page, including the real sidebar lists.
			for selection := -1; selection < len(h.topics[c]); selection++ {
				var topic *kernel.ID
				if selection >= 0 {
					topic = &h.topics[c][selection]
				}
				page, err := reader.Page(t.Context(), member, channel, topic, nil)
				feedRequire(t, err)
				violations = append(violations, readOracleCheckCounts(page.PageCounts, states, channel, h.topics[c], topic, member.Member.JoinedEventSeq)...)
			}
		}
	}
	return violations
}

func TestReadOracle(t *testing.T) {
	const seed int64 = 15641486
	t.Logf("read oracle seed=%d", seed)
	for history := range 3 {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			t.Parallel()
			h := newReadOracle(t)
			rng := rand.New(rand.NewSource(seed + int64(history)))
			kinds := readOracleKinds()
			// Guarantee the production mutation witnesses before randomized history:
			// islands, a stale topic post, moves on both sides of S, newest feed,
			// then a cursorless post separated from its predecessor by another channel.
			steps := []readOracleStep{
				{1, 0, 0, 1, 660}, {1, 1, 0, 0, 660},
				{2, 1, 0, 1, 0}, {4, 0, 0, 1, 660},
				{5, 1, 0, 0, -1}, {1, 0, 0, 1, 662}, {1, 0, 0, 1, 664},
				{0, 0, 0, 0, 664}, {2, 1, 1, 0, 0}, {2, 0, 0, 0, 0},
				{0, 0, 0, 0, 666}, {3, 1, 1, 0, 665},
			}
			for kind := 6; kind < len(kinds); kind++ {
				for _, cursor := range []int64{668, -1, math.MaxInt64} {
					steps = append(steps, readOracleStep{kind, 0, 0, 1, cursor})
				}
			}
			isolated := readOracleRows(t, h.pool, &h.other, true)
			for index := range 150 {
				var committed int64
				feedRequire(t, h.pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id=$1", h.members[0].Organization.ID).Scan(&committed))
				step := readOracleStep{kind: rng.Intn(len(kinds)), member: rng.Intn(2), channel: rng.Intn(2), cursor: rng.Int63n(committed + 1)}
				if index < len(steps) {
					step = steps[index]
				} else {
					step.topic = rng.Intn(len(h.topics[step.channel]))
					if kinds[step.kind].action == "move" {
						step.channel, step.topic = 0, 0
					}
					if kinds[step.kind].refused {
						step.cursor = []int64{committed + 1, -1, math.MaxInt64}[rng.Intn(3)]
					}
				}
				if !t.Run(fmt.Sprintf("step%03d/%s", index, kinds[step.kind].name), func(t *testing.T) {
					t.Logf("seed=%d history=%d step=%d action=%+v", seed, history, index, step)
					var before []string
					if kinds[step.kind].refused {
						before = readOracleRows(t, h.pool, nil, false)
					}
					h.apply(t, step, kinds[step.kind], rng)
					if kinds[step.kind].refused {
						for _, violation := range readOracleCheckRows(before, readOracleRows(t, h.pool, nil, false), "refused step") {
							t.Error(violation)
						}
					}
					for _, violation := range readOracleCheckRows(isolated, readOracleRows(t, h.pool, &h.other, true), "second organisation") {
						t.Error(violation)
					}
					for _, violation := range h.check(t) {
						t.Error(violation)
					}
				}) {
					t.Fatalf("seed=%d history=%d failing step=%d", seed, history, index)
				}
			}
		})
	}
}
