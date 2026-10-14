package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestCostReadCapacity(t *testing.T) {
	if (costReadResult{}).complete() {
		t.Fatal("empty changed-read accounting accepted")
	}
	for _, seconds := range []int{60, 600, 601} {
		p, err := newCostReadPlan(5000, time.Duration(seconds)*time.Second)
		if err != nil || p.scheduled != 100*seconds || p.capacity != (100*seconds+4999)/5000 {
			t.Fatalf("whole-window capacity lost: plan=%+v error=%v", p, err)
		}
		seen := make([]int, 5000)
		for i := range p.scheduled {
			member, cursor := p.target(i, 2)
			if cursor != 2+int64(seen[member]) || cursor >= 2+int64(p.capacity) {
				t.Fatal("read target repeated or exceeded seeded capacity")
			}
			seen[member]++
		}
	}
	for _, tt := range []struct {
		streams  int
		duration time.Duration
	}{{0, time.Second}, {1, 0}, {1, time.Nanosecond}} {
		if _, err := newCostReadPlan(tt.streams, tt.duration); err == nil {
			t.Fatal("empty read load accepted")
		}
	}
}

func TestCostReadAccounting(t *testing.T) {
	for _, tt := range []struct {
		name    string
		changed bool
		err     error
		want    costReadResult
	}{
		{"changed", true, nil, costReadResult{scheduled: 4, changed: 4}},
		{"noop", false, nil, costReadResult{scheduled: 4, noop: 4}},
		{"failed", false, errors.New("rolled back"), costReadResult{scheduled: 4, failed: 4}},
		{"unknown", true, platform.ErrCommitUnknown, costReadResult{scheduled: 4, failed: 4}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, _ := newCostReadPlan(5000, 40*time.Millisecond)
				start := time.Now()
				got := p.run(t.Context(), start, 2, func(_ context.Context, member int, cursor int64) (bool, error) {
					if cursor != 2 || time.Now() != start.Add(time.Duration(member)*10*time.Millisecond) {
						t.Error("read load lost fixed 100/s deadlines or member targets")
					}
					return tt.changed, tt.err
				})
				if got != tt.want || got.complete() != (tt.name == "changed") {
					t.Fatalf("read outcomes conflated: got=%+v want=%+v", got, tt.want)
				}
			})
		})
	}
}

