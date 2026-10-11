package realtime_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// These adapters mirror cmd/ribbitto's public count composition without moving
// production wiring. Only HTML rendering is replaced by a snapshot witness.
type costPageCounter struct{ snapshot platform.Snapshot }

func costPageCountsIn(s platform.Snapshot) conversation.PageCounter { return costPageCounter{s} }

func (c costPageCounter) Read(ctx context.Context, m org.Membership, channel kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	counts, err := unread.NewChannelCounts(unreadpg.ChannelRangesIn,
		func(s platform.Snapshot) unread.ChannelCounter { return conversationpg.ChannelUnreadIn(s) }).Read(ctx, c.snapshot, m.Organization.ID, m.Member.ID, m.Member.JoinedEventSeq, channels)
	if err != nil {
		return conversation.PageCounts{}, err
	}
	result := conversation.PageCounts{ChannelCounts: make(map[kernel.ID]int64, len(counts)), FeedFirstUnread: counts[channel].FirstUnread}
	for id, count := range counts {
		result.ChannelCounts[id] = count.Count
	}
	result.TopicCounts, result.TopicFirstUnread, err = unread.NewTopicCounts(unreadpg.TopicStateIn,
		func(s platform.Snapshot) unread.TopicCounter { return conversationpg.TopicUnreadIn(s) }).Read(ctx, c.snapshot, unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}, m.Member.JoinedEventSeq, topics, selected)
	return result, err
}

type costSnapshot struct {
	counts [2]int64
	at     time.Time
}

func (s costSnapshot) covers(want int64) bool { return s.counts[0] >= want && s.counts[1] >= want }

type costConnection struct {
	reader         *conversation.Reader
	member         org.Membership
	channel, topic kernel.ID
	initial        int64
	messages       *collector
	mu             sync.Mutex
	snapshots      []costSnapshot
	sequences      map[int64]bool
	timings        *costTimings
	slots          chan struct{}
}

func (c *costConnection) Trigger(e realtime.Event) bool {
	return e.Kind == conversation.KindPosted || e.Kind == conversation.KindMessagesMoved
}

func (c *costConnection) Recount(ctx context.Context, _ realtime.Subscription) (realtime.Outgoing, error) {
	start := time.Now()
	if c.slots != nil {
		select {
		case c.slots <- struct{}{}:
			defer func() { <-c.slots }()
		case <-ctx.Done():
			c.timings.add(costGate, start, ctx.Err())
			return realtime.Outgoing{}, context.Cause(ctx)
		}
	}
	c.timings.add(costGate, start, nil)
	start = time.Now()
	ctx = context.WithValue(ctx, costRoleKey{}, costSidebar)
	s, err := c.reader.Sidebar(ctx, c.member, c.channel, nil)
	c.timings.add(costWhole, start, err)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	data, err := json.Marshal([2]int64{s.ChannelCounts[c.channel], s.TopicCounts[c.topic]})
	return realtime.Outgoing{Data: data}, err
}

func (c *costConnection) Heartbeat(context.Context) error { return nil }

func (c *costConnection) Send(ctx context.Context, out realtime.Outgoing) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch out.Name {
	case "sidebar":
		var counts [2]int64
		if err := json.Unmarshal(out.Data, &counts); err != nil {
			return err
		}
		c.snapshots = append(c.snapshots, costSnapshot{counts, time.Now()})
	case "message":
		c.sequences[out.ID] = true
		return c.messages.Send(ctx, out)
	}
	return nil
}

func (c *costConnection) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots = nil
}

func costInitial(connections []*costConnection) bool {
	for _, c := range connections {
		c.mu.Lock()
		ready := len(c.snapshots) > 0 && c.snapshots[0].counts == [2]int64{c.initial, c.initial}
		c.mu.Unlock()
		if !ready {
			return false
		}
	}
	return true
}

