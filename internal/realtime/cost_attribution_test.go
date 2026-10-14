package realtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

const (
	costOther = iota
	costSidebar
	costEvents
	costAuth
	costRanges
	costChannelCount
	costTopicState
	costTopicCount
	costGetChannel
	costListChannels
	costListTopics
	costWitness
	costBegin
	costCommit
	costRollback
	costWhole
	costGate
	costKinds
)

var costNames = [costKinds]string{"acquire-other", "acquire-sidebar", "acquire-events", "acquire-auth", "FirstChannelReadRanges", "CountChannelUnread", "ReadTopicState", "CountTopicUnread", "GetChannel", "ListChannels", "ListTopics", "GetEventSeq", "BEGIN", "COMMIT", "ROLLBACK", "whole-sidebar", "recount-slot"}

type costRoleKey struct{}
type costAcquireKey struct{}
type costQueryKey struct{}
type costStart struct {
	kind int
	at   time.Time
}

// Acquisition elapsed includes pgx overhead, connection creation and failures;
// it is not puddle's empty-only wait. Shared loaders preserve the role through
// WithoutCancel. Statement elapsed ends at rows.Close, including decoding.
type costTimings struct {
	mu      sync.Mutex
	samples [costKinds][]time.Duration
	failed  [costKinds]int
}

func (c *costTimings) add(kind int, start time.Time, err error) {
	if c == nil {
		return
	}
	took := time.Since(start)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples[kind] = append(c.samples[kind], took)
	if err != nil {
		c.failed[kind]++
	}
}

func (c *costTimings) reset() {
	c.take()
}

func (c *costTimings) take() *costTimings {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := &costTimings{samples: c.samples, failed: c.failed}
	c.samples, c.failed = [costKinds][]time.Duration{}, [costKinds]int{}
	return snapshot
}

func (c *costTimings) report(t *testing.T, streams int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for kind, samples := range c.samples {
		var total time.Duration
		for _, d := range samples {
			total += d
		}
		p := percentiles(samples)
		t.Logf("attribution: streams=%d kind=%s samples=%d errors=%d total=%s p50/p95/p99=%s/%s/%s", streams, costNames[kind], len(samples), c.failed[kind], total.Round(time.Microsecond), p[0].Round(time.Microsecond), p[1].Round(time.Microsecond), p[2].Round(time.Microsecond))
	}
}

func (c *epochCounter) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	if c.timings == nil {
		return ctx
	}
	kind, _ := ctx.Value(costRoleKey{}).(int)
	return context.WithValue(ctx, costAcquireKey{}, costStart{kind, time.Now()})
}

func (c *epochCounter) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	if data.Err == nil {
		n := c.active.Add(1)
		for old := c.peak.Load(); n > old; old = c.peak.Load() {
			if c.peak.CompareAndSwap(old, n) {
				break
			}
		}
	}
	if start, ok := ctx.Value(costAcquireKey{}).(costStart); ok {
		c.timings.add(start.kind, start.at, data.Err)
	}
}

func (c *epochCounter) TraceRelease(_ *pgxpool.Pool, _ pgxpool.TraceReleaseData) {
	c.active.Add(-1)
}

func (c *costTimings) queryStart(ctx context.Context, sql string) context.Context {
	if c == nil || ctx.Value(costRoleKey{}) != costSidebar {
		return ctx
	}
	for kind := costRanges; kind <= costWitness; kind++ {
		if strings.HasPrefix(sql, "-- name: "+costNames[kind]+" ") {
			return context.WithValue(ctx, costQueryKey{}, costStart{kind, time.Now()})
		}
	}
	if fields := strings.Fields(sql); len(fields) > 0 {
		for kind := costBegin; kind <= costRollback; kind++ {
			if strings.EqualFold(fields[0], costNames[kind]) {
				return context.WithValue(ctx, costQueryKey{}, costStart{kind, time.Now()})
			}
		}
	}
	return ctx
}

func (c *epochCounter) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	costPersistEnd(ctx, data)
	if start, ok := ctx.Value(costQueryKey{}).(costStart); ok {
		c.timings.add(start.kind, start.at, data.Err)
	}
}

