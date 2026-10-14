package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

type hybridDeliveryRequest struct {
	ctx     context.Context
	frame   hybridFrame
	resume  chan struct{}
	release func()
}

type hybridDeliveryWatch struct {
	realtime.SidebarWatch
	releases int
}

func (w *hybridDeliveryWatch) Release() { w.releases++; w.SidebarWatch.Release() }

// Computations carry model witnesses, not HTML. The exported stream remains
// the sole writer; the cost probe consumes a fresh bound-member grant per send.
type hybridDelivery struct {
	*costWriterProbe
	hub        *realtime.Hub
	changes    *unread.Changes
	watch      *hybridDeliveryWatch
	coverage   hybridCoverage
	snapshot   func(context.Context) (hybridFrame, error)
	started    chan *hybridDeliveryRequest
	done       chan costWriterExit
	disconnect context.CancelFunc
	mu         sync.Mutex
	requests   []*hybridDeliveryRequest
	trigger    func(realtime.Event) bool
	cost       bool
}

func (d *hybridDelivery) Watch(organization, member kernel.ID) realtime.SidebarWatch {
	if organization != d.sub.Organization || member != d.sub.Member {
		d.t.Error("hybrid watch lost bound scope")
	}
	d.watch = &hybridDeliveryWatch{SidebarWatch: d.changes.Watch(organization, member)}
	return d.watch
}

func (d *hybridDelivery) Trigger(e realtime.Event) bool {
	if d.trigger != nil {
		return d.trigger(e)
	}
	return true
}

func (d *hybridDelivery) Recount(ctx context.Context, sub realtime.Subscription) (realtime.Outgoing, error) {
	if sub.Organization != d.sub.Organization || sub.Member != d.sub.Member || d.watch == nil {
		d.t.Error("hybrid recount preceded scoped subscription")
	}
	generation, _ := d.watch.Snapshot()
	frame, err := d.snapshot(ctx)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	d.mu.Lock()
	frame.incarnation, frame.request, frame.watch = d.coverage.frame.incarnation, uint64(len(d.requests)+1), generation
	r := &hybridDeliveryRequest{ctx: ctx, frame: frame, resume: make(chan struct{})}
	r.release = sync.OnceFunc(func() { close(r.resume) })
	d.requests = append(d.requests, r)
	d.mu.Unlock()
	d.started <- r
	<-r.resume // Deliberately ignore cancellation to expose late completions.
	return realtime.Outgoing{ID: 999, Name: "untrusted", Data: []byte(fmt.Sprint(frame.request))}, nil
}

func (d *hybridDelivery) Send(ctx context.Context, out realtime.Outgoing) error {
	if ctx.Err() != nil || d.deny.Load() {
		d.t.Error("hybrid send survived cancellation or revocation")
	}
	if out.Name == "sidebar" && !d.cost {
		d.mu.Lock()
		var frame hybridFrame
		for _, r := range d.requests {
			if string(out.Data) == fmt.Sprint(r.frame.request) {
				frame = r.frame
			}
		}
		d.mu.Unlock()
		if out.ID != 0 || !out.Ephemeral || d.coverage.install(frame) != "applied" {
			d.t.Error("hybrid sidebar send lost flags or accepted stale completion")
		}
	}
	return d.costWriterProbe.Send(ctx, out)
}

func (d *hybridDelivery) Heartbeat(ctx context.Context) error {
	return d.Send(ctx, realtime.Outgoing{Name: "heartbeat", Ephemeral: true})
}

func (d *hybridDelivery) close() {
	d.disconnect()
	d.mu.Lock()
	for _, r := range d.requests {
		r.release()
	}
	d.mu.Unlock()
	synctest.Wait()
}

func startHybridDelivery(t *testing.T, hub *realtime.Hub, q *realtime.RecountScheduler, changes *unread.Changes, sub realtime.Subscription, incarnation uint64, snapshot func(context.Context) (hybridFrame, error), heartbeat ...time.Duration) *hybridDelivery {
	return startHybridDeliveryArm(t, hub, q, changes, sub, incarnation, snapshot, false, heartbeat...)
}

