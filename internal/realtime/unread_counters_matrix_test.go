package realtime_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// Production's composition is package-local to cmd/ribbitto/read_adapters.go.
// These adapters bind the same exported writers, including preparation reuse
// for a topic post; they never synthesize persisted messages or read state.
func counterFeedWriter() *unread.FeedWriter {
	return unreadpg.NewFeedWriter(
		func(tx platform.Tx) unread.ChannelMessages { return conversationpg.MessageSequencesIn(tx) },
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) },
	)
}

func counterTopicWriter(prepared *unread.PreparedTopic) *unread.TopicWriter {
	return unread.NewTopicWriter(
		func(tx platform.Tx) func(context.Context, unread.TopicScope, int64) (unread.PreparedTopic, error) {
			return func(ctx context.Context, scope unread.TopicScope, joined int64) (unread.PreparedTopic, error) {
				p, err := unreadpg.WriterIn(tx).Prepare(ctx, scope, joined)
				if prepared != nil {
					*prepared = p
				}
				return p, err
			}
		},
		func(tx platform.Tx) unread.TopicMessages {
			return counterTopicMessages{conversationpg.TopicReadCandidatesIn(tx)}
		},
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) },
	)
}

type counterTopicMessages struct {
	candidates conversation.TopicReadCandidates
}

func (m counterTopicMessages) Ranges(ctx context.Context, organization, channel, topic kernel.ID, cursor, prefix, floor int64, read []unread.Range) ([]unread.Range, error) {
	set := make([]conversation.SequenceRange, len(read))
	for i, r := range read {
		set[i] = conversation.SequenceRange{Lo: r.Lo, Hi: r.Hi}
	}
	bounds, err := m.candidates.Ranges(ctx, organization, channel, topic, cursor, prefix, floor, set)
	if err != nil {
		return nil, err
	}
	ranges := make([]unread.Range, len(bounds))
	for i, r := range bounds {
		ranges[i] = unread.Range{Lo: r.Lo, Hi: r.Hi}
	}
	return ranges, nil
}

type counterPostReads struct{ tx platform.Tx }

func (w counterPostReads) Read(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, cursor *int64, lo, hi, floor int64) error {
	scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}
	joined := m.Member.JoinedEventSeq
	if cursor != nil && topic != nil {
		var prepared unread.PreparedTopic
		if _, err := counterTopicWriter(&prepared).Read(ctx, w.tx, unread.TopicScope{Scope: scope, TopicID: *topic}, joined, *cursor); err != nil {
			return err
		}
		if prepared == nil {
			return fmt.Errorf("topic post returned without preparation")
		}
		return prepared.Add(ctx, []int64{lo}, []int64{hi}, floor)
	}
	if cursor != nil {
		if _, err := counterFeedWriter().Read(ctx, w.tx, scope, joined, *cursor); err != nil {
			return err
		}
	}
	return unreadpg.WriterIn(w.tx).Merge(ctx, scope, joined, unread.Range{Lo: lo, Hi: hi})
}

type counterMatrix struct {
	pool          *pgxpool.Pool
	posting       *conversation.Posting
	reading       *unread.Reading
	signup        *org.SignUp
	members       []org.Membership
	channel, base kernel.ID
	selected      kernel.ID
	extra         []counterView
	posted        map[int64]kernel.ID
	prototype     counterPrototype
	truth         counterTruth
	truthByOrg    map[kernel.ID]counterTruth
	models        map[kernel.ID]sidebarOracleModel
}

type counterView struct {
	member            org.Membership
	channel, selected kernel.ID
}

func newCounterMatrix(t *testing.T) *counterMatrix {
	t.Helper()
	pool := pgtest.New(t)
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	accounts := func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }
	events := func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) }
	setup := orgpg.NewSetup(pool, hasher, "test", accounts, events,
		func(tx platform.Tx) org.DefaultChannelCreator { return conversationpg.DefaultChannelCreatorIn(tx) })
	result, err := setup.Complete(t.Context(), "test", org.SetupInput{OrganizationName: "Counters", Slug: "counters", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: result.AccountID}, "counters")
	if err != nil {
		t.Fatal(err)
	}
	channel, err := conversationpg.NewChannels(pool, 100).Default(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	conversationtest.Channel(t, pool, result.OrganizationID, "other", false)
	h := &counterMatrix{pool: pool, members: []org.Membership{owner}, channel: channel.ID, base: channel.DefaultTopicID,
		selected: channel.DefaultTopicID, posted: map[int64]kernel.ID{},
		truthByOrg: map[kernel.ID]counterTruth{},
		prototype:  counterPrototype{totals: map[kernel.ID]map[kernel.ID]int64{}, bundles: map[counterKey]*counterBundle{}, suspended: map[kernel.ID]bool{}},
		models:     map[kernel.ID]sidebarOracleModel{}}
	h.posting = conversationpg.NewPagePosting(pool, postingSequence, postingEvents,
		func(tx platform.Tx) conversation.PostReadWriter { return counterPostReads{tx} }, nil)
	h.reading = unreadpg.NewReading(pool, counterFeedWriter(), counterTopicWriter(nil), nil)
	h.signup = orgpg.NewSignUp(pool, hasher, true, accounts, events)
	h.check(t, "setup")
	h.prototype.materialize(h.truth, counterKey{owner.Member.ID, h.channel})
	return h
}

