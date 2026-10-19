package realtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// costPageCounter wraps production counts with a same-snapshot cursor witness.
// Its extra statement is included in SQL and timing totals; rendering uses bytes.
type costPageCounter struct{ snapshot platform.Snapshot }

type costWitnessKey struct{}

func costPageCountsIn(s platform.Snapshot) conversation.PageCounter { return costPageCounter{s} }

func (c costPageCounter) Read(ctx context.Context, m org.Membership, channel kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	return c.read(ctx, m, channel, channels, topics, selected, true)
}

func (c costPageCounter) ReadCounts(ctx context.Context, m org.Membership, channel kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	return c.read(ctx, m, channel, channels, topics, selected, false)
}

func (c costPageCounter) read(ctx context.Context, m org.Membership, channel kernel.ID, channels, topics []kernel.ID, selected *kernel.ID, firstUnread bool) (conversation.PageCounts, error) {
	counts := compose.PageCountsIn(c.snapshot)
	read := counts.Read
	if !firstUnread {
		read = counts.(conversation.SidebarCounter).ReadCounts
	}
	result, err := read(ctx, m, channel, channels, topics, selected)
	if witness, ok := ctx.Value(costWitnessKey{}).(*int64); ok && err == nil {
		// Read in the counts' snapshot, never from the hub or a later transaction.
		// This extra test-only statement is included in SQL and timing totals.
		*witness, err = orgpg.EventCursorIn(c.snapshot).EventSeq(ctx, m.Organization.ID)
	}
	return result, err
}

type costSnapshot struct {
	counts     [2]int64
	through    int64
	at         time.Time
	generation uint64
}

type costConnection struct {
	reader         *conversation.Reader
	member         org.Membership
	channel, topic kernel.ID
	initial        int64
	messages       *collector
	mu             sync.Mutex
	snapshots      []costSnapshot
	sequences      map[int64]bool
	changes        *unread.Changes
	watch          *unread.ChangeWatch
	other          bool
	postingChannel kernel.ID
	unread         []delivery
	messageCount   int
	postTriggers   int
	resyncAt       time.Time
}

func (c *costConnection) Watch(organization, member kernel.ID) realtime.SidebarWatch {
	c.watch = c.changes.Watch(organization, member)
	return c.watch
}

func (c *costConnection) Trigger(e realtime.Event) (trigger bool) {
	// cmd/ and web are forbidden imports here; mirror only the bound trigger.
	defer func() {
		if trigger && e.Kind == conversation.KindPosted {
			c.postTriggers++ // Read only after the stream writer has joined.
		}
	}()
	switch e.Kind {
	case conversation.KindPosted:
		post, err := conversation.DecodePosted(e.Payload)
		return err == nil && post.AuthorID != nil && *post.AuthorID == c.member.Member.ID
	case conversation.KindMessagesMoved:
		return e.ChannelID == c.channel
	default:
		return false
	}
}

func (c *costConnection) Recount(ctx context.Context, _ realtime.Subscription) (realtime.Outgoing, error) {
	var through int64
	ctx = context.WithValue(ctx, costWitnessKey{}, &through)
	generation, _ := c.watch.Snapshot() // Prove computation began after final publication.
	s, err := c.reader.Sidebar(ctx, c.member, c.channel, nil)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	if c.other && s.ChannelCounts[c.channel] != 0 {
		return realtime.Outgoing{}, fmt.Errorf("other placement's empty channel has unread messages")
	}
	data, err := json.Marshal([4]int64{s.ChannelCounts[c.postingChannel], s.TopicCounts[c.topic], through, int64(generation)})
	return realtime.Outgoing{Data: data}, err
}

func (c *costConnection) Heartbeat(context.Context) error { return nil }

func (c *costConnection) Send(ctx context.Context, out realtime.Outgoing) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch out.Name {
	case "sidebar":
		var counts [4]int64
		if err := json.Unmarshal(out.Data, &counts); err != nil {
			return err
		}
		c.snapshots = append(c.snapshots, costSnapshot{counts: [2]int64{counts[0], counts[1]}, through: counts[2], at: time.Now(), generation: uint64(counts[3])})
	case "message":
		c.messageCount++
		c.sequences[out.ID] = true
		return c.messages.Send(ctx, out)
	case "unread":
		var post struct{ Seq int64 }
		if err := json.Unmarshal(out.Data, &post); err != nil {
			return err
		}
		c.sequences[post.Seq] = true
		c.unread = append(c.unread, delivery{post.Seq, time.Now()})
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
		ready := len(c.snapshots) > 0 && c.snapshots[0].counts == c.expectedCounts(0)
		c.mu.Unlock()
		if !ready {
			return false
		}
	}
	return true
}