func startHybridDeliveryArm(t *testing.T, hub *realtime.Hub, q *realtime.RecountScheduler, changes *unread.Changes, sub realtime.Subscription, incarnation uint64, snapshot func(context.Context) (hybridFrame, error), cost bool, heartbeat ...time.Duration) *hybridDelivery {
	t.Helper()
	d := &hybridDelivery{costWriterProbe: &costWriterProbe{t: t, sub: sub}, hub: hub, changes: changes,
		coverage: hybridCoverage{frame: hybridFrame{incarnation: incarnation}}, snapshot: snapshot,
		started: make(chan *hybridDeliveryRequest, 8), done: make(chan costWriterExit, 1)}
	d.cost = cost
	parent, cancel := context.WithCancel(t.Context())
	d.disconnect = cancel
	ctx, unregister, err := hub.Register(parent, realtime.Connection{Organization: sub.Organization, Account: sub.Account, Session: kernel.ID{9}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	stream := realtime.Stream{Hub: hub, Events: d, Renderer: d, Authorizer: d, Sidebar: d, Recounts: q}
	if len(heartbeat) != 0 {
		stream.Heartbeat = heartbeat[0]
	}
	var sender realtime.Sender = d
	if cost {
		c := &costHybridConnection{costModelConnection: &costModelConnection{changes: changes, sender: d}, organization: sub.Organization, incarnation: incarnation, coverage: &d.coverage, watcher: d}
		c.snapshot = func(ctx context.Context) (hybridFrame, error) {
			_, err := d.Recount(ctx, sub)
			d.mu.Lock()
			defer d.mu.Unlock()
			if err != nil {
				return hybridFrame{}, err
			}
			return d.requests[len(d.requests)-1].frame, nil
		}
		stream.Sidebar, sender = c, c
	}
	go func() {
		defer unregister()
		cursor, err := stream.Run(ctx, sub, 0, sender)
		d.done <- costWriterExit{cursor, err}
	}()
	return d
}

func TestHybridDelivery(t *testing.T) { testHybridDelivery(t, false) }

func TestCostHybridDelivery(t *testing.T) { testHybridDelivery(t, true) }

func testHybridDelivery(t *testing.T, cost bool) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "delivery", "general")
	account := identitytest.Account(t, pool, "reader@example.org", "Reader")
	member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: member}}
	sub := realtime.Subscription{Organization: f.OrganizationID, OrganizationSlug: "delivery", Account: account, Member: member, Channel: f.Channel.ID,
		Interests: []realtime.Interest{realtime.InterestMessages, realtime.InterestSidebar}}
	snapshot := func(ctx context.Context) (hybridFrame, error) {
		if cost {
			return costModelSnapshot(ctx, pool, m, sub.Channel, &f.Channel.DefaultTopicID, nil)
		}
		model, err := hybridSnapshot(ctx, pool, m, f.Channel.ID, &f.Channel.DefaultTopicID)
		return hybridFrame{model: model}, err
	}
	for _, scenario := range []string{"order", "audience deny", "audience revoke", "render revoke", "completion revoke", "heartbeat revoke", "reconnect"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hub, q, changes := realtime.NewHub(), realtime.NewRecountScheduler(2, 4), new(unread.Changes)
				defer q.Close()
				heartbeat := time.Duration(0)
				if scenario == "heartbeat revoke" {
					heartbeat = realtime.SidebarInterval
				}
				d := startHybridDeliveryArm(t, hub, q, changes, sub, 1, snapshot, cost, heartbeat)
				defer d.close()
				first := <-d.started
				synctest.Wait()
				if hub.Connections() != 1 {
					t.Fatal("hybrid stream did not retain hub registration")
				}
				if scenario == "order" || scenario == "reconnect" {
					first.release()
					synctest.Wait()
					if len(d.frames) != 1 || d.coverage.frame.watch != 0 {
						t.Fatal("initial hybrid completion was not sent")
					}
					changes.ReadChanged(sub.Organization, sub.Member)
					synctest.Wait()
					<-time.After(realtime.SidebarInterval) // Only the bubble's virtual deadline advances.
					first = <-d.started
					synctest.Wait()
					if first.frame.watch != 1 {
						t.Fatal("hybrid request lost captured generation")
					}
				}
				if scenario == "reconnect" {
					d.disconnect()
					synctest.Wait()
					got := <-d.done
					if !errors.Is(got.err, context.Canceled) || got.cursor != 0 || first.ctx.Err() == nil || d.watch.releases != 1 || hub.Connections() != 0 {
						t.Fatalf("disconnect did not cancel work/release lifecycle: exit=%+v work=%v releases=%d connections=%d", got, first.ctx.Err(), d.watch.releases, hub.Connections())
					}
					// No subscriber retains an invalidation; reconnect must read again.
					if _, err := pool.Exec(t.Context(), `WITH advanced AS (
UPDATE organization SET event_seq=event_seq+1,event_log_boundary_seq=event_seq+1 WHERE id=$1 RETURNING event_seq)
INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1,$2,$3,$4,'offline',event_seq FROM advanced`, sub.Organization, sub.Channel, f.Channel.DefaultTopicID, f.MemberID); err != nil {
						t.Fatal(err)
					}
					changes.ReadChanged(sub.Organization, sub.Member)
					reconnected := startHybridDeliveryArm(t, hub, q, changes, sub, 2, snapshot, cost)
					defer reconnected.close()
					fresh := <-reconnected.started
					if fresh.frame.watch != 0 || fresh.frame.request != 1 {
						t.Fatal("reconnect inherited old request/watch generation")
					}
					fresh.release()
					synctest.Wait()
					if len(reconnected.frames) != 1 || reconnected.coverage.frame.incarnation != 2 || sidebarOracleCompare(reconnected.coverage.frame.model, d.coverage.frame.model) == nil {
						t.Fatal("reconnect failed to recover through fresh completion")
					}
					first.release()
					synctest.Wait()
					if len(d.frames) != 1 || len(reconnected.frames) != 1 || q.Stats().Registrations != 1 || q.Stats().RunningWorkers != 0 {
						t.Fatal("late completion survived old stream exit or polluted reconnect")
					}
					before := hybridClone(reconnected.coverage.frame.model)
					if reconnected.coverage.install(first.frame) != "ignored" || sidebarOracleCompare(reconnected.coverage.frame.model, before) != nil {
						t.Fatal("reconnect accepted old incarnation completion")
					}
					return
				}
				if scenario == "heartbeat revoke" {
					for range 2 {
						<-time.After(realtime.SidebarInterval)
						synctest.Wait()
					}
					if len(d.frames) != 2 || d.frames[0].Name != "heartbeat" || d.frames[1].Name != "heartbeat" {
						t.Fatal("held recount blocked authorized heartbeats")
					}
					d.deny.Store(true)
					<-time.After(realtime.SidebarInterval)
					synctest.Wait()
					select {
					case got := <-d.done:
						if got.cursor != 0 || !errors.Is(got.err, realtime.ErrMembershipRevoked) || len(d.frames) != 2 || first.ctx.Err() == nil {
							t.Fatalf("heartbeat revocation failed: %+v frames=%v", got, d.frames)
						}
					default:
						t.Fatal("heartbeat revocation did not stop stream")
					}
					first.release()
					synctest.Wait()
					if len(d.frames) != 2 {
						t.Fatal("late completion sent after heartbeat revocation")
					}
					return
				}
				if scenario == "completion revoke" {
					d.deny.Store(true)
					first.release()
				} else {
					if scenario == "render revoke" {
						d.renderGate = make(chan struct{})
						defer close(d.renderGate)
					}
					if scenario == "audience deny" || scenario == "audience revoke" {
						other := kernel.ID{5}
						d.audiences = map[int64]*kernel.ID{1: &other}
						d.deny.Store(scenario == "audience revoke")
					}
					d.seq = 3
					hub.Raise(sub.Organization, d.seq)
					synctest.Wait()
					if scenario == "render revoke" {
						d.deny.Store(true)
						first.release() // Buffer a completion behind the durable render.
						synctest.Wait()
						d.renderGate <- struct{}{}
					}
				}
				synctest.Wait()
				if scenario == "order" || scenario == "audience deny" {
					first.release()
					synctest.Wait()
					var ids []int64
					for _, frame := range d.frames {
						if frame.Name == "message" {
							ids = append(ids, frame.ID)
						}
					}
					want := []int64{1, 2, 3}
					if scenario == "audience deny" {
						want = []int64{2, 3}
					}
					if !slices.Equal(ids, want) {
						t.Fatalf("hybrid durable send order = %v, want %v", ids, want)
					}
					if d.frames[len(d.frames)-1].Name != "sidebar" || d.coverage.frame.request != first.frame.request || d.coverage.frame.watch != first.frame.watch {
						t.Fatal("hybrid completion lost send order or generation coverage")
					}
					d.disconnect()
				} else {
					select {
					case got := <-d.done:
						if got.cursor != 0 || !errors.Is(got.err, realtime.ErrMembershipRevoked) || len(d.frames) != 0 {
							t.Fatalf("revocation sent output or advanced cursor: %+v frames=%v", got, d.frames)
						}
					default:
						t.Fatal("hybrid revocation did not stop stream")
					}
					if scenario == "audience revoke" && !slices.Equal(d.checks, []kernel.ID{{5}, sub.Member}) {
						t.Fatal("audience denial bypassed bound-member revocation")
					}
					first.release()
					synctest.Wait()
					if len(d.frames) != 0 {
						t.Fatal("late completion sent after revocation")
					}
					return
				}
				synctest.Wait()
				if got := <-d.done; got.cursor != 3 || !errors.Is(got.err, context.Canceled) || d.watch.releases != 1 || hub.Connections() != 0 {
					t.Fatalf("hybrid delivery exit: %+v releases=%d connections=%d", got, d.watch.releases, hub.Connections())
				}
			})
		})
	}
}