func (h *counterMatrix) check(t *testing.T, name string) {
	t.Helper()
	if !t.Run(name, func(t *testing.T) {
		tx, err := h.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		h.truth = counterLoad(t, tx, h.members[0].Organization.ID)
		h.truthByOrg[h.members[0].Organization.ID] = h.truth
		if len(h.prototype.totals) == 0 {
			h.prototype.totals[h.channel] = counterRecount(h.truth, h.channel, kernel.ID{}, false)
		}
		views := append([]counterView(nil), h.extra...)
		for _, m := range h.members {
			views = append(views, counterView{m, h.channel, h.selected})
		}
		for _, v := range views {
			m, truth := v.member, h.truth
			if m.Organization.ID != h.members[0].Organization.ID {
				truth = counterLoad(t, tx, m.Organization.ID)
			}
			h.truthByOrg[m.Organization.ID] = truth
			got := h.prototype.model(t, tx, truth, m.Organization.ID, m.Member.ID, v.channel, &v.selected)
			want, err := sidebarOracleIn(t.Context(), tx, m.Organization.ID, m.Member.ID, v.channel, &v.selected)
			if err != nil {
				t.Fatal(err)
			}
			if err := sidebarOracleCompare(got, want); err != nil {
				t.Fatalf("production-writer full model differs from oracle: %v", err)
			}
			h.models[m.Member.ID] = got
		}
	}) {
		t.FailNow() // Later operations must not mask a failed boundary.
	}
}

func (h *counterMatrix) join(t *testing.T, handle string) org.Membership {
	t.Helper()
	account, err := h.signup.SignUp(t.Context(), handle, handle, handle+"@example.org", "long enough password")
	if err != nil {
		t.Fatal(err)
	}
	m, err := orgpg.NewAuthorizer(h.pool).Member(t.Context(), &identity.Account{ID: account}, "counters")
	if err != nil {
		t.Fatal(err)
	}
	h.members = append(h.members, m)
	h.check(t, "join-"+handle)
	for _, r := range h.truth.ranges {
		if r.Member == m.Member.ID {
			t.Fatal("join must have no read rows")
		}
	}
	return m
}

func (h *counterMatrix) post(t *testing.T, name string, m org.Membership, topic *kernel.ID, cursor *int64) int64 {
	t.Helper()
	return h.postIn(t, name, m, h.channel, topic, cursor)
}

func (h *counterMatrix) postIn(t *testing.T, name string, m org.Membership, channel kernel.ID, topic *kernel.ID, cursor *int64) int64 {
	t.Helper()
	var message conversation.Message
	var err error
	if cursor == nil {
		message, err = h.posting.PostToTopic(t.Context(), m, channel, topic, "older page")
	} else {
		message, err = h.posting.PostFromPage(t.Context(), m, channel, topic, "latest page", *cursor)
	}
	if err != nil {
		t.Fatal(err)
	}
	var scope kernel.ID
	var through int64
	if topic != nil {
		scope = *topic
	}
	if cursor != nil {
		through = *cursor
	}
	// Maintenance uses the pre-write snapshot and the caller's arguments/result,
	// never a post-write recount or any value returned by the oracle.
	h.prototype.post(h.truthByOrg[m.Organization.ID], counterMessage{channel: channel, topic: message.TopicID, seq: message.EventSeq}, m.Member.ID, scope, through)
	if m.Organization.ID == h.members[0].Organization.ID {
		h.posted[message.EventSeq] = message.ID
	}
	h.check(t, name)
	return message.EventSeq
}

