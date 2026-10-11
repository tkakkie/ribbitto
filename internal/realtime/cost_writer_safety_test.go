package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type costWriterProbe struct {
	t          *testing.T
	sub        realtime.Subscription
	frames     []realtime.Outgoing
	sends      atomic.Int64
	last       string
	seq        int64
	deny       atomic.Bool
	renderGate chan struct{}
}

func (p *costWriterProbe) EventsAfter(_ context.Context, _ kernel.ID, after int64, _ int) ([]realtime.Event, error) {
	var events []realtime.Event
	for seq := after + 1; seq <= p.seq; seq++ {
		events = append(events, realtime.Event{OrganizationID: p.sub.Organization, ChannelID: p.sub.Channel, Seq: seq, AudienceMemberID: &p.sub.Account})
	}
	return events, nil
}
func (p *costWriterProbe) Render(_ context.Context, e realtime.Event) (realtime.Outgoing, error) {
	p.last = "render"
	if p.renderGate != nil {
		<-p.renderGate
	}
	return realtime.Outgoing{ID: e.Seq, Name: "message"}, nil
}
func (p *costWriterProbe) MayReceive(_ context.Context, account kernel.ID, slug string, e realtime.Event) (bool, error) {
	if account != p.sub.Account || slug != p.sub.OrganizationSlug || e.OrganizationID != p.sub.Organization || e.AudienceMemberID == nil {
		p.t.Error("authorization lost stream scope")
	}
	p.last = "audience"
	if e.AudienceMemberID != nil && *e.AudienceMemberID == p.sub.Member {
		p.last = "bound"
		return !p.deny.Load(), nil
	}
	return true, nil
}
func (p *costWriterProbe) Send(_ context.Context, out realtime.Outgoing) error {
	if p.last != "bound" {
		p.t.Error("send was not immediately preceded by bound-member authorization")
	}
	p.last = "send"
	p.frames = append(p.frames, out)
	p.sends.Add(1)
	return nil
}
func (p *costWriterProbe) Heartbeat(ctx context.Context) error {
	return p.Send(ctx, realtime.Outgoing{Name: "heartbeat", Ephemeral: true})
}

type costWriterSource func(context.Context) (realtime.Outgoing, error)

func (costWriterSource) Trigger(realtime.Event) bool { return true }
func (s costWriterSource) Recount(ctx context.Context, _ realtime.Subscription) (realtime.Outgoing, error) {
	return s(ctx)
}

type costWriterExit struct {
	cursor int64
	err    error
}

func startCostWriter(t *testing.T, q *costQueue, g costQueueGate, heartbeat time.Duration) (costWriter, *costWriterProbe, context.CancelFunc, <-chan costWriterExit) {
	p := &costWriterProbe{t: t, sub: realtime.Subscription{Organization: kernel.ID{1}, Channel: kernel.ID{2}, Member: kernel.ID{3}, Account: kernel.ID{4}, OrganizationSlug: "acme", Interests: []realtime.Interest{realtime.InterestMessages, realtime.InterestSidebar}}}
	n := int64(0)
	source := costWriterSource(func(context.Context) (realtime.Outgoing, error) {
		n++
		g.started <- n
		<-g.release // Deliberately return even after cancellation to test late work.
		return realtime.Outgoing{ID: 99, Name: "untrusted", Data: []byte(fmt.Sprint(n))}, nil
	})
	w := costWriter{realtime.Stream{Hub: realtime.NewHub(), Events: p, Renderer: p, Authorizer: p, Sidebar: source, Heartbeat: heartbeat}, q, q.register()}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan costWriterExit, 1)
	go func() { cursor, err := w.run(ctx, p.sub, 0, p); done <- costWriterExit{cursor, err} }()
	return w, p, cancel, done
}

func TestCostWriterProgress(t *testing.T) {
	for _, queued := range []bool{true, false} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, g := newCostQueue(1, 2), costQueueGate{make(chan int64, 16), make(chan struct{}, 16)}
				defer q.shutdown()
				defer close(g.release)
				if queued {
					q.submit(q.register(), g.request(10))
					g.want(t, 10)
				}
				w, p, cancel, done := startCostWriter(t, q, g, 0)
				defer cancel()
				synctest.Wait()
				if queued && q.stats().queued != 1 {
					t.Fatal("stream recount was not queued")
				}
				if !queued {
					g.want(t, 1)
				}
				p.seq = 3
				w.stream.Hub.Raise(p.sub.Organization, 3)
				synctest.Wait()
				if len(p.frames) != 3 {
					t.Fatalf("durable delivery stopped behind recount: %v", p.frames)
				}
				for i, frame := range p.frames {
					if frame.ID != int64(i+1) || frame.Ephemeral || frame.Name != "message" {
						t.Fatalf("durable IDs/order changed: %v", p.frames)
					}
				}
				cancel()
				if got := <-done; got.cursor != 3 || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("disconnect cursor/cause: %+v", got)
				}
			})
		})
	}
}

