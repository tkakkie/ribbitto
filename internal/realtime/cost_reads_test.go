package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

const costChangedReadRate = 100

// Each round consumes one pre-existing message per member. Capacity therefore
// depends on the whole window, never on posts arriving fast enough to supply it.
type costReadPlan struct {
	streams, scheduled, capacity int
}

func newCostReadPlan(streams int, duration time.Duration) (costReadPlan, error) {
	if streams < 1 || duration <= 0 {
		return costReadPlan{}, fmt.Errorf("changed reads need streams and a positive window")
	}
	n := scheduledPosts(costChangedReadRate, duration)
	if n < 1 {
		return costReadPlan{}, fmt.Errorf("changed-read window schedules no work")
	}
	return costReadPlan{streams, n, (n + streams - 1) / streams}, nil
}

func (p costReadPlan) target(i int, first int64) (int, int64) {
	return i % p.streams, first + int64(i/p.streams)
}

// Seed before streams/counters start, on a fresh fixture after all members join.
// Synthetic persisted history is not a measured post or a durable notification.
func (p costReadPlan) seed(ctx context.Context, pool *pgxpool.Pool, author org.Membership, channel, topic kernel.ID) (int64, error) {
	var last int64
	err := pool.QueryRow(ctx, `WITH advanced AS (
UPDATE organization SET event_seq=event_seq+$2,event_log_boundary_seq=event_seq+$2
WHERE id=$1 RETURNING event_seq
), seeded AS (
INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1,$3,$4,$5,'changed-read seed',s
FROM advanced,generate_series(event_seq-$2+1,event_seq) s
)
SELECT event_seq FROM advanced`, author.Organization.ID, p.capacity, channel, topic, author.Member.ID).Scan(&last)
	return last - int64(p.capacity) + 1, err
}

// Capture the production writer's locked message-existence result, then require
// a successful commit. A watch wake alone cannot prove persistence (unknown
// commits also wake watches). Match Reading's detached five-second completion.
func costChangedRead(ctx context.Context, pool *pgxpool.Pool, changes *unread.Changes, m org.Membership, channel kernel.ID, cursor int64) (bool, error) {
	feed := unreadpg.NewFeedWriter(
		func(tx platform.Tx) unread.ChannelMessages { return conversationpg.MessageSequencesIn(tx) },
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) })
	var changed bool
	err := platform.InTxDetachedCommit(ctx, pool, 5*time.Second, func(tx platform.Tx) error {
		var err error
		changed, err = feed.Read(ctx, tx, unread.Scope{OrganizationID: m.Organization.ID, MemberID: m.Member.ID, ChannelID: channel}, m.Member.JoinedEventSeq, cursor)
		return err
	})
	if changed && (err == nil || errors.Is(err, platform.ErrCommitUnknown)) {
		changes.ReadChanged(m.Organization.ID, m.Member.ID)
	}
	return changed && err == nil, err
}

type costReadResult struct {
	scheduled, changed, noop, missed, failed int
}

func (r costReadResult) complete() bool {
	return r.scheduled > 0 && r.changed == r.scheduled && r.noop == 0 && r.missed == 0 && r.failed == 0
}

// Both arms share this open-loop load. A one-second global admission budget and
// one operation per member prevent overload or cursor reordering from silently
// lowering the offered rate. No retries turn no-ops/failures into changed reads.
// The caller's context covers the window plus drain; return joins every worker.
func (p costReadPlan) run(ctx context.Context, start time.Time, first int64, read func(context.Context, int, int64) (bool, error)) costReadResult {
	r := costReadResult{scheduled: p.scheduled}
	var mu sync.Mutex
	var workers sync.WaitGroup
	busy := make([]bool, p.streams)
	active := 0
	for i := range p.scheduled {
		timer := time.NewTimer(time.Until(start.Add(time.Duration(i) * (time.Second / costChangedReadRate))))
		select {
		case <-ctx.Done():
			timer.Stop()
			mu.Lock()
			r.missed += p.scheduled - i
			mu.Unlock()
			workers.Wait()
			return r
		case <-timer.C:
		}
		member, cursor := p.target(i, first)
		mu.Lock()
		if busy[member] || active == costChangedReadRate {
			r.missed++
			mu.Unlock()
			continue
		}
		busy[member], active = true, active+1
		mu.Unlock()
		workers.Go(func() {
			changed, err := read(ctx, member, cursor)
			mu.Lock()
			defer mu.Unlock()
			busy[member], active = false, active-1
			switch {
			case err != nil:
				r.failed++
			case changed:
				r.changed++
			default:
				r.noop++
			}
		})
	}
	workers.Wait()
	return r
}

// Gate all writes before capturing independent final truth, outside application
// SQL totals. Sequence coverage alone cannot detect a read-only stale snapshot.
func costReadDrain(connections []*costModelConnection, want []sidebarOracleModel) error {
	if len(connections) == 0 || len(connections) != len(want) {
		return fmt.Errorf("changed-read drain lost connection witnesses")
	}
	for i, c := range connections {
		if err := c.exact(want[i]); err != nil {
			return fmt.Errorf("changed-read drain stream %d: %w", i, err)
		}
	}
	return nil
}
