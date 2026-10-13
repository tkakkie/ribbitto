package realtime_test

import (
	"context"
	"fmt"
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

func TestHybridBaseline(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "hybrid", "general")
	account := identitytest.Account(t, pool, "reader@example.org", "Reader")
	member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: member}}
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	conversationtest.Channel(t, pool, f.OrganizationID, "zero", false)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
	var selected kernel.ID
	for i := 1; i <= 50; i++ {
		selected = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("Topic%02d", i)).ID
	}
	if _, err := pool.Exec(t.Context(), `
INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,'fixture',s FROM generate_series(1,107) s
UNION ALL SELECT $1::uuid,$2::uuid,$5::uuid,$4::uuid,'selected',108
UNION ALL SELECT $1::uuid,$6::uuid,$7::uuid,$4::uuid,'other',109;
`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, selected, other.ID, other.DefaultTopicID); err != nil {
		t.Fatal(err)
	}
	snapshot := func(ctx context.Context) (sidebarOracleModel, error) {
		return hybridSnapshot(ctx, pool, m, f.Channel.ID, &selected)
	}
	exact := func(t *testing.T, p *hybridPrototype) {
		t.Helper()
		want, err := sidebarOracle(t.Context(), pool, f.OrganizationID, member, f.Channel.ID, &selected)
		if err != nil {
			t.Fatal(err)
		}
		if err := sidebarOracleCompare(p.state, want); err != nil {
			t.Fatal(err)
		}
	}
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	q := realtime.NewRecountScheduler(26, 100)
	defer q.Close()
	now := time.Unix(0, 0)
	for _, interval := range []time.Duration{10 * time.Second, 0, 60 * time.Second} {
		t.Run(fmt.Sprint(interval), func(t *testing.T) {
			p := newHybrid(q, now, interval, snapshot)
			defer p.registration.Cancel()
			if (interval == 0 && p.interval != 30*time.Second) || (interval != 0 && p.interval != interval) {
				t.Fatal("repair interval must be 30s by default and honor 10s/60s overrides")
			}
			must(t, p.reconcile("connect"))
			must(t, p.complete())
			exact(t, p)
			if p.state.channels[0].count.raw != 107 || p.state.channels[0].count.capped != 100 || len(p.state.topics) != 50 || p.state.selection[0].id != selected {
				t.Fatal("connect fixture must exercise uncapped counts and off-list selection")
			}
			for cycle := 1; cycle <= 2; cycle++ {
				p.state.channels[0].count.raw++                   // Invisible at the cap, still drift.
				p.state.selection[0].count = sidebarOracleCount{} // Visible drift too.
				deadline := now.Add(time.Duration(cycle) * p.interval)
				before := len(p.records)
				must(t, p.tick(deadline.Add(-time.Nanosecond)))
				if len(p.records) != before {
					t.Fatal("periodic repair ran before its fixed deadline")
				}
				must(t, p.tick(deadline))
				if len(p.records) != before+1 || p.records[before].operation != "periodic" {
					t.Fatal("quiet deadline did not request unconditional periodic repair")
				}
				must(t, p.complete())
				exact(t, p)
			}
		})
	}
	p := newHybrid(q, now, 0, snapshot)
	defer p.registration.Cancel()
	must(t, p.reconcile("connect"))
	must(t, p.complete())
	// Gate writes before oracle checkpoints; no comparison with a later DB.
	must(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		return unreadpg.WriterIn(tx).Merge(t.Context(), unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: member}, 1, unread.Range{Lo: 0, Hi: 109})
	}))
	release := make(chan struct{})
	p.snapshot = func(ctx context.Context) (sidebarOracleModel, error) {
		select {
		case <-release:
			return snapshot(ctx)
		case <-ctx.Done():
			return sidebarOracleModel{}, ctx.Err()
		}
	}
	must(t, p.handle("feed-read", nil, &hybridRead{f.OrganizationID, member}, f.OrganizationID, member))
	if p.state.channels[0].count.raw != 107 {
		t.Fatal("non-derivable read changed state before reconciliation completed")
	}
	close(release)
	must(t, p.complete())
	exact(t, p)
	p.snapshot = snapshot
	post := realtime.Event{OrganizationID: f.OrganizationID, Kind: conversation.KindPosted, ChannelID: other.ID, Payload: conversation.EncodePosted(other.ID, kernel.ID{1}, other.DefaultTopicID)}
	move := realtime.Event{OrganizationID: f.OrganizationID, Kind: conversation.KindMessagesMoved, Payload: conversation.EncodeMoved(conversation.Moved{ChannelID: f.Channel.ID, FromTopicID: f.Channel.DefaultTopicID, ToTopicID: selected, MessageIDs: []kernel.ID{{1}}})}
	for _, tt := range []struct {
		operation string
		event     *realtime.Event
		read      *hybridRead
		mode      string
	}{
		{"ordinary-post", &post, nil, "reconciliation"},
		{"post-with-cursor", &post, nil, "reconciliation"},
		{"older-page-post", &post, nil, "reconciliation"},
		{"move-or-branch", &move, nil, "reconciliation"},
		{"topic-read", nil, &hybridRead{f.OrganizationID, member}, "reconciliation"},
		{"overlapping-read-or-unknown-outcome", nil, &hybridRead{f.OrganizationID, member}, "reconciliation"},
		{"list-change-or-lost-input", nil, nil, "reconciliation"},
		{"unknown-payload", &realtime.Event{OrganizationID: f.OrganizationID}, nil, "reconciliation"},
		{"other-member-read", nil, &hybridRead{f.OrganizationID, f.MemberID}, "delta-noop"},
		{"other-organization-read", nil, &hybridRead{foreign.OrganizationID, member}, "delta-noop"},
		{"foreign-post", &realtime.Event{OrganizationID: foreign.OrganizationID, Kind: conversation.KindPosted}, nil, "delta-noop"},
	} {
		must(t, p.handle(tt.operation, tt.event, tt.read, f.OrganizationID, member))
		r := p.records[len(p.records)-1]
		if r.condition != "production-only" || r.operation != tt.operation || r.mode != tt.mode || (r.request == 0) != (tt.mode == "delta-noop") {
			t.Fatalf("production-only routing %s: record=%+v, want %s", tt.operation, r, tt.mode)
		}
		if tt.mode == "reconciliation" {
			must(t, p.complete())
		}
		exact(t, p)
	}
	// A new registration/incarnation must take fresh persisted state.
	p.registration.Cancel()
	p = newHybrid(q, now, 0, snapshot)
	defer p.registration.Cancel()
	must(t, p.reconcile("reconnect"))
	must(t, p.complete())
	exact(t, p)
}
