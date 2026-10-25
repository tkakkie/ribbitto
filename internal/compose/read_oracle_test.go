package compose_test

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"slices"
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

// Scope selects the page for reads and cursor compatibility for posts.
// Under #1486 (decision 32, amended), every post adds only the own message, whatever its page.
func readOracleKinds() []readOracleKind {
	return []readOracleKind{
		{"feed", "read", "feed", false},
		{"topic", "read", "topic", false},
		{"post without cursor", "post", "", false},
		{"repeat post", "post", "", false},
		{"post from feed", "post", "feed", false},
		{"post from topic", "post", "topic", false},
		{"move", "move", "", false},
		{"refused feed", "read", "feed", true},
		{"refused topic", "read", "topic", true},
		{"refused feed post", "post", "feed", true},
		{"refused topic post", "post", "topic", true},
		{"shown feed", "shown", "feed", false},
		{"refused shown feed", "shown", "feed", true},
		{"shown topic", "shown", "topic", false},
		{"refused shown topic", "shown", "topic", true},
	}
}

func readOracleKindIndex(t *testing.T, kinds []readOracleKind, name string) int {
	t.Helper()
	for i, kind := range kinds {
		if kind.name == name {
			return i
		}
	}
	t.Fatalf("unknown read oracle kind %q", name)
	return 0
}

