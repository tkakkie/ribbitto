package realtime_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func TestHybridProductionDomain(t *testing.T) {
	pool := pgtest.New(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		must(err)
	}
	f := conversationtest.OrganizationWithOwner(t, pool, "domain", "general")
	account := identitytest.Account(t, pool, "reader@example.org", "Reader")
	member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 5)
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
	var first, selected kernel.ID
	for i := 1; i <= 50; i++ {
		id := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("Topic%02d", i)).ID
		if i == 1 {
			first = id
		}
		selected = id
	}
	// Persisted fixtures express domain effects, not hypothetical event fields.
	// The candidate receives only production envelopes and invalidations.
	exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1,$2,$3,$4,'fixture',s FROM generate_series(1,102) s`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	seq := int64(102)
	merge := func(ch, who kernel.ID, joined int64, ranges ...unread.Range) {
		t.Helper()
		must(platform.InTx(ctx, pool, func(tx platform.Tx) error {
			for _, r := range ranges {
				if err := unreadpg.WriterIn(tx).Merge(ctx, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: ch, MemberID: who}, joined, r); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	post := func(ch, topic, author kernel.ID) realtime.Event {
		t.Helper()
		seq++
		var id kernel.ID
		must(pool.QueryRow(ctx, `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
VALUES ($1,$2,$3,$4,'post',$5) RETURNING id`, f.OrganizationID, ch, topic, author, seq).Scan(&id))
		joined := int64(1)
		if author == member {
			joined = 5
		}
		merge(ch, author, joined, unread.Range{Lo: seq, Hi: seq + 1})
		// Synthetic messages have no log rows; retain only sequences after them.
		exec(`UPDATE organization SET event_seq=$2,event_log_boundary_seq=$2 WHERE id=$1`, f.OrganizationID, seq)
		return realtime.Event{OrganizationID: f.OrganizationID, Seq: seq, Kind: conversation.KindPosted, ChannelID: ch, Payload: conversation.EncodePosted(ch, id, topic)}
	}
	post(f.Channel.ID, selected, f.MemberID)
	post(f.Channel.ID, selected, f.MemberID)
	post(other.ID, other.DefaultTopicID, f.MemberID)
	post(other.ID, other.DefaultTopicID, f.MemberID)
	q := realtime.NewRecountScheduler(26, 100)
	defer q.Close()
	now := time.Unix(0, 0)
	var tabs []*hybridPrototype
	for _, view := range []struct {
		organization, member, channel kernel.ID
		selection                     *kernel.ID
	}{
		{f.OrganizationID, member, f.Channel.ID, &selected},
		{f.OrganizationID, member, f.Channel.ID, nil},
		{f.OrganizationID, f.MemberID, f.Channel.ID, &first},
		{foreign.OrganizationID, foreign.MemberID, foreign.Channel.ID, nil},
	} {
		p := newHybrid(q, now, 0, func(ctx context.Context) (sidebarOracleModel, error) {
			m := org.Membership{Organization: org.Organization{ID: view.organization}, Member: org.Member{ID: view.member}}
			return hybridSnapshot(ctx, pool, m, view.channel, view.selection)
		})
		defer p.registration.Cancel()
		must(p.reconcile("connect"))
		must(p.complete())
		tabs = append(tabs, p)
	}
	oracle := func(p *hybridPrototype) sidebarOracleModel {
		t.Helper()
		var selection *kernel.ID
		if len(p.state.selection) == 1 {
			selection = &p.state.selection[0].id
		}
		want, err := sidebarOracle(ctx, pool, p.state.organization, p.state.member, p.state.current, selection)
		must(err)
		return want
	}
	checkCounts := func(total, defaultTopic, selection int64) {
		t.Helper()
		m := tabs[0].state
		if m.channels[0].count != sidebarOracleDisplay(total) || m.topics[0].count != sidebarOracleDisplay(defaultTopic) || m.selection[0].count != sidebarOracleDisplay(selection) || len(m.topics) != 50 {
			t.Fatalf("domain witness: total/default/selection want %d/%d/%d; got %+v", total, defaultTopic, selection, m)
		}
	}
	// All writes finish before this checkpoint and resume only after every
	// mailbox completes. Computation gates prove invalidations do not mutate
	// the model early. Oracle values are never passed to the snapshot adapter.
	step := func(operation string, event *realtime.Event, read *hybridRead, reason string) {
		t.Helper()
		for i, p := range tabs {
			want, before := oracle(p), p.state
			before.channels, before.topics, before.selection = slices.Clone(before.channels), slices.Clone(before.topics), slices.Clone(before.selection)
			snapshot := p.snapshot
			release := make(chan struct{})
			p.snapshot = func(ctx context.Context) (sidebarOracleModel, error) {
				select {
				case <-release:
					return snapshot(ctx)
				case <-ctx.Done():
					return sidebarOracleModel{}, ctx.Err()
				}
			}
			err := p.handle(operation, event, read, p.state.organization, p.state.member)
			unchanged := sidebarOracleCompare(p.state, before)
			close(release)
			must(err)
			must(unchanged)
			r := p.records[len(p.records)-1]
			noop := (event != nil && event.OrganizationID != p.state.organization) || (read != nil && (read.organization != p.state.organization || read.member != p.state.member))
			mode, why := "reconciliation", reason
			if noop {
				mode, why = "delta-noop", "outside bound organisation or member"
			} else {
				must(p.complete())
			}
			if r.condition != "production-only" || r.operation != operation || r.mode != mode || r.reason != why || (r.request == 0) != noop {
				t.Fatalf("operation routing %s tab %d: %+v, want %s: %s", operation, i, r, mode, why)
			}
			t.Logf("tab=%d record=%+v", i, r)
			must(sidebarOracleCompare(p.state, want)) // Immediate for the exact no-op.
			p.snapshot = snapshot
		}
	}
	postStep := func(operation string, ch, topic, author kernel.ID) {
		t.Helper()
		e := post(ch, topic, author)
		step(operation, &e, nil, "post author and page read effects absent")
	}
	readStep := func(operation string, who kernel.ID) {
		t.Helper()
		step(operation, nil, &hybridRead{f.OrganizationID, who}, "read commit and exact coverage absent")
	}
	step("join-prefix-and-unlisted-selection", nil, nil, "unknown input or list metadata absent")
	checkCounts(99, 97, 2)
	if slices.ContainsFunc(tabs[0].state.topics, func(e sidebarOracleEntry) bool { return e.id == selected }) {
		t.Fatal("selection must start unlisted")
	}
	postStep("cap-up-to-100", f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	checkCounts(100, 98, 2)
	postStep("cap-above-100", f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	checkCounts(101, 99, 2)
	merge(f.Channel.ID, member, 5, unread.Range{Lo: 6, Hi: 8}, unread.Range{Lo: 10, Hi: 12})
	readStep("fragmented-read-cap-down", member)
	checkCounts(97, 95, 2)
	merge(f.Channel.ID, member, 5, unread.Range{Lo: 7, Hi: 11})
	readStep("overlapping-tab-read", member)
	checkCounts(95, 93, 2)
	merge(f.Channel.ID, member, 5, unread.Range{Lo: 6, Hi: 12})
	readStep("repeated-other-tab-read", member)
	checkCounts(95, 93, 2)
	merge(f.Channel.ID, member, 5, unread.Range{Lo: 103, Hi: 105})
	readStep("topic-through-latest-leaves-default-unread", member)
	checkCounts(93, 93, 0)
	postStep("older-page-own-post-only", f.Channel.ID, selected, member)
	checkCounts(93, 93, 0)
	merge(f.Channel.ID, member, 5, unread.Range{Lo: 0, Hi: seq + 1})
	postStep("feed-page-post-reads-scope", f.Channel.ID, f.Channel.DefaultTopicID, member)
	checkCounts(0, 0, 0)
	postStep("other-author-topic-post", f.Channel.ID, selected, f.MemberID)
	postStep("other-author-default-post", f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	merge(f.Channel.ID, member, 5, unread.Range{Lo: seq - 1, Hi: seq})
	postStep("topic-page-post-leaves-default-unread", f.Channel.ID, selected, member)
	checkCounts(1, 1, 0)
	postStep("other-channel-post", other.ID, other.DefaultTopicID, f.MemberID)
	merge(other.ID, member, 5, unread.Range{Lo: 0, Hi: seq + 1})
	readStep("other-channel-feed-clears-badge", member)
	if tabs[0].state.channels[1].count != sidebarOracleDisplay(0) {
		t.Fatal("other-channel badge not cleared")
	}
	merge(f.Channel.ID, f.MemberID, 1, unread.Range{Lo: 0, Hi: seq + 1})
	readStep("other-member-persisted-read", f.MemberID)
	// Foreign persistence must be an immediate no-op for every local tab.
	exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'foreign',2)`, foreign.OrganizationID, foreign.Channel.ID, foreign.Channel.DefaultTopicID, foreign.MemberID)
	exec(`UPDATE organization SET event_seq=2,event_log_boundary_seq=2 WHERE id=$1`, foreign.OrganizationID)
	step("foreign-post", &realtime.Event{OrganizationID: foreign.OrganizationID, Kind: conversation.KindPosted}, nil, "post author and page read effects absent")
	must(platform.InTx(ctx, pool, func(tx platform.Tx) error {
		return unreadpg.WriterIn(tx).Merge(ctx, unread.Scope{OrganizationID: foreign.OrganizationID, ChannelID: foreign.Channel.ID, MemberID: foreign.MemberID}, 1, unread.Range{Lo: 0, Hi: 3})
	}))
	step("foreign-read", nil, &hybridRead{foreign.OrganizationID, foreign.MemberID}, "read commit and exact coverage absent")
	// Move one read and one unread message, retaining their sequences. The
	// separate notice is unread to this reader and read to its author.
	var movedIDs []kernel.ID
	must(pool.QueryRow(ctx, `WITH moved AS (
UPDATE message SET topic_id=$1,moved_event_seq=$2 WHERE organization_id=$3 AND channel_id=$4 AND event_seq IN (6,$5) RETURNING id
) SELECT array_agg(id) FROM moved`, selected, seq+1, f.OrganizationID, f.Channel.ID, seq-2).Scan(&movedIDs))
	seq++
	exec(`UPDATE organization SET event_seq=$2,event_log_boundary_seq=$2 WHERE id=$1`, f.OrganizationID, seq)
	move := realtime.Event{OrganizationID: f.OrganizationID, Seq: seq, Kind: conversation.KindMessagesMoved, ChannelID: f.Channel.ID, Payload: conversation.EncodeMoved(conversation.Moved{ChannelID: f.Channel.ID, FromTopicID: f.Channel.DefaultTopicID, ToTopicID: selected, MessageIDs: movedIDs})}
	step("move-read-and-unread", &move, nil, "move read identities and list metadata absent")
	checkCounts(1, 0, 1)
	postStep("branch-notice-author-read", f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	checkCounts(2, 1, 1)
	conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "Aardvark")
	exec(`UPDATE topic SET name='AAA selected' WHERE organization_id=$1 AND id=$2`, f.OrganizationID, selected)
	step("list-displacement-and-renamed-selection", nil, nil, "unknown input or list metadata absent")
	if tabs[0].state.topics[1].id != selected || tabs[0].state.topics[49].name != "Topic47" {
		t.Fatal("topic list did not displace entries or include renamed selection")
	}
	created := conversationtest.Channel(t, pool, f.OrganizationID, "zzz", false)
	exec(`UPDATE channel SET name='zz renamed' WHERE organization_id=$1 AND id=$2`, f.OrganizationID, created.ID)
	step("new-and-renamed-channel", nil, nil, "unknown input or list metadata absent")
	for _, fault := range []string{"lost", "duplicated", "reordered"} {
		t.Run(fault, func(t *testing.T) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			e := post(f.Channel.ID, selected, f.MemberID) // Deliberately omit delivery.
			p := tabs[0]
			switch fault {
			case "duplicated":
				p.state.selection[0].count.raw += 2
			case "reordered":
				p.state.topics[0].count, p.state.selection[0].count = p.state.selection[0].count, p.state.topics[0].count
			}
			want := oracle(p)
			if sidebarOracleCompare(p.state, want) == nil {
				t.Fatalf("%s domain drift was not detected at post %d", fault, e.Seq)
			}
			now = now.Add(p.interval)
			must(p.tick(now))
			must(p.complete())
			if r := p.records[len(p.records)-1]; r.mode != "reconciliation" || r.reason != "unconditional lost-state repair" {
				t.Fatalf("drift repair record: %+v", r)
			}
			t.Logf("%s repair record=%+v", fault, p.records[len(p.records)-1])
			must(sidebarOracleCompare(p.state, want))
		})
	}
	merge(f.Channel.ID, member, 5, unread.Range{Lo: 0, Hi: seq + 1})
	readStep("feed-after-topic-and-moves-clears-zero", member)
	checkCounts(0, 0, 0)
}
