package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type readCostSQL struct {
	statements atomic.Int64
	counts     atomic.Int64
	commit     time.Time
}
type readCostKey struct{}
type readCostCommitKey struct{}
type readCostTrace struct{}

func (readCostTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if s, ok := ctx.Value(readCostKey{}).(*readCostSQL); ok {
		s.statements.Add(1)
		for _, name := range []string{"FirstChannelReadRanges", "CountChannelUnread", "ReadTopicState", "CountTopicUnread"} {
			if strings.HasPrefix(d.SQL, "-- name: "+name+" ") {
				s.counts.Add(1)
			}
		}
	}
	return context.WithValue(ctx, readCostCommitKey{}, strings.EqualFold(d.SQL, "commit"))
}
func (readCostTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if s, ok := ctx.Value(readCostKey{}).(*readCostSQL); ok && ctx.Value(readCostCommitKey{}) == true && d.Err == nil {
		s.commit = time.Now()
	}
}

type readCostPublisher struct {
	changes unread.Changes
	mu      sync.Mutex
	times   map[kernel.ID]time.Duration
}

func (p *readCostPublisher) ReadChanged(o, m kernel.ID) {
	start := time.Now()
	p.changes.ReadChanged(o, m)
	took := time.Since(start)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.times[m] = took
}

type readCostTab struct {
	reader                                            *conversation.Reader
	events                                            realtime.EventReader
	auth                                              realtime.Authorizer
	changes                                           *unread.Changes
	member                                            org.Membership
	channel, a, b                                     kernel.ID
	snapshots, wakes, loops, recounts, frames, checks atomic.Int64
	recountSQL, eventSQL, authSQL                     readCostSQL
	ready                                             chan struct{}
	sent                                              chan readCostFrame
	recountAt                                         time.Time
	lastFrame                                         int64
	generation                                        uint64
	watch                                             *unread.ChangeWatch
}
type readCostFrame struct {
	counts        [3]int64
	at, recountAt time.Time
}

func (c *readCostTab) Watch(o, m kernel.ID) realtime.SidebarWatch {
	c.watch = c.changes.Watch(o, m)
	return c
}
func (c *readCostTab) Release() { c.watch.Release() }
func (c *readCostTab) Snapshot() (uint64, <-chan struct{}) {
	c.snapshots.Add(1)
	g, changed := c.watch.Snapshot()
	// Count observed invalidation wakes, including any coalesced generation jump.
	if g > c.generation {
		c.wakes.Add(1)
		c.generation = g
	}
	if f := c.frames.Load(); f > c.lastFrame {
		c.lastFrame = f
		c.ready <- struct{}{}
	}
	return g, changed
}
func (c *readCostTab) Trigger(e realtime.Event) bool {
	return e.Kind == conversation.KindPosted || e.Kind == conversation.KindMessagesMoved
}
func (c *readCostTab) Recount(ctx context.Context, _ realtime.Subscription) (realtime.Outgoing, error) {
	c.recountAt = time.Now()
	c.recounts.Add(1)
	s, err := c.reader.Sidebar(context.WithValue(ctx, readCostKey{}, &c.recountSQL), c.member, c.channel, nil)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	data, err := json.Marshal([3]int64{s.ChannelCounts[c.channel], s.TopicCounts[c.a], s.TopicCounts[c.b]})
	return realtime.Outgoing{Data: data}, err
}
func (c *readCostTab) EventsAfter(ctx context.Context, o kernel.ID, after int64, limit int) ([]realtime.Event, error) {
	c.loops.Add(1)
	return c.events.EventsAfter(context.WithValue(ctx, readCostKey{}, &c.eventSQL), o, after, limit)
}
func (c *readCostTab) MayReceive(ctx context.Context, a kernel.ID, slug string, e realtime.Event) (bool, error) {
	c.checks.Add(1)
	return c.auth.MayReceive(context.WithValue(ctx, readCostKey{}, &c.authSQL), a, slug, e)
}
func (c *readCostTab) Heartbeat(context.Context) error { return nil }
func (c *readCostTab) Send(_ context.Context, out realtime.Outgoing) error {
	if out.Name != "sidebar" || out.ID != 0 || !out.Ephemeral {
		return fmt.Errorf("unexpected frame: %+v", out)
	}
	var counts [3]int64
	if err := json.Unmarshal(out.Data, &counts); err != nil {
		return err
	}
	c.frames.Add(1)
	c.sent <- readCostFrame{counts, time.Now(), c.recountAt}
	return nil
}
func (c *readCostTab) counters() [10]int64 {
	return [10]int64{c.snapshots.Load(), c.wakes.Load(), c.loops.Load(), c.recounts.Load(), c.frames.Load(), c.checks.Load(), c.recountSQL.statements.Load(), c.eventSQL.statements.Load(), c.authSQL.statements.Load(), c.recountSQL.counts.Load()}
}
func readCostReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatal("measurement drain failed")
		var v T
		return v
	}
}
func readCostInts(t *testing.T, key, fallback string) []int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		raw = fallback
	}
	var out []int
	for _, s := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("invalid %s", key)
		}
		out = append(out, n)
	}
	return out
}