type readOracleStep struct {
	kind, member, channel, topic int
	cursor                       int64
	from, through                int64
	messageIndex                 *int
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
	// Sequence 2 is a default-topic message. M's first topic-a read adds
	// [3,8), so only the join prefix can preserve the boundary message.
	const mJoined int64 = 2
	m := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "m@example.org", "M"), org.RoleMember, "reader", mJoined)
	feedFirst := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "f@example.org", "F"), org.RoleMember, "feed-reader", 4)
	postFirst := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "p@example.org", "P"), org.RoleMember, "poster", 6)
	for i, member := range []struct {
		id     kernel.ID
		joined int64
	}{{m, mJoined}, {f.MemberID, 1}, {feedFirst, 4}, {postFirst, 6}} {
		id, joined := member.id, member.joined
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

func readOracleModel(kind readOracleKind, before []readOracleMessage, set map[int64]bool, topic kernel.ID, cursor, own, from, through int64) {
	if kind.refused {
		return
	}
	for _, message := range before {
		if kind.action == "shown" && from <= message.seq && message.seq <= through && (kind.scope == "feed" || (message.topic == topic && message.moved <= cursor)) {
			set[message.seq] = true
		}
		if kind.action == "read" && message.seq <= cursor && (kind.scope == "feed" || (kind.scope == "topic" && message.topic == topic && message.moved <= cursor)) {
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
	case "shown":
		reading := unreadpg.NewReading(h.pool, compose.NewFeedWriter(), compose.NewTopicWriter(), nil)
		if kind.scope == "feed" {
			err = reading.FeedShown(t.Context(), scope, member.Member.JoinedEventSeq, step.cursor, step.from, step.through)
		} else {
			err = reading.TopicShown(t.Context(), unread.TopicScope{Scope: scope, TopicID: topic}, member.Member.JoinedEventSeq, step.cursor, step.from, step.through)
		}
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
		send := conversation.Send{Topic: selected, Cursor: cursor, Body: "own"}
		if kind.name == "repeat post" {
			send.Key = new(conversation.NewIdempotencyKey())
		}
		sent, err = posting.Send(t.Context(), member, scope.ChannelID, send)
		if err == nil && send.Key != nil {
			rows := readOracleRows(t, h.pool, nil, true)
			repeat, repeatErr := posting.Send(t.Context(), member, scope.ChannelID, send)
			feedRequire(t, repeatErr)
			if !repeat.Repeat || repeat.Message.ID != sent.Message.ID || !slices.Equal(rows, readOracleRows(t, h.pool, nil, true)) {
				t.Fatal("idempotent repeat changed persisted state")
			}
		}
		own = sent.Message.EventSeq
	case "move":
		// Moves are confined to the channel with two topics. Pick an actual
		// message, so repeated moves exercise both directions without conflicts.
		message := before.messages[rng.Intn(len(before.messages))]
		if step.messageIndex != nil {
			message = before.messages[*step.messageIndex]
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
	if kind.action == "post" && !slices.Equal(before.floors, readOracleLoad(t, h.pool, scope).floors) {
		t.Error("posting changed topic floors contrary to #1486 (decision 32, amended)")
	}
	readOracleModel(kind, before.messages, h.model[step.member][scope.ChannelID], topic, step.cursor, own, step.from, step.through)
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
			kind := func(name string) int { return readOracleKindIndex(t, kinds, name) }
			for _, violation := range h.check(t) {
				t.Fatal("initial state: " + violation)
			}
			for _, premise := range []readOracleStep{{member: 2, channel: 0, cursor: 4}, {member: 3, channel: 1, cursor: 6}} {
				scope := h.scope(premise)
				state := readOracleLoad(t, h.pool, scope)
				var persisted bool
				feedRequire(t, h.pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM channel_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3)", scope.OrganizationID, scope.ChannelID, scope.MemberID).Scan(&persisted))
				joined := h.members[premise.member].Member.JoinedEventSeq
				boundary := false
				for _, message := range state.messages {
					boundary = boundary || message.seq == premise.cursor
				}
				if persisted || len(state.ranges) != 0 || joined != premise.cursor || !boundary {
					t.Fatalf("first-write premise: member=%d channel=%d persisted=%t ranges=%v joined=%d boundary=%d present=%t", premise.member, premise.channel, persisted, state.ranges, joined, premise.cursor, boundary)
				}
			}
			// Guarantee the production mutation witnesses before randomized history:
			// join-boundary preservation, islands, a topic post with the current cursor
			// (661) after another member's post, then a topic post with an older cursor
			// (662) after a move, and topic reads on both sides of the move.
			// B's feed cursor 665 must cover M's newest post, still unread for B.
			// A later cursorless post is separated from its channel predecessor by
			// only another channel's message. F's feed-first and P's post-first writes
			// must preserve their boundary messages through MergeCovered's join prefix.
			movedMessage := 2 // Channel 0's third message has sequence 8.
			steps := []readOracleStep{
				{
					kind: kind("topic"), member: 0, channel: 0, topic: 1, cursor: 660,
				},
				{
					kind: kind("topic"), member: 1, channel: 0, topic: 0, cursor: 660,
				},
				{
					kind: kind("post without cursor"), member: 1, channel: 0, topic: 1,
				},
				{
					kind: kind("post from topic"), member: 0, channel: 0, topic: 1, cursor: 661,
				},
				{
					kind: kind("move"), member: 1, channel: 0, topic: 0, messageIndex: &movedMessage,
				},
				{
					kind: kind("post from topic"), member: 0, channel: 0, topic: 1, cursor: 662,
				},
				// Read B's post without reading moved-in 8 below floor 660,
				// then ensure the next run preserves that floor exception.
				{kind: kind("shown topic"), member: 0, topic: 1, cursor: 662, from: 661, through: 661},
				{kind: kind("shown topic"), member: 0, topic: 1, cursor: 665, from: 665, through: 665},
				{
					kind: kind("topic"), member: 0, channel: 0, topic: 1, cursor: 662,
				},
				{
					kind: kind("topic"), member: 0, channel: 0, topic: 1, cursor: 664,
				},
				{
					kind: kind("feed"), member: 1, channel: 0, topic: 0, cursor: 665,
				},
				{
					kind: kind("post without cursor"), member: 1, channel: 1, topic: 0,
				},
				{
					kind: kind("repeat post"), member: 0, channel: 0, topic: 0,
				},
				{
					kind: kind("feed"), member: 0, channel: 0, topic: 0, cursor: 667,
				},
				{
					kind: kind("post from feed"), member: 1, channel: 1, topic: 0, cursor: 667,
				},
			}
			for i, kind := range kinds {
				if !kind.refused {
					continue
				}
				for _, cursor := range []int64{669, -1, math.MaxInt64} {
					steps = append(steps, readOracleStep{kind: i, member: 0, channel: 0, topic: 1, cursor: cursor})
				}
			}
			steps = append(steps,
				readOracleStep{kind: kind("feed"), member: 2, channel: 0, topic: 0, cursor: 0},
				readOracleStep{kind: kind("post without cursor"), member: 3, channel: 1, topic: 0},
			)
			// The existing move of 8 into topic 1 is after cursor 662, before 668.
			steps = append(steps,
				readOracleStep{kind: kind("shown topic"), member: 2, topic: 1, cursor: 662, from: 7, through: 10},
				readOracleStep{kind: kind("shown topic"), member: 2, topic: 1, cursor: 668, from: 7, through: 10},
			)
			topicRuns := 0
			isolated := readOracleRows(t, h.pool, &h.other, true)
			for index := range 150 {
				var committed int64
				feedRequire(t, h.pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id=$1", h.members[0].Organization.ID).Scan(&committed))
				// Keep random members limited to M and B; F and P witness only first writes.
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
				if kinds[step.kind].action == "shown" && step.from == 0 {
					// Three of four topic draws keep T < S and F above the oldest;
					// the fourth draws both T = S and lo = 0 (F below every message).
					if !kinds[step.kind].refused {
						step.cursor = 9 + rng.Int63n(committed-8)
					}
					step.through = 8 + rng.Int63n(max(1, step.cursor-8))
					step.from = 7 + rng.Int63n(step.through-6)
					if kinds[step.kind].scope == "topic" && !kinds[step.kind].refused {
						topicRuns++
						if topicRuns%4 == 0 {
							step.from, step.through = 1, step.cursor
						}
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
