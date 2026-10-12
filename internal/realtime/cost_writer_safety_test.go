package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	renderAt   int64
	audiences  map[int64]*kernel.ID
	checks     []kernel.ID
	afters     []int64
}

func (p *costWriterProbe) EventsAfter(_ context.Context, _ kernel.ID, after int64, limit int) ([]realtime.Event, error) {
	p.afters = append(p.afters, after)
	var events []realtime.Event
	for seq := after + 1; seq <= p.seq && len(events) < limit; seq++ {
		audience, ok := p.audiences[seq]
		if !ok {
			audience = &p.sub.Member
		}
		events = append(events, realtime.Event{OrganizationID: p.sub.Organization, ChannelID: p.sub.Channel, Seq: seq, AudienceMemberID: audience})
	}
	return events, nil
}
func (p *costWriterProbe) Render(_ context.Context, e realtime.Event) (realtime.Outgoing, error) {
	p.last = "render"
	if p.renderGate != nil && (p.renderAt == 0 || e.Seq == p.renderAt) {
		<-p.renderGate
	}
	return realtime.Outgoing{ID: e.Seq, Name: "message"}, nil
}
func (p *costWriterProbe) MayReceive(_ context.Context, account kernel.ID, slug string, e realtime.Event) (bool, error) {
	if account != p.sub.Account || slug != p.sub.OrganizationSlug || e.OrganizationID != p.sub.Organization || e.AudienceMemberID == nil {
		p.t.Error("authorization lost stream scope")
	}
	p.last = "audience"
	if e.AudienceMemberID != nil {
		p.checks = append(p.checks, *e.AudienceMemberID)
	}
	if e.AudienceMemberID != nil && *e.AudienceMemberID == p.sub.Member {
		p.last = "bound"
		return !p.deny.Load(), nil
	}
	return false, nil
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

// Unrelated durable events must not invalidate the recount held by the backlog test.
type costWriterQuietSource struct{ realtime.SidebarSource }

func (costWriterQuietSource) Trigger(realtime.Event) bool { return false }

type costWriterExit struct {
	cursor int64
	err    error
}

func startCostWriter(t *testing.T, q *costQueue, g costQueueGate, heartbeat time.Duration, configure ...func(*costWriter, *costWriterProbe)) (costWriter, *costWriterProbe, context.CancelFunc, <-chan costWriterExit) {
	p := &costWriterProbe{t: t, sub: realtime.Subscription{Organization: kernel.ID{1}, Channel: kernel.ID{2}, Member: kernel.ID{3}, Account: kernel.ID{4}, OrganizationSlug: "acme", Interests: []realtime.Interest{realtime.InterestMessages, realtime.InterestSidebar}}}
	n := int64(0)
	source := costWriterSource(func(context.Context) (realtime.Outgoing, error) {
		n++
		g.started <- n
		<-g.release // Deliberately return even after cancellation to test late work.
		return realtime.Outgoing{ID: 99, Name: "untrusted", Data: []byte(fmt.Sprint(n))}, nil
	})
	w := costWriter{realtime.Stream{Hub: realtime.NewHub(), Events: p, Renderer: p, Authorizer: p, Sidebar: source, Heartbeat: heartbeat}, q, q.register()}
	for _, configure := range configure {
		configure(&w, p)
	}
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

func TestCostWriterBacklogCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, g := newCostQueue(1, 1), costQueueGate{make(chan int64, 16), make(chan struct{}, 16)}
		defer q.shutdown()
		defer close(g.release)
		renderGate := make(chan struct{})
		release := sync.OnceFunc(func() { close(renderGate) })
		defer release()
		w, p, cancel, done := startCostWriter(t, q, g, 0, func(w *costWriter, p *costWriterProbe) {
			w.stream.Sidebar = costWriterQuietSource{w.stream.Sidebar}
			p.renderGate, p.renderAt = renderGate, realtime.DefaultBatchSize
		})
		defer cancel()
		g.want(t, 1)
		p.seq = realtime.DefaultBatchSize + 3
		w.stream.Hub.Raise(p.sub.Organization, p.seq)
		synctest.Wait()
		if len(p.frames) != realtime.DefaultBatchSize-1 {
			t.Fatalf("backlog did not stop at first batch gate: %v", p.frames)
		}
		g.release <- struct{}{}
		synctest.Wait()
		if len(w.work.done) != 1 {
			t.Fatal("recount completion did not arrive during first batch")
		}
		release()
		synctest.Wait()
		cancel()
		got := <-done
		if got.cursor != p.seq || !errors.Is(got.err, context.Canceled) || !slices.Contains(p.afters, realtime.DefaultBatchSize) {
			t.Fatalf("backlog batch cursor/cause: %+v, reads=%v", got, p.afters)
		}
		if len(p.frames) != int(p.seq)+1 || p.frames[realtime.DefaultBatchSize].Name != "sidebar" {
			t.Fatal("recount completion was not sent between batches before backlog drained")
		}
		seq := int64(0)
		for i, frame := range p.frames {
			if i == realtime.DefaultBatchSize {
				if frame.ID != 0 || !frame.Ephemeral || string(frame.Data) != "1" {
					t.Fatalf("backlog sidebar flags/payload changed: %+v", frame)
				}
				continue
			}
			seq++
			if frame.ID != seq || frame.Name != "message" || frame.Ephemeral {
				t.Fatalf("backlog durable IDs/order changed at frame %d: %+v", i, frame)
			}
		}
	})
}

func TestCostWriterRecountProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, g := newCostQueue(1, 2), costQueueGate{make(chan int64, 16), make(chan struct{}, 16)}
		defer q.shutdown()
		defer close(g.release)
		blocker := q.register()
		q.submit(blocker, g.request(100))
		g.want(t, 100)
		w, p, cancel, done := startCostWriter(t, q, g, 0)
		defer cancel()
		completedAt := time.Now()
		for recount := int64(1); recount <= 3; recount++ {
			if recount > 1 {
				q.submit(blocker, g.request(100+recount))
				g.want(t, 100+recount)
				// Advance only the interval deadline; gates synchronize all work.
				time.Sleep(realtime.SidebarInterval - time.Nanosecond)
				synctest.Wait()
				if q.stats().queued != 0 {
					t.Fatal("recount started before minimum interval")
				}
				time.Sleep(time.Nanosecond)
			}
			synctest.Wait()
			if q.stats().queued != 1 {
				t.Fatalf("recount %d was not queued at next interval after outdated frame", recount)
			}
			// Every request is invalidated both while queued and while running.
			p.seq++
			w.stream.Hub.Raise(p.sub.Organization, p.seq)
			synctest.Wait()
			g.release <- struct{}{}
			g.want(t, recount)
			p.seq++
			w.stream.Hub.Raise(p.sub.Organization, p.seq)
			synctest.Wait()
			g.release <- struct{}{}
			synctest.Wait()
			var sidebars []realtime.Outgoing
			for _, frame := range p.frames {
				if frame.Name == "sidebar" {
					sidebars = append(sidebars, frame)
				}
			}
			if len(sidebars) != int(recount) {
				t.Fatalf("recount %d completion made no sidebar progress: got %d frames, want %d", recount, len(sidebars), recount)
			}
			frame := sidebars[recount-1]
			if string(frame.Data) != fmt.Sprint(recount) || frame.ID != 0 || !frame.Ephemeral {
				t.Fatalf("recount %d frame did not preserve completion payload/flags: %+v", recount, frame)
			}
			if recount > 1 && time.Since(completedAt) != realtime.SidebarInterval {
				t.Fatalf("recount %d did not send at next interval: %s", recount, time.Since(completedAt))
			}
			completedAt = time.Now()
		}
		cancel()
		if got := <-done; got.cursor != p.seq || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("recount progress cursor/cause: %+v", got)
		}
	})
}

func TestCostWriterAudiences(t *testing.T) {
	other := kernel.ID{5}
	for _, audience := range []string{"own", "other", "organization"} {
		t.Run(audience, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, g := newCostQueue(1, 1), costQueueGate{make(chan int64, 16), make(chan struct{}, 16)}
				defer q.shutdown()
				defer close(g.release)
				w, p, cancel, done := startCostWriter(t, q, g, 0)
				defer cancel()
				g.want(t, 1)
				p.audiences = map[int64]*kernel.ID{}
				wantChecks, wantFrames := []kernel.ID{p.sub.Member}, 1
				switch audience {
				case "other":
					p.audiences[1] = &other
					wantChecks, wantFrames = []kernel.ID{other, p.sub.Member}, 0
				case "organization":
					p.audiences[1] = nil
				}
				p.seq = 1
				w.stream.Hub.Raise(p.sub.Organization, 1)
				synctest.Wait()
				if len(p.frames) != wantFrames {
					t.Errorf("audience %s sent %d frames, want %d", audience, len(p.frames), wantFrames)
				}
				if !slices.Equal(p.checks, wantChecks) {
					t.Errorf("audience %s authorization path: %v, want %v", audience, p.checks, wantChecks)
				}
				// A subsequent broadcast must read from the skipped event's cursor too.
				p.audiences[2], p.seq = nil, 2
				w.stream.Hub.Raise(p.sub.Organization, 2)
				synctest.Wait()
				if !slices.Contains(p.afters, 1) {
					t.Errorf("audience %s did not advance live cursor: reads=%v", audience, p.afters)
				}
				cancel()
				if got := <-done; got.cursor != 2 || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("audience %s cursor/cause: %+v", audience, got)
				}
				if len(p.frames) != wantFrames+1 {
					t.Fatalf("audience %s blocked subsequent broadcast: %v", audience, p.frames)
				}
				for i, frame := range p.frames {
					if frame.ID != int64(2-wantFrames+i) || frame.Name != "message" || frame.Ephemeral {
						t.Fatalf("audience %s durable frame changed: %+v", audience, frame)
					}
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
		if len(p.frames) != 2 || string(p.frames[1].Data) != "1" || p.frames[1].ID != 0 || !p.frames[1].Ephemeral || p.frames[1].Name != "sidebar" {
			t.Fatalf("outdated completion newer than last sent frame lost: %v", p.frames)
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
		if len(p.frames) != 3 || string(p.frames[2].Data) != "2" || p.frames[2].ID != 0 || !p.frames[2].Ephemeral || p.frames[2].Name != "sidebar" {
			t.Fatalf("quiet completion lost or sidebar carried durable ID: %v", p.frames)
		}
		// Inject delayed/duplicate results beyond the scheduler's serialization.
		for _, version := range []int64{1, 2} {
			w.work.done <- costQueueResult{version: version, out: realtime.Outgoing{Data: []byte("old")}}
			synctest.Wait()
			if len(p.frames) != 3 {
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
