package realtime_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

// These are sidebar coverage, never the durable delivery cursor. The read
// revision is hypothetical persisted metadata, not a production watch token.
type hybridBoundary struct{ sequence, revision int64 }
type hybridFrame struct {
	boundary                    hybridBoundary
	incarnation, request, watch uint64
	model                       sidebarOracleModel
}
type hybridExact struct {
	before, after  hybridBoundary
	incarnation    uint64
	channel, topic kernel.ID
	change         int64
}
type hybridCoverage struct {
	frame    hybridFrame
	watch    uint64
	retained []hybridExact
}

func hybridClone(m sidebarOracleModel) sidebarOracleModel {
	m.channels, m.topics, m.selection = slices.Clone(m.channels), slices.Clone(m.topics), slices.Clone(m.selection)
	return m
}

func hybridCovered(a, b hybridBoundary) bool {
	return a.sequence >= b.sequence && a.revision >= b.revision
}

func hybridEffect(m *sidebarOracleModel, d hybridExact) {
	hybridAdjust(m.channels, d.channel, d.change, false)
	hybridAdjust(m.topics, d.topic, d.change, false)
	hybridAdjust(m.selection, d.topic, d.change, false)
}

// A gap repairs instead of guessing. No reorder buffer is needed; the retained
// exact chain is bounded and only used to prevent snapshot rollback.
func (c *hybridCoverage) delta(d hybridExact) string {
	if d.incarnation != c.frame.incarnation || hybridCovered(c.frame.boundary, d.after) {
		return "ignored"
	}
	if d.before != c.frame.boundary {
		return "repair"
	}
	hybridEffect(&c.frame.model, d)
	c.frame.boundary = d.after
	c.retained = append(c.retained, d)
	if len(c.retained) > 8 {
		c.retained = c.retained[1:]
	}
	return "applied"
}

func (c *hybridCoverage) install(f hybridFrame) string {
	if f.incarnation != c.frame.incarnation || f.request <= c.frame.request {
		return "ignored"
	}
	f.model = hybridClone(f.model)
	for !hybridCovered(f.boundary, c.frame.boundary) {
		i := slices.IndexFunc(c.retained, func(d hybridExact) bool { return d.before == f.boundary })
		if i < 0 {
			return "repair"
		}
		d := c.retained[i]
		hybridEffect(&f.model, d)
		f.boundary = d.after
	}
	f.watch = max(f.watch, c.frame.watch)
	c.frame = f
	return "applied"
}