func TestCostReadAdmissionAndDrain(t *testing.T) {
	for _, streams := range []int{1, 5000} {
		t.Run(fmt.Sprint(streams), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, _ := newCostReadPlan(streams, 1020*time.Millisecond)
				deadline := time.Now().Add(1020 * time.Millisecond)
				release := make(chan struct{})
				go func() { <-time.After(1020 * time.Millisecond); close(release) }()
				got := p.run(t.Context(), time.Now(), 2, func(context.Context, int, int64) (bool, error) {
					<-release
					return true, nil
				})
				if time.Now().Before(deadline) {
					<-time.After(time.Until(deadline))
				}
				synctest.Wait()
				want := min(streams, 100)
				if got != (costReadResult{scheduled: 102, changed: want, missed: 102 - want}) {
					t.Fatalf("admission or worker drain lost: %+v", got)
				}
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		p, _ := newCostReadPlan(5000, time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		go func() { <-time.After(15 * time.Millisecond); cancel() }()
		got := p.run(ctx, time.Now(), 2, func(ctx context.Context, _ int, _ int64) (bool, error) {
			<-ctx.Done()
			return false, ctx.Err()
		})
		if got != (costReadResult{scheduled: 100, failed: 2, missed: 98}) {
			t.Fatalf("cancelled admissions or failed drain lost: %+v", got)
		}
	})
}

func TestCostPersistedReadsAcrossMembers(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "member-reads", "general")
	members := []org.Membership{{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}}
	for i := 1; i < 3; i++ {
		account := identitytest.Account(t, pool, fmt.Sprintf("reader%d@example.org", i), "Reader")
		member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, fmt.Sprintf("reader%d", i), 1)
		members = append(members, org.Membership{Organization: members[0].Organization, Member: org.Member{ID: member, JoinedEventSeq: 1}})
	}
	// An incomplete final round leaves a seeded message unread for one member.
	p, err := newCostReadPlan(len(members), 80*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.seed(t.Context(), pool, members[0], f.Channel.ID, f.Channel.DefaultTopicID)
	if err != nil {
		t.Fatal(err)
	}
	changes := new(unread.Changes)
	for i := range p.scheduled {
		member, cursor := p.target(i, first)
		changed, err := costChangedRead(t.Context(), pool, changes, members[member], f.Channel.ID, cursor)
		if err != nil || !changed {
			t.Fatalf("scheduled read %d member %d cursor %d: changed=%t error=%v; want committed change", i, member, cursor, changed, err)
		}
	}
	for i, m := range members {
		rounds := (p.scheduled + len(members) - 1 - i) / len(members)
		if rounds < 2 {
			t.Fatalf("member %d has only %d scheduled rounds", i, rounds)
		}
		want, err := sidebarOracle(t.Context(), pool, f.OrganizationID, m.Member.ID, f.Channel.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		unreadCount := int64(p.capacity - rounds)
		if len(want.channels) != 1 || want.channels[0].id != f.Channel.ID || want.channels[0].count.raw != unreadCount {
			t.Fatalf("member %d persisted unread after %d rounds: channels=%+v; want %d", i, rounds, want.channels, unreadCount)
		}
		got, err := costModelSnapshot(t.Context(), pool, m, f.Channel.ID, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := sidebarOracleCompare(got.model, want); err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
		changed, err := costChangedRead(t.Context(), pool, changes, m, f.Channel.ID, first+int64(rounds)-1)
		if err != nil || changed {
			t.Fatalf("member %d repeated final read: changed=%t error=%v; want no-op", i, changed, err)
		}
	}
}

func TestCostPersistedReadsAndFinalModel(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "reads", "general")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	p, _ := newCostReadPlan(1, 30*time.Millisecond)
	first, err := p.seed(t.Context(), pool, m, f.Channel.ID, f.Channel.DefaultTopicID)
	if err != nil {
		t.Fatal(err)
	}
	changes := new(unread.Changes)
	watch := changes.Watch(f.OrganizationID, f.MemberID)
	defer watch.Release()
	c := &costModelConnection{changes: changes, sender: &retentionSender{send: func(realtime.Outgoing) error { return nil }}, snapshot: func(ctx context.Context) (hybridFrame, error) {
		return costModelSnapshot(ctx, pool, m, f.Channel.ID, nil, nil)
	}}
	install := func() {
		t.Helper()
		out, err := c.Recount(t.Context(), realtime.Subscription{Organization: f.OrganizationID, Member: f.MemberID, Channel: f.Channel.ID})
		if err != nil {
			t.Fatal(err)
		}
		out.Name, out.Ephemeral = "sidebar", true
		if err := c.Send(t.Context(), out); err != nil {
			t.Fatal(err)
		}
	}
	install()
	for i := range p.scheduled {
		_, cursor := p.target(i, first)
		changed, err := costChangedRead(t.Context(), pool, changes, m, f.Channel.ID, cursor)
		if err != nil || !changed {
			t.Fatalf("seed exhausted before whole window: changed=%t error=%v", changed, err)
		}
		generation, _ := watch.Snapshot()
		if generation != uint64(i+1) {
			t.Fatal("committed changed read did not publish exactly once")
		}
	}
	changed, err := costChangedRead(t.Context(), pool, changes, m, f.Channel.ID, first)
	if err != nil || changed {
		t.Fatal("successful repeated read counted as change")
	}
	changed, err = costChangedRead(t.Context(), pool, changes, m, f.Channel.ID, first+int64(p.capacity))
	if err == nil || changed {
		t.Fatal("failed read counted as persisted change")
	}
	generation, _ := watch.Snapshot()
	want, err := sidebarOracle(t.Context(), pool, f.OrganizationID, f.MemberID, f.Channel.ID, nil)
	if err != nil || generation != 3 || want.channels[0].count.raw != 0 {
		t.Fatalf("persisted read truth/publications lost: %v", err)
	}
	connections, truth := []*costModelConnection{c, c}, []sidebarOracleModel{want, want}
	if costReadDrain(connections, truth) == nil {
		t.Fatal("drain accepted stale read-only model")
	}
	install()
	if err := costReadDrain(connections, truth); err != nil {
		t.Fatal(err)
	}
	connections[1] = &costModelConnection{}
	if costReadDrain(connections, truth) == nil {
		t.Fatal("one stream satisfied another's missing final snapshot")
	}
	if costReadDrain(nil, nil) == nil || costReadDrain(connections, truth[:1]) == nil {
		t.Fatal("drain accepted missing witnesses")
	}
}