// TestReadAdvanceCost is opt-in; it measures real transactions and stream loops,
// replacing HTML/network writes with exact count witnesses. No heartbeat or posts
// occur during measurement. Channel handoffs exclude seeding and initial recounts.
func TestReadAdvanceCost(t *testing.T) {
	if os.Getenv("RIBBITTO_READ_COST") != "1" {
		t.Skip("set RIBBITTO_READ_COST=1")
	}
	for _, mode := range []string{"concentrated", "distributed"} {
		for _, n := range readCostInts(t, "RIBBITTO_READ_COST_STEPS", "1,10,100,1000") {
			for _, tabs := range readCostInts(t, "RIBBITTO_READ_COST_TABS", "1,2,16") {
				if tabs > n {
					continue
				}
				t.Run(fmt.Sprintf("%s/n%d/tabs%d", mode, n, tabs), func(t *testing.T) { readCostStep(t, n, tabs, mode) })
			}
		}
	}
	t.Run("zero-subscribers", func(t *testing.T) { readCostStep(t, 0, 0, "concentrated") })
}
func readCostStep(t *testing.T, n, tabs int, mode string) {
	fixture := pgtest.NewEmpty(t)
	pgtest.NewMigrator(t, fixture).Up(t.Context())
	f := conversationtest.OrganizationWithOwner(t, fixture, "cost", "general")
	other := conversationtest.OrganizationWithOwner(t, fixture, "foreign", "general")
	a := conversationtest.Topic(t, fixture, f.OrganizationID, f.Channel.ID, "A")
	b := conversationtest.Topic(t, fixture, f.OrganizationID, f.Channel.ID, "B")
	owner := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "cost"}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	seed := conversationpg.NewPosting(fixture, postingSequence, postingEvents, nil)
	var cursor int64
	for _, id := range []kernel.ID{a.ID, b.ID, a.ID, b.ID} {
		msg, err := seed.PostToTopic(t.Context(), owner, f.Channel.ID, &id, "seed")
		acceptanceOK(t, err)
		cursor = msg.EventSeq
	}
	active := 1
	if mode == "distributed" {
		active = min(4, n/tabs)
	}
	members := make([]org.Membership, max(n, active))
	// Placeholder hashes exclude password hashing from fixture creation.
	for i := range members {
		account := identitytest.Account(t, fixture, fmt.Sprintf("m%d@example.org", i), "Reader")
		m := orgtest.Member(t, fixture, f.OrganizationID, account, org.RoleMember, fmt.Sprintf("m%d", i), 1)
		members[i] = org.Membership{Organization: owner.Organization, Member: org.Member{ID: m, AccountID: account, JoinedEventSeq: 1}}
	}
	config := fixture.Config()
	config.MaxConns = 10
	config.ConnConfig.Tracer = readCostTrace{}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	acceptanceOK(t, err)
	defer pool.Close()
	var version string
	acceptanceOK(t, pool.QueryRow(t.Context(), "SHOW server_version").Scan(&version))
	t.Logf("%s %s/%s CPUs=%d PostgreSQL=%s pool=10 active-readers=%d", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), version, active)
	pub := &readCostPublisher{times: make(map[kernel.ID]time.Duration)}
	reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn)
	hub := realtime.NewHub()
	events := realtime.NewCachedEvents(t.Context(), realtimepg.NewReader(pool, orgpg.BoundsIn, conversationpg.EventKinds()), hub, 1024, time.Minute)
	auth := orgpg.NewCachedAuthorizer(t.Context(), pool, org.DefaultAuthorizationCapacity)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	var connections []*readCostTab
	for i := range n {
		m := members[i]
		ch, ta, tb, cur := f.Channel.ID, a.ID, b.ID, cursor
		if i < active*tabs {
			m = members[i/tabs]
		} else if i%2 == 1 {
			m = org.Membership{Organization: org.Organization{ID: other.OrganizationID, Slug: "foreign"}, Member: org.Member{ID: other.MemberID, AccountID: other.AccountID, JoinedEventSeq: 1}}
			ch, ta, tb, cur = other.Channel.ID, other.Channel.DefaultTopicID, other.Channel.DefaultTopicID, 1
		}
		c := &readCostTab{reader: reader, events: events, auth: auth, changes: &pub.changes, member: m, channel: ch, a: ta, b: tb, ready: make(chan struct{}, 4), sent: make(chan readCostFrame, 4)}
		connections = append(connections, c)
		sub := realtime.Subscription{Organization: m.Organization.ID, OrganizationSlug: m.Organization.Slug, Member: m.Member.ID, Account: m.Member.AccountID, Channel: ch, Interests: []realtime.Interest{realtime.InterestSidebar}}
		wg.Go(func() {
			_, err := (realtime.Stream{Hub: hub, Events: c, Authorizer: c, Sidebar: c}).Run(ctx, sub, cur, c)
			if err != nil && ctx.Err() == nil {
				t.Errorf("stream: %v", err)
			}
		})
		frame := readCostReceive(t, c.sent)
		want := [3]int64{4, 2, 2}
		if m.Organization.ID == other.OrganizationID {
			want = [3]int64{}
		}
		if frame.counts != want {
			t.Fatalf("initial counts=%v want=%v", frame.counts, want)
		}
		readCostReceive(t, c.ready)
	}
	baseline := make([][10]int64, n)
	for i, c := range connections {
		baseline[i] = c.counters()
	}
	reading := unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), pub)
	for phase := range 4 {
		type result struct {
			member   int
			sql      *readCostSQL
			duration time.Duration
			err      error
		}
		results := make(chan result, active)
		for i := range active {
			wg.Go(func() {
				s := &readCostSQL{}
				ctx := context.WithValue(ctx, readCostKey{}, s)
				scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: members[i].Member.ID}
				start := time.Now()
				var err error
				if phase < 2 {
					err = reading.Topic(ctx, unread.TopicScope{Scope: scope, TopicID: a.ID}, 1, cursor)
				} else {
					err = reading.Feed(ctx, scope, 1, cursor)
				}
				results <- result{i, s, time.Since(start), err}
			})
		}
		for range active {
			r := readCostReceive(t, results)
			acceptanceOK(t, r.err)
			if r.sql.commit.IsZero() {
				t.Fatal("missing successful COMMIT trace")
			}
			pub.mu.Lock()
			publication, published := pub.times[members[r.member].Member.ID]
			delete(pub.times, members[r.member].Member.ID)
			pub.mu.Unlock()
			effective := phase%2 == 0
			if published != effective {
				t.Fatalf("phase=%d publication=%t want=%t", phase, published, effective)
			}
			var delays, starts []time.Duration
			if effective {
				for _, c := range connections {
					if c.member.Member.ID != members[r.member].Member.ID {
						continue
					}
					frame := readCostReceive(t, c.sent)
					want := [3]int64{2, 0, 2}
					if phase == 2 {
						want = [3]int64{}
					}
					if frame.counts != want {
						t.Fatalf("final counts=%v want=%v", frame.counts, want)
					}
					delays = append(delays, frame.at.Sub(r.sql.commit))
					starts = append(starts, frame.recountAt.Sub(r.sql.commit))
					readCostReceive(t, c.ready)
				}
			}
			t.Logf("phase=%d reader=%d effective=%t transaction=%s publication=%s SQL=%d commit-to-recount=%v commit-to-frame=%v", phase, r.member, effective, r.duration-publication, publication, r.sql.statements.Load(), starts, delays)
		}
	}
	cancel()
	wg.Wait()
	totals := map[[2]kernel.ID][10]int64{}
	for i, c := range connections {
		got := c.counters()
		var delta [10]int64
		for j := range delta {
			delta[j] = got[j] - baseline[i][j]
		}
		matching := i < active*tabs
		if !matching && delta != [10]int64{} {
			t.Fatalf("unrelated org/member=%v/%v work=%v", c.member.Organization.ID, c.member.Member.ID, delta)
		}
		if matching && (delta[1] != 2 || delta[3] != 2 || delta[4] != 2 || delta[9] != 8) {
			t.Fatalf("matching work=%v want two wakes/recounts/frames and eight count statements", delta)
		}
		key := [2]kernel.ID{c.member.Organization.ID, c.member.Member.ID}
		sum := totals[key]
		for j := range sum {
			sum[j] += delta[j]
		}
		totals[key] = sum
	}
	for key, total := range totals {
		t.Logf("org/member=%v snapshots/wakes/loops/recounts/frames/checks/recountSQL/eventSQL/authSQL/countSQL=%v", key, total)
	}
}