func costComplete(connections []*costConnection, returned map[int64]time.Time) bool {
	var last int64
	for seq := range returned {
		last = max(last, seq)
	}
	for _, c := range connections {
		c.mu.Lock()
		ready := len(c.snapshots) > 0 && c.snapshots[len(c.snapshots)-1].exact(c.expectedCounts(len(returned)), last, c.resyncAt)
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

func (c *costConnection) expectedCounts(posts int) [2]int64 {
	want := min(c.initial+int64(posts), 100)
	if c.other {
		return [2]int64{want, 0}
	}
	return [2]int64{want, want}
}

func (s costSnapshot) exact(counts [2]int64, seq int64, after time.Time) bool {
	return s.counts == counts && s.through == seq && s.generation == 1 && s.at.After(after)
}

func TestCostExactSnapshot(t *testing.T) {
	after := time.Now()
	s := costSnapshot{counts: [2]int64{2, 0}, through: 7, generation: 1, at: after.Add(time.Nanosecond)}
	for _, tt := range []struct {
		counts [2]int64
		seq    int64
		want   bool
	}{{[2]int64{2, 0}, 7, true}, {[2]int64{1, 0}, 7, false}, {[2]int64{3, 0}, 7, false}, {[2]int64{2, 1}, 7, false}, {[2]int64{2, 0}, 8, false}} {
		if s.exact(tt.counts, tt.seq, after) != tt.want {
			t.Fatalf("exact snapshot accepted wrong counts or cursor: %+v", tt)
		}
	}
	s.generation = 0
	if s.exact(s.counts, s.through, after) {
		t.Fatal("exact snapshot accepted a computation before resync publication")
	}
}

// The renderer cannot import web; retain its public payload and cache contract.
// Stream.Run selects this through UnreadRenderer and performs authorization.
func (r readingRenderer) RenderUnread(ctx context.Context, e realtime.Event) (realtime.Outgoing, error) {
	if e.Kind != conversation.KindPosted {
		return realtime.Outgoing{}, nil
	}
	load := func(context.Context) (realtime.Outgoing, error) {
		post, err := conversation.DecodePosted(e.Payload)
		if err != nil || post.TopicID == nil || post.AuthorID == nil {
			return realtime.Outgoing{}, err
		}
		data, err := json.Marshal(struct {
			Channel string `json:"channel"`
			Topic   string `json:"topic"`
			Seq     int64  `json:"seq"`
			Author  string `json:"author"`
		}{realtime.FormatPayloadID(post.ChannelID), realtime.FormatPayloadID(*post.TopicID), e.Seq, realtime.FormatPayloadID(*post.AuthorID)})
		return realtime.Outgoing{Name: "unread", Ephemeral: true, Data: data}, err
	}
	if r.unreadRenders == nil {
		return load(ctx)
	}
	return r.unreadRenders.Get(ctx, e.Seq, load)
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
		// This makes shared member output observable even before the count cap.
		initial := int64(i % 2)
		err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return unreadpg.WriterIn(tx).Merge(t.Context(), unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}, m.Member.JoinedEventSeq, conversation.SequenceRange{Lo: 0, Hi: seed.EventSeq + 1 - initial})
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
		{"counts too high", [2]int64{3, 3}, true, false},
		{"channel stale", [2]int64{1, 2}, true, false},
		{"topic stale", [2]int64{2, 1}, true, false},
		{"message missing", [2]int64{2, 2}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &costConnection{initial: 1, snapshots: []costSnapshot{{counts: tt.counts, through: 7, at: time.Now(), generation: 1}}, sequences: map[int64]bool{7: tt.delivered}}
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
	first.snapshots[0].through = 7
	first.snapshots[0].generation = 1
	first.sequences = map[int64]bool{7: true}
	if costComplete([]*costConnection{first, second}, map[int64]time.Time{7: time.Now()}) {
		t.Fatal("one connection satisfied another's drain")
	}
}

func validCostSidebar(distinct bool, posts int) bool { return distinct && posts > 0 }

func TestCostSidebarConfiguration(t *testing.T) {
	for _, tt := range []struct {
		distinct bool
		posts    int
		want     bool
	}{{true, 99, true}, {false, 20, false}, {true, 100, true}, {true, 1200, true}, {true, 0, false}} {
		if got := validCostSidebar(tt.distinct, tt.posts); got != tt.want {
			t.Errorf("distinct=%t posts=%d: valid=%t, want %t", tt.distinct, tt.posts, got, tt.want)
		}
	}
}

func TestCostConnectionWatchIsNeverNil(t *testing.T) {
	// Stream.Run defers Release on whatever Watch returns.
	watch := (&costConnection{changes: &unread.Changes{}}).Watch(kernel.ID{1}, kernel.ID{2})
	if watch == nil {
		t.Fatal("connection returned a nil sidebar watch")
	}
	watch.Release()
}