func TestHybridCoverage(t *testing.T) {
	pool, ctx := pgtest.New(t), t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(sql string, args ...any) { t.Helper(); _, err := pool.Exec(ctx, sql, args...); must(err) }
	f := conversationtest.OrganizationWithOwner(t, pool, "coverage", "general")
	account := identitytest.Account(t, pool, "reader@example.org", "Reader")
	member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: member}}
	exec(`CREATE TABLE hybrid_read_metadata (revision bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
organization_id uuid NOT NULL,member_id uuid NOT NULL,channel_id uuid NOT NULL,coverage bytea,absolute_count bigint NOT NULL)`)
	exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1,$2,$3,$4,'fixture',s FROM generate_series(2,100) s`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	exec(`UPDATE organization SET event_seq=100,event_log_boundary_seq=100 WHERE id=$1`, f.OrganizationID)
	point := hybridBoundary{100, 0}
	c := &hybridCoverage{frame: hybridFrame{incarnation: 1}}
	// Each synthetic post commits its sequence and message together. Reads use
	// 960b's transaction and replayable receipt; no oracle supplies delta effects.
	post := func(noop bool) hybridExact {
		t.Helper()
		d := hybridExact{before: point, incarnation: 1, channel: f.Channel.ID, topic: f.Channel.DefaultTopicID, change: 1}
		point.sequence++
		must(pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE organization SET event_seq=$2,event_log_boundary_seq=$2 WHERE id=$1`, f.OrganizationID, point.sequence)
			if err == nil && !noop {
				_, err = tx.Exec(ctx, `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'post',$5)`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, point.sequence)
			}
			return err
		}))
		if noop {
			d.change = 0
		}
		d.after = point
		return d
	}
	readSeq := int64(2)
	read := func() hybridExact {
		t.Helper()
		d := hybridExact{before: point, incarnation: 1, channel: f.Channel.ID, topic: f.Channel.DefaultTopicID, change: -1}
		r, err := hybridReadTransaction(ctx, pool, "hypothetical-read-ranges", f.OrganizationID, member, f.Channel.ID, 1, []unread.Range{{Lo: readSeq, Hi: readSeq + 1}})
		must(err)
		replayed, err := hybridReplayReceipt(ctx, pool, r.revision)
		must(err)
		point.revision = replayed.revision
		readSeq++
		c.watch++
		d.after = point
		return d
	}
	q := realtime.NewRecountScheduler(26, 100)
	defer q.Close()
	p := newHybrid(q, time.Unix(0, 0), 0, nil)
	defer p.registration.Cancel()
	verdict := func(got, want string) {
		t.Helper()
		if got != want {
			t.Fatalf("coverage verdict = %s, want %s", got, want)
		}
	}
	check := func(want sidebarOracleModel) {
		t.Helper()
		must(sidebarOracleCompare(p.state, want))
		must(sidebarOracleCompare(c.frame.model, want))
	}
	// Pin candidate and oracle in the very same repeatable-read transaction.
	// Gates place writes before pinning, during computation and before apply/send.
	capture := func(interleave bool) hybridFrame {
		t.Helper()
		pin, pinned, compute, computed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		captured := hybridFrame{incarnation: 1, request: p.requested + 1, watch: c.watch}
		var want sidebarOracleModel
		p.snapshot = func(ctx context.Context) (sidebarOracleModel, error) {
			<-pin
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				return sidebarOracleModel{}, err
			}
			defer func() { _ = tx.Rollback(ctx) }()
			err = tx.QueryRow(ctx, `SELECT event_seq,(SELECT coalesce(max(revision),0) FROM hybrid_read_metadata WHERE organization_id=$1 AND member_id=$2) FROM organization WHERE id=$1`, f.OrganizationID, member).Scan(&captured.boundary.sequence, &captured.boundary.revision)
			close(pinned)
			<-compute
			if err != nil {
				return sidebarOracleModel{}, err
			}
			captured.model, err = hybridSnapshotIn(ctx, tx, m, f.Channel.ID, &f.Channel.DefaultTopicID)
			if err == nil {
				want, err = sidebarOracleIn(ctx, tx, f.OrganizationID, member, f.Channel.ID, &f.Channel.DefaultTopicID)
			}
			if err == nil {
				err = tx.Commit(ctx)
			}
			close(computed)
			return captured.model, err
		}
		p.accept = func(version uint64, model sidebarOracleModel) error {
			captured.request, captured.model = version, model
			outcome := c.install(captured)
			p.state = c.frame.model
			if outcome == "repair" {
				return p.reconcile("coverage unavailable")
			}
			return nil
		}
		must(p.reconcile("coverage checkpoint"))
		if interleave {
			post(false)
			post(false)
			read()
		}
		close(pin)
		<-pinned
		if interleave {
			post(false)
			post(false)
			read()
		}
		close(compute)
		<-computed
		if interleave {
			post(false)
			post(false)
			read()
		}
		must(p.complete())
		check(want)
		if interleave {
			later, err := sidebarOracle(ctx, pool, f.OrganizationID, member, f.Channel.ID, &f.Channel.DefaultTopicID)
			must(err)
			if sidebarOracleCompare(want, later) == nil {
				t.Fatal("pinned oracle failed to distinguish later persistence")
			}
		}
		if c.frame.boundary != captured.boundary || c.frame.watch != captured.watch {
			t.Fatal("snapshot coverage differs from pinned boundary")
		}
		return captured
	}
	old := capture(false)
	capture(true)
	if c.frame.watch >= c.watch {
		t.Fatal("later read publications were falsely covered")
	}
	// Even writes visible in a snapshot do not cover publications after G.
	capture(false)
	if c.frame.watch != c.watch {
		t.Fatal("repair did not cover observed watch generation")
	}
	apply := func(d hybridExact) {
		t.Helper()
		verdict(c.delta(d), "applied")
		p.state = c.frame.model
		want, err := sidebarOracle(ctx, pool, f.OrganizationID, member, f.Channel.ID, &f.Channel.DefaultTopicID)
		must(err)
		check(want)
	}
	p.condition = "hypothetical-exact-coverage"
	base := capture(false)
	d := post(false)
	apply(d)
	apply(post(false))
	apply(post(false))
	readDelta := read()
	apply(readDelta)
	apply(post(true))
	before := hybridClone(p.state)
	verdict(c.delta(d), "ignored")
	verdict(c.delta(readDelta), "ignored")
	check(before)
	// A newer request with an older persisted boundary must replay both axes.
	base.request = p.requested + 1
	verdict(c.install(base), "applied")
	p.state = c.frame.model
	check(before)
	if c.frame.boundary != point {
		t.Fatal("retained replay lost sequence or read revision")
	}
	watchFrame := c.frame
	watchFrame.request++
	watchFrame.watch--
	verdict(c.install(watchFrame), "applied")
	if c.frame.watch != watchFrame.watch+1 {
		t.Fatal("older watch generation rolled back coverage")
	}
	verdict(c.install(old), "ignored")
	old.request = c.frame.request + 1
	old.incarnation = 0
	verdict(c.install(old), "ignored")
	foreignDelta := hybridExact{before: point, after: hybridBoundary{point.sequence + 1, point.revision}, incarnation: 0, channel: f.Channel.ID, topic: f.Channel.DefaultTopicID, change: 1}
	verdict(c.delta(foreignDelta), "ignored")
	check(before)
	// Lost and reordered real inputs trigger repair without partially applying.
	for _, fault := range []string{"lost", "reordered", "lost-noop"} {
		t.Logf("coverage fault: %s", fault)
		first := post(fault == "lost-noop")
		wantFirst, err := sidebarOracle(ctx, pool, f.OrganizationID, member, f.Channel.ID, &f.Channel.DefaultTopicID)
		must(err)
		second := post(false)
		verdict(c.delta(second), "repair")
		check(before)
		if fault == "reordered" {
			verdict(c.delta(first), "applied")
			p.state = c.frame.model
			check(wantFirst)
		}
		p.requested = c.frame.request
		capture(false)
		verdict(c.delta(first), "ignored")
		verdict(c.delta(second), "ignored")
		before = hybridClone(p.state)
	}
	// Eviction and missing read coverage refuse an older snapshot, then the
	// scheduler obtains a fresh boundary. Both failed installs leave state intact.
	for _, fault := range []string{"evicted", "missing-read"} {
		t.Logf("coverage fault: %s", fault)
		base = capture(false)
		if fault == "evicted" {
			for range 9 {
				apply(post(false))
			}
		} else {
			apply(read())
			c.retained = nil
		}
		before = hybridClone(p.state)
		base.request = p.requested + 1
		verdict(c.install(base), "repair")
		check(before)
		staleAccept, requested := p.accept, p.requested
		p.snapshot = func(ctx context.Context) (sidebarOracleModel, error) {
			return hybridSnapshot(ctx, pool, m, f.Channel.ID, &f.Channel.DefaultTopicID)
		}
		p.accept = func(version uint64, model sidebarOracleModel) error {
			verdict(c.install(hybridFrame{boundary: point, incarnation: 1, request: version, watch: c.watch, model: model}), "applied")
			p.state = c.frame.model
			return nil
		}
		must(staleAccept(base.request, base.model))
		if p.requested != requested+1 {
			t.Fatal("unavailable coverage did not schedule reconciliation")
		}
		check(before)
		must(p.complete())
		want, err := sidebarOracle(ctx, pool, f.OrganizationID, member, f.Channel.ID, &f.Channel.DefaultTopicID)
		must(err)
		check(want)
	}
}
