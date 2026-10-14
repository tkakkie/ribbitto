package realtime_test

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
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

type hybridProgressCapture struct {
	frame hybridFrame
	want  sidebarOracleModel
}

func TestHybridDeliveryProgress(t *testing.T) {
	for _, interval := range []time.Duration{0, 10 * time.Second, 30 * time.Second, 60 * time.Second} {
		t.Run(fmt.Sprint(interval), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "progress", "general")
				account := identitytest.Account(t, pool, "reader@example.org", "Reader")
				member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
				m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: member}}
				conversationtest.Channel(t, pool, f.OrganizationID, "zero", false)
				var selected kernel.ID
				for i := range 50 {
					selected = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("Topic%02d", i)).ID
				}
				exec := func(t *testing.T, sql string, args ...any) {
					t.Helper()
					if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
						t.Fatal(err)
					}
				}
				exec(t, `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,'fixture',s FROM generate_series(2,107) s
UNION ALL SELECT $1::uuid,$2::uuid,$5::uuid,$4::uuid,'selected',108`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID, selected)
				exec(t, `INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)`, f.OrganizationID, f.Channel.ID, member)
				exec(t, `UPDATE organization SET event_seq=108,event_log_boundary_seq=108 WHERE id=$1`, f.OrganizationID)
				sub := realtime.Subscription{Organization: f.OrganizationID, OrganizationSlug: "progress", Account: account, Member: member, Channel: f.Channel.ID,
					Interests: []realtime.Interest{realtime.InterestMessages, realtime.InterestSidebar}}
				hub, q, changes := realtime.NewHub(), realtime.NewRecountScheduler(2, 4), new(unread.Changes)
				defer q.Close()
				captures, compute := make(chan *hybridProgressCapture, 8), make(chan struct{})
				snapshot := func(ctx context.Context) (hybridFrame, error) {
					tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
					if err != nil {
						return hybridFrame{}, err
					}
					defer func() { _ = tx.Rollback(ctx) }()
					c := new(hybridProgressCapture)
					// This first read pins both algorithms before any gated posts.
					err = tx.QueryRow(ctx, `SELECT event_seq FROM organization WHERE id=$1`, sub.Organization).Scan(&c.frame.boundary.sequence)
					if err != nil {
						return hybridFrame{}, err
					}
					captures <- c
					select {
					case <-compute:
					case <-ctx.Done():
						return hybridFrame{}, ctx.Err()
					}
					c.frame.model, err = hybridSnapshotIn(ctx, tx, m, sub.Channel, &selected)
					if err == nil {
						c.want, err = sidebarOracleIn(ctx, tx, sub.Organization, sub.Member, sub.Channel, &selected)
					}
					if err == nil {
						err = tx.Commit(ctx)
					}
					return c.frame, err
				}
				d := startHybridDelivery(t, hub, q, changes, sub, 1, snapshot)
				render := make(chan struct{}, 4*realtime.DefaultBatchSize)
				defer func() { close(render); d.close() }()
				capture := func() *hybridProgressCapture {
					t.Helper()
					synctest.Wait()
					select {
					case c := <-captures:
						return c
					default:
						t.Fatal("continuous posting postponed recount beyond fixed deadline")
						return nil
					}
				}
				finish := func() *hybridDeliveryRequest {
					t.Helper()
					compute <- struct{}{}
					synctest.Wait()
					select {
					case r := <-d.started:
						return r
					default:
						t.Fatal("gated snapshot did not finish computation")
						return nil
					}
				}
				check := func(c *hybridProgressCapture, r *hybridDeliveryRequest, count int) {
					t.Helper()
					synctest.Wait()
					sidebars := 0
					for _, out := range d.frames {
						if out.Name == "sidebar" {
							sidebars++
						}
					}
					if sidebars != count || d.coverage.frame.request != r.frame.request {
						t.Fatalf("completion starved behind continuous posts: sidebars=%d want=%d", sidebars, count)
					}
					if d.coverage.frame.boundary != c.frame.boundary || d.coverage.frame.watch != r.frame.watch {
						t.Fatal("delivery did not retain its pinned boundary and watch capture")
					}
					if err := sidebarOracleCompare(d.coverage.frame.model, c.want); err != nil {
						t.Fatalf("delivered full model at pinned boundary: %v", err)
					}
				}
				initial := capture()
				first := finish()
				first.release()
				check(initial, first, 1)
				if initial.want.channels[0].count.raw != 107 || initial.want.channels[0].count.capped != 100 || len(initial.want.topics) != 50 || initial.want.topics[49].id == selected {
					t.Fatal("progress fixture lost cap or off-list selection coverage")
				}
				// Repair-only arm deliberately loses post triggers and read wakes.
				// An unconditional test-only ticker wakes the real member watch;
				// neither posting nor completion can move its 10/30/60s deadlines.
				if interval != 0 {
					d.trigger = func(realtime.Event) bool { return false }
					ticker := time.NewTicker(interval)
					defer ticker.Stop()
					repairCtx, stop := context.WithCancel(t.Context())
					defer stop()
					go func() {
						for {
							select {
							case <-ticker.C:
								changes.ReadChanged(sub.Organization, sub.Member)
							case <-repairCtx.Done():
								return
							}
						}
					}()
				}
				point := int64(108)
				post := func(n int) {
					t.Helper()
					exec(t, `WITH advanced AS (
UPDATE organization SET event_seq=event_seq+$2,event_log_boundary_seq=event_seq+$2 WHERE id=$1 RETURNING event_seq)
INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1,$3,$4,$5,'continuous',s FROM advanced,generate_series(event_seq-$2+1,event_seq) s`, sub.Organization, n, sub.Channel, f.Channel.DefaultTopicID, f.MemberID)
					point += int64(n)
					d.seq = point
					hub.Raise(sub.Organization, point)
					synctest.Wait()
				}
				start := time.Now()
				for cycle := 1; cycle <= 2; cycle++ {
					// Lost persisted read input includes a visible selected badge on
					// cycle one, then raw-only drift above the cap on cycle two.
					hi := int64(109)
					if cycle == 2 {
						hi = point + 1
					}
					exec(t, `INSERT INTO read_range (organization_id,member_id,channel_id,lo,hi) VALUES ($1,$2,$3,$4,$5)`, sub.Organization, sub.Member, sub.Channel, hi-1, hi)
					step := realtime.SidebarInterval / 4
					deadline := time.Now().Add(realtime.SidebarInterval)
					if interval != 0 {
						deadline = start.Add(time.Duration(cycle) * interval)
					}
					for time.Until(deadline) > step {
						post(1)
						<-time.After(step)
					}
					post(1)
					<-time.After(time.Until(deadline) - time.Nanosecond)
					synctest.Wait()
					if len(captures) != 0 {
						t.Fatal("repair/recount ran before fixed deadline")
					}
					<-time.After(time.Nanosecond)
					c := capture()
					if c.frame.boundary != (hybridBoundary{sequence: point}) {
						t.Fatal("recount did not pin last committed posting boundary")
					}
					d.renderGate = render
					post(1) // Persist and deliver input during the pinned computation.
					if interval == 0 {
						changes.ReadChanged(sub.Organization, sub.Member)
						synctest.Wait()
					}
					r := finish()
					watch := uint64(cycle - 1)
					if interval != 0 {
						watch++
					}
					if r.frame.watch != watch {
						t.Fatal("recount/periodic repair lost request-local watch generation")
					}
					if sidebarOracleCompare(d.coverage.frame.model, c.want) == nil {
						t.Fatal("lost input did not leave drift for repair")
					}
					post(2 * realtime.DefaultBatchSize) // More persisted input before send.
					r.release()
					synctest.Wait()
					for range realtime.DefaultBatchSize {
						render <- struct{}{}
					}
					check(c, r, cycle+1) // The writer is blocked in the next batch.
					if time.Now() != deadline || q.Stats().RunningWorkers != 0 || hub.Connections() != 1 {
						t.Fatal("completion exceeded bounded virtual-time/lifecycle budget")
					}
					later, err := sidebarOracle(t.Context(), pool, sub.Organization, sub.Member, sub.Channel, &selected)
					if err != nil {
						t.Fatal(err)
					}
					if sidebarOracleCompare(c.want, later) == nil {
						t.Fatal("pinned oracle failed to distinguish continuous later posting")
					}
					// Drain without advancing time; no additional sidebar may appear.
					last := int64(0)
					for _, out := range d.frames {
						if out.Name == "message" {
							last = out.ID
						}
					}
					if point-last <= 0 {
						t.Fatal("completion was checked only after durable backlog drained")
					}
					for range point - last {
						render <- struct{}{}
					}
					check(c, r, cycle+1)
					d.renderGate = nil
				}
			})
		})
	}
}