func TestCostAttribution(t *testing.T) {
	c := &epochCounter{timings: &costTimings{}}
	failure := errors.New("measurement failure")
	for _, role := range []int{costOther, costSidebar, costEvents, costAuth} {
		ctx := context.WithoutCancel(context.WithValue(t.Context(), costRoleKey{}, role))
		ctx = c.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
		stamp := ctx.Value(costAcquireKey{}).(costStart)
		stamp.at = stamp.at.Add(-time.Second)
		ctx = context.WithValue(ctx, costAcquireKey{}, stamp)
		c.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{})
		c.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Err: failure})
	}
	for kind := costRanges; kind <= costRollback; kind++ {
		ctx := context.WithValue(t.Context(), costRoleKey{}, costSidebar)
		sql := "-- name: " + costNames[kind] + " :many\nSELECT 1"
		if kind >= costBegin {
			sql = []string{"begin isolation level repeatable read read only", "COMMIT", "rollback"}[kind-costBegin]
		}
		ctx = c.timings.queryStart(ctx, sql)
		stamp, ok := ctx.Value(costQueryKey{}).(costStart)
		if !ok {
			t.Fatalf("statement not attributed: %s", sql)
		}
		stamp.at = stamp.at.Add(-time.Second)
		ctx = context.WithValue(ctx, costQueryKey{}, stamp)
		c.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
		c.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: failure})
	}
	// Unrelated SQL and count SQL outside recounts must not contaminate timings.
	for _, ctx := range []context.Context{t.Context(), context.WithValue(t.Context(), costRoleKey{}, costSidebar)} {
		c.TraceQueryEnd(c.timings.queryStart(ctx, "SELECT 1"), nil, pgx.TraceQueryEndData{})
	}
	c.TraceQueryEnd(c.timings.queryStart(t.Context(), "-- name: CountTopicUnread :many"), nil, pgx.TraceQueryEndData{})
	for kind := costOther; kind <= costRollback; kind++ {
		if got := c.timings.samples[kind]; len(got) != 2 || got[0] < time.Second || c.timings.failed[kind] != 1 {
			t.Fatalf("%s: samples=%v errors=%d", costNames[kind], got, c.timings.failed[kind])
		}
	}
	c.timings.add(costWhole, time.Now(), nil)
	c.timings.add(costGate, time.Now(), failure)
	if len(c.timings.samples[costWhole]) != 1 || c.timings.failed[costGate] != 1 {
		t.Fatal("snapshot or gate timing lost")
	}
	c.timings.reset()
	for kind := range costKinds {
		if len(c.timings.samples[kind]) != 0 || c.timings.failed[kind] != 0 {
			t.Fatal("initial measurements retained")
		}
	}
}

func TestCostAttributionDatabase(t *testing.T) {
	fixturePool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, fixturePool, "acme", "general")
	config := fixturePool.Config()
	counter := &epochCounter{QueryCounter: platform.NewQueryCounter(), timings: &costTimings{}}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, costPageCountsIn)
	c := &costConnection{reader: reader, member: m, channel: f.Channel.ID, timings: counter.timings, slots: make(chan struct{}, 1)}
	c.slots <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Recount(ctx, realtime.Subscription{}); !errors.Is(err, context.Canceled) || len(counter.timings.samples[costWhole]) != 0 {
		t.Fatal("cancelled slot wait started a snapshot")
	}
	<-c.slots
	if _, err := c.Recount(t.Context(), realtime.Subscription{}); err != nil || len(c.slots) != 0 {
		t.Fatalf("recount or slot release: %v", err)
	}
	events := &countingReader{inner: realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t)), trace: true}
	if _, err := events.EventsAfter(t.Context(), f.OrganizationID, 0, 100); err != nil {
		t.Fatal(err)
	}
	a := &timingAuthorizer{inner: orgpg.NewCachedAuthorizer(t.Context(), pool, org.DefaultAuthorizationCapacity)}
	if allowed, err := a.MayReceive(t.Context(), f.AccountID, "acme", realtime.Event{OrganizationID: f.OrganizationID}); err != nil || !allowed {
		t.Fatalf("authorization: %t, %v", allowed, err)
	}
	if counter.active.Load() != 0 || counter.peak.Load() != 1 {
		t.Fatal("successful acquire/release peak accounting wrong")
	}
	measured := counter.timings.take()
	for _, kind := range []int{costSidebar, costEvents, costRanges, costChannelCount, costTopicState, costTopicCount, costGetChannel, costListChannels, costListTopics, costWitness, costBegin, costCommit, costWhole} {
		want := 1
		if kind == costWitness {
			want = 2
		} // Snapshot sequence plus the test-only witness.
		if len(measured.samples[kind]) != want {
			t.Fatalf("%s: want %d real operations, got %d", costNames[kind], want, len(measured.samples[kind]))
		}
	}
	if len(measured.samples[costAuth]) == 0 || len(measured.samples[costOther]) != 0 || measured.failed[costGate] != 1 {
		t.Fatal("authorization attribution, unrelated work or cancelled gate accounting wrong")
	}
}