func (h *counterMatrix) read(t *testing.T, tab string, m org.Membership, topic *kernel.ID, cursor int64) {
	t.Helper()
	scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: h.channel, MemberID: m.Member.ID}
	var target kernel.ID
	var err error
	if topic == nil {
		err = h.reading.Feed(t.Context(), scope, m.Member.JoinedEventSeq, cursor)
	} else {
		target = *topic
		err = h.reading.Topic(t.Context(), unread.TopicScope{Scope: scope, TopicID: target}, m.Member.JoinedEventSeq, cursor)
	}
	if err != nil {
		t.Fatal(err)
	}
	h.prototype.advance(h.truth, counterKey{m.Member.ID, h.channel}, target, cursor)
	h.check(t, tab)
}

func (h *counterMatrix) expect(t *testing.T, m org.Membership, raw int64) {
	t.Helper()
	selection := h.models[m.Member.ID].selection
	if len(selection) != 1 {
		t.Fatal("selected topic missing")
	}
	if selection[0].count != sidebarOracleDisplay(raw) {
		t.Fatalf("boundary count: got %+v; want raw=%d", selection[0].count, raw)
	}
}

func (h *counterMatrix) floor(t *testing.T, m org.Membership, topic kernel.ID, want int64) {
	t.Helper()
	var floor int64
	if err := h.pool.QueryRow(t.Context(), `SELECT floor_seq FROM topic_read_floor WHERE organization_id=$1 AND member_id=$2 AND topic_id=$3`, m.Organization.ID, m.Member.ID, topic).Scan(&floor); err != nil {
		t.Fatal(err)
	}
	if floor != want {
		t.Fatalf("topic floor: got %d; want %d", floor, want)
	}
}

func TestUnreadCountersSequential(t *testing.T) {
	h := newCounterMatrix(t)
	owner := h.members[0]
	writer := h.join(t, "writer") // No counter bundle for this member throughout.
	topic := conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, "Topic").ID
	var sequences []int64
	for i := 0; i < 6; i++ {
		var target *kernel.ID
		if i%2 == 1 {
			target = &topic
		}
		sequences = append(sequences, h.post(t, fmt.Sprintf("fragment-%d", i), writer, target, nil))
	}
	h.read(t, "tab-A-topic-fragments", owner, &topic, sequences[3])
	fragments := 0
	for _, r := range h.truth.ranges {
		if r.Member == owner.Member.ID && r.Channel == h.channel {
			fragments++
		}
	}
	if fragments < 3 {
		t.Fatal("topic read must leave fragmented ranges around unread messages")
	}
	h.read(t, "tab-B-older-topic-overlap", owner, &topic, sequences[1])
	h.post(t, "feed-post-with-shown-cursor", owner, nil, &sequences[0])
	h.read(t, "tab-C-feed-overlap", owner, nil, sequences[2])
	h.post(t, "cursorless-older-topic-page-own-message", owner, &topic, nil)
	h.floor(t, owner, topic, sequences[3])
	h.post(t, "topic-post-with-shown-cursor", owner, &topic, &sequences[5])
	h.read(t, "tab-A-topic-newer", owner, &topic, sequences[5])
	h.read(t, "tab-B-topic-repeated", owner, &topic, sequences[5])
	older := h.post(t, "cursorless-older-feed-page", writer, nil, nil)
	for i := 0; i < 97; i++ {
		h.post(t, fmt.Sprintf("toward-cap-%d", i), writer, nil, nil)
	}
	h.expect(t, owner, 99)
	h.post(t, "cap-up-to-100", writer, nil, nil)
	h.expect(t, owner, 100)
	latest := h.post(t, "cap-up-to-101", writer, nil, nil)
	h.expect(t, owner, 101)
	h.read(t, "tab-C-cap-down-to-100", owner, nil, sequences[4])
	h.expect(t, owner, 100)
	h.read(t, "tab-D-cap-down-to-99", owner, nil, older)
	h.expect(t, owner, 99)
	h.read(t, "tab-A-topic-to-zero", owner, &h.base, latest)
	h.expect(t, owner, 0)
	h.read(t, "tab-B-topic-floor-only", owner, &topic, latest)
	h.floor(t, owner, topic, latest)
	h.read(t, "tab-B-topic-no-op", owner, &topic, latest)
	h.read(t, "tab-C-feed-after-topics", owner, nil, latest)
	h.read(t, "tab-D-older-feed-no-op", owner, nil, sequences[0])
	joined := h.join(t, "late")
	h.expect(t, joined, 0)
	h.post(t, "post-after-late-join", writer, nil, nil)
	h.expect(t, owner, 1)
	h.expect(t, joined, 1)
	h.read(t, "missing-bundle-topic-read", writer, &topic, latest)
	if h.prototype.bundles[counterKey{writer.Member.ID, h.channel}] != nil {
		t.Fatal("writer must exercise missing-bundle fallback")
	}
}
