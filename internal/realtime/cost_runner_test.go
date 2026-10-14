package realtime_test

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

// #952 shares fixtures and load, but keeps (a)'s fair queue and (b)'s production
// scheduler. Oracle truth uses the fixture pool after all writes have joined.
type costRunner struct {
	arm         string
	plan        costReadPlan
	first       int64
	pool, truth *pgxpool.Pool
	members     []org.Membership
	subs        []realtime.Subscription
	changes     *unread.Changes
	models      []*costModelConnection
	hybrids     []*costHybridConnection
	senders     []*costRunnerSender
	snapshot    func(context.Context, org.Membership, realtime.Subscription) (hybridFrame, error)

	persistMu        sync.Mutex
	changesPersisted []costChange
	initial          []sidebarOracleModel
	history          []costExpectedMessage
	topic            kernel.ID
}

func costModelArm(arm string) bool { return arm == "baseline" || arm == "hybrid" }

func newCostRunner(ctx context.Context, arm string, pool, truth *pgxpool.Pool, members []org.Membership, subs []realtime.Subscription, topic kernel.ID, duration, repair time.Duration) (*costRunner, error) {
	plan, err := newCostReadPlan(len(members), duration)
	if err != nil {
		return nil, err
	}
	first, err := plan.seed(ctx, truth, members[0], subs[0].Channel, topic)
	if err != nil {
		return nil, err
	}
	r := &costRunner{topic: topic, arm: arm, plan: plan, first: first, pool: pool, truth: truth, members: members, subs: subs, changes: new(unread.Changes)}
	r.snapshot = func(ctx context.Context, m org.Membership, sub realtime.Subscription) (hybridFrame, error) {
		return costModelSnapshot(ctx, pool, m, sub.Channel, sub.Topic, nil)
	}
	for i, m := range members {
		sub := subs[i]
		c := &costModelConnection{changes: r.changes, snapshot: func(ctx context.Context) (hybridFrame, error) { return r.snapshot(ctx, m, sub) }}
		r.models = append(r.models, c)
		r.senders = append(r.senders, &costRunnerSender{})
		if arm == "hybrid" {
			r.hybrids = append(r.hybrids, &costHybridConnection{costModelConnection: c, organization: sub.Organization, incarnation: 1, interval: repair, coverage: &hybridCoverage{frame: hybridFrame{incarnation: 1}}})
		}
	}
	return r, nil
}

func (r *costRunner) wire(i int, stream realtime.Stream, sink *collector, scheduler *realtime.RecountScheduler) (realtime.Stream, realtime.Subscription, realtime.Sender) {
	sub := r.subs[i]
	sub.Interests = []realtime.Interest{realtime.InterestMessages, realtime.InterestSidebar}
	r.senders[i].Sender = sink
	r.models[i].sender = r.senders[i]
	stream.Heartbeat = 0 // Rejected hybrid completions also reset production's timer.
	stream.Sidebar = r.models[i]
	var sender realtime.Sender = r.models[i]
	if r.arm == "hybrid" {
		stream.Sidebar, stream.Recounts, sender = r.hybrids[i], scheduler, r.hybrids[i]
	}
	return stream, sub, sender
}

func (r *costRunner) reads(ctx context.Context, start time.Time) costReadResult {
	return r.plan.run(ctx, start, r.first, func(ctx context.Context, member int, cursor int64) (bool, error) {
		p := new(costPersist)
		changed, err := costChangedRead(context.WithValue(ctx, costPersistKey{}, p), r.pool, r.changes, r.members[member], r.subs[member].Channel, cursor)
		if changed && err == nil {
			r.persisted(costChange{stream: member, readThrough: cursor}, p)
		}
		return changed, err
	})
}

func (r *costRunner) capture(ctx context.Context) ([]sidebarOracleModel, error) {
	want := make([]sidebarOracleModel, len(r.models))
	for i, sub := range r.subs {
		var err error
		want[i], err = sidebarOracle(ctx, r.truth, sub.Organization, sub.Member, sub.Channel, sub.Topic)
		if err != nil {
			return nil, err
		}
	}
	return want, nil
}

func (r *costRunner) rejections() costHybridRejections {
	var total costHybridRejections
	for _, c := range r.hybrids {
		n := c.rejections()
		total.repair += n.repair
		total.ignored += n.ignored
	}
	return total
}

// Sender-side successes are evidence; StreamObserver.Delivered includes rejected
// hybrid completions. Atomic totals can be captured while writers are active.
type costRunnerSender struct {
	realtime.Sender
	snapshots, deltas atomic.Int64
}

func (s *costRunnerSender) Send(ctx context.Context, out realtime.Outgoing) error {
	if err := s.Sender.Send(ctx, out); err != nil {
		return err
	}
	switch out.Name {
	case "sidebar":
		s.snapshots.Add(1)
	case "sidebar-delta":
		s.deltas.Add(1)
	}
	return nil
}

// SQL shares include attempted transaction controls, regardless of outcome.
// The existing pool tracer attributes recounts by role; fixture pools are untraced.
type costAccounting struct {
	sidebarSQL, otherSQL, totalSQL  int64
	computations, snapshots, deltas int64
}

func (r *costRunner) accounting(queries *epochCounter) costAccounting {
	queries.sqlMu.Lock()
	q := queries.Counts()
	total := costAccounting{sidebarSQL: queries.sidebarSQL, totalSQL: q.Queries + q.Begins + q.Commits + q.Rollbacks}
	total.otherSQL = total.totalSQL - total.sidebarSQL
	queries.sqlMu.Unlock()
	for i, c := range r.models {
		total.computations += c.computations.Load()
		total.snapshots += r.senders[i].snapshots.Load()
		total.deltas += r.senders[i].deltas.Load()
	}
	return total
}

func (a costAccounting) since(before costAccounting) costAccounting {
	return costAccounting{
		sidebarSQL: a.sidebarSQL - before.sidebarSQL, otherSQL: a.otherSQL - before.otherSQL, totalSQL: a.totalSQL - before.totalSQL,
		computations: a.computations - before.computations, snapshots: a.snapshots - before.snapshots, deltas: a.deltas - before.deltas,
	}
}