func costComplete(connections []*costConnection, returned map[int64]time.Time) bool {
	for _, c := range connections {
		c.mu.Lock()
		ready := len(c.snapshots) > 0 && c.snapshots[len(c.snapshots)-1].covers(c.initial+int64(len(returned)))
		for seq := range returned {
			ready = ready && c.sequences[seq]
		}
		c.mu.Unlock()
		if !ready {
			return false
		}
	}
	return true
}

func costConnections(t *testing.T, pool *pgxpool.Pool, posting *conversation.Posting, reader *conversation.Reader, members []org.Membership, channel, topic kernel.ID) []*costConnection {
	t.Helper()
	seed, err := posting.Post(t.Context(), members[0], channel, "read-state seed")
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*costConnection, len(members))
	for i, m := range members {
		// All prior steps are read; alternating members retain one unread seed.
		// This keeps totals <=100 and makes shared member output observable.
		initial := int64(i % 2)
		err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return unreadpg.WriterIn(tx).Merge(t.Context(), unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}, m.Member.JoinedEventSeq, unread.Range{Lo: 0, Hi: seed.EventSeq + 1 - initial})
		})
		if err != nil {
			t.Fatal(err)
		}
		connections[i] = &costConnection{reader: reader, member: m, channel: channel, topic: topic, initial: initial, sequences: make(map[int64]bool)}
	}
	return connections
}

func TestCostCompletion(t *testing.T) {
	returned := map[int64]time.Time{7: time.Now()}
	for _, tt := range []struct {
		name      string
		counts    [2]int64
		delivered bool
		want      bool
	}{
		{"complete", [2]int64{2, 2}, true, true},
		{"coalesced", [2]int64{3, 3}, true, true},
		{"channel stale", [2]int64{1, 2}, true, false},
		{"topic stale", [2]int64{2, 1}, true, false},
		{"message missing", [2]int64{2, 2}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &costConnection{initial: 1, snapshots: []costSnapshot{{counts: tt.counts, at: time.Now()}}, sequences: map[int64]bool{7: tt.delivered}}
			if got := costComplete([]*costConnection{c}, returned); got != tt.want {
				t.Fatalf("completion = %t, want %t", got, tt.want)
			}
		})
	}
	sink := &collector{}
	if err := sink.Send(t.Context(), realtime.Outgoing{Name: "sidebar"}); err != nil {
		t.Fatal(err)
	}
	if sink.count() != 0 {
		t.Fatal("sidebar counted as a message delivery")
	}
}

func TestCostInitialAndEachConnection(t *testing.T) {
	first := &costConnection{initial: 0, snapshots: []costSnapshot{{counts: [2]int64{0, 0}}}}
	second := &costConnection{initial: 1}
	if costInitial([]*costConnection{first, second}) {
		t.Fatal("baseline accepted a missing initial recount")
	}
	second.snapshots = []costSnapshot{{counts: [2]int64{0, 0}}}
	if costInitial([]*costConnection{first, second}) {
		t.Fatal("baseline accepted another member's read state")
	}
	second.snapshots[0].counts = [2]int64{1, 1}
	if !costInitial([]*costConnection{first, second}) {
		t.Fatal("initial recounts did not complete")
	}
	first.snapshots[0].counts = [2]int64{1, 1}
	first.sequences = map[int64]bool{7: true}
	if costComplete([]*costConnection{first, second}, map[int64]time.Time{7: time.Now()}) {
		t.Fatal("one connection satisfied another's drain")
	}
}

func validCostSidebar(distinct bool, posts int) bool { return distinct && posts <= 99 }

func TestCostSidebarConfiguration(t *testing.T) {
	for _, tt := range []struct {
		distinct bool
		posts    int
		want     bool
	}{{true, 99, true}, {false, 20, false}, {true, 100, false}} {
		if got := validCostSidebar(tt.distinct, tt.posts); got != tt.want {
			t.Errorf("distinct=%t posts=%d: valid=%t, want %t", tt.distinct, tt.posts, got, tt.want)
		}
	}
}