func TestCostWriterNoRollback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, g := newCostQueue(1, 1), costQueueGate{make(chan int64, 16), make(chan struct{}, 16)}
		defer q.shutdown()
		defer close(g.release)
		w, p, cancel, done := startCostWriter(t, q, g, 0)
		defer cancel()
		g.want(t, 1)
		p.seq = 1
		w.stream.Hub.Raise(p.sub.Organization, 1)
		synctest.Wait()
		g.release <- struct{}{}
		synctest.Wait()
		if len(p.frames) != 1 {
			t.Fatal("stale completion was sent")
		}
		// Fake time tests the fixed minimum; gates, not sleeps, order computation.
		time.Sleep(realtime.SidebarInterval - time.Nanosecond)
		synctest.Wait()
		if len(g.started) != 0 {
			t.Fatal("recount started before minimum interval")
		}
		time.Sleep(time.Nanosecond)
		g.want(t, 2)
		g.release <- struct{}{}
		synctest.Wait()
		if len(p.frames) != 2 || string(p.frames[1].Data) != "2" || p.frames[1].ID != 0 || !p.frames[1].Ephemeral || p.frames[1].Name != "sidebar" {
			t.Fatalf("quiet completion lost or sidebar carried durable ID: %v", p.frames)
		}
		// Inject delayed/duplicate results beyond the scheduler's serialization.
		for _, version := range []int64{1, 2} {
			w.work.done <- costQueueResult{version: version, out: realtime.Outgoing{Data: []byte("old")}}
			synctest.Wait()
			if len(p.frames) != 2 {
				t.Fatal("completed recount rolled back or duplicated a newer frame")
			}
		}
		cancel()
		<-done
	})
}

func TestCostWriterStops(t *testing.T) {
	for _, mode := range []string{"disconnect", "render-disconnect", "durable", "sidebar", "heartbeat"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, g := newCostQueue(1, 1), costQueueGate{make(chan int64, 16), make(chan struct{}, 16)}
				defer q.shutdown()
				release := sync.OnceFunc(func() { close(g.release) })
				defer release()
				heartbeat := map[string]time.Duration{"heartbeat": time.Second}[mode]
				w, p, cancel, done := startCostWriter(t, q, g, heartbeat)
				defer cancel()
				g.want(t, 1)
				q.submit(w.work, g.request(9)) // A successor must be dropped too.
				want := realtime.ErrMembershipRevoked
				switch mode {
				case "disconnect":
					cancel()
					want = context.Canceled
				case "durable", "render-disconnect":
					p.renderGate = make(chan struct{})
					p.seq = 1
					w.stream.Hub.Raise(p.sub.Organization, 1)
					synctest.Wait()
					p.deny.Store(mode == "durable") // Revoke while rendering.
					if mode == "render-disconnect" {
						cancel()
						want = context.Canceled
					}
					close(p.renderGate)
				case "sidebar":
					p.deny.Store(true) // Revoke after computation started, before completion.
					g.release <- struct{}{}
				default:
					time.Sleep(time.Second)
					synctest.Wait()
					if p.sends.Load() != 1 {
						t.Error("running recount stopped authorized heartbeat")
					}
					p.deny.Store(true)
					time.Sleep(time.Second)
				}
				synctest.Wait()
				select {
				case got := <-done:
					if got.cursor != 0 || !errors.Is(got.err, want) {
						t.Errorf("writer did not stop without cursor advance: %+v", got)
					}
				default:
					t.Error("revoked or disconnected writer kept running")
				}
				cancel()
				synctest.Wait()
				before := len(p.frames)
				release()
				synctest.Wait()
				if len(p.frames) != before || (mode != "heartbeat" && before != 0) || len(w.work.done) != 0 || q.stats().registered != 0 || w.work.ctx.Err() == nil {
					t.Errorf("ended writer sent or retained late recount: frames=%v queue=%+v", p.frames, q.stats())
				}
			})
		})
	}
}
