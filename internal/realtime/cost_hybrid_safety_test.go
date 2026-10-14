package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestCostHybridRepairs(t *testing.T) {
	for _, interval := range []time.Duration{0, 10 * time.Second, 60 * time.Second} {
		t.Run(fmt.Sprint(interval), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sub := realtime.Subscription{Organization: kernel.ID{1}, Member: kernel.ID{2}, Interests: []realtime.Interest{realtime.InterestSidebar}}
				p := &costWriterProbe{t: t, sub: sub}
				changes := new(unread.Changes)
				baseline := changes.Watch(sub.Organization, sub.Member)
				defer baseline.Release()
				c := &costHybridConnection{costModelConnection: &costModelConnection{changes: changes, sender: p}, organization: sub.Organization, incarnation: 1, interval: interval, coverage: &hybridCoverage{frame: hybridFrame{incarnation: 1}}}
				c.snapshot = func(context.Context) (hybridFrame, error) {
					return hybridFrame{model: sidebarOracleModel{organization: sub.Organization, member: sub.Member}}, nil
				}
				for _, kind := range []realtime.EventKind{conversation.KindPosted, conversation.KindMessagesMoved, "unknown"} {
					if !c.Trigger(realtime.Event{OrganizationID: sub.Organization, ChannelID: kernel.ID{99}, Kind: kind}) || c.Trigger(realtime.Event{OrganizationID: kernel.ID{99}, Kind: kind}) {
						t.Fatal("production-only routing guessed a delta or lost organization scope")
					}
				}
				q := realtime.NewRecountScheduler(1, 1)
				defer q.Close()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				stream := realtime.Stream{Hub: realtime.NewHub(), Events: p, Authorizer: p, Sidebar: c, Recounts: q}
				go func() { _, err := stream.Run(ctx, sub, 0, c); done <- err }()
				synctest.Wait()
				if interval == 0 {
					interval = 30 * time.Second
				}
				start := time.Now()
				for cycle := 1; cycle <= 2; cycle++ {
					<-time.After(time.Until(start.Add(time.Duration(cycle)*interval)) - time.Second)
					changes.ReadChanged(kernel.ID{99}, sub.Member)
					changes.ReadChanged(sub.Organization, kernel.ID{99})
					synctest.Wait()
					if p.sends.Load() != int64(2*cycle-1) {
						t.Fatal("foreign publication dirtied hybrid model")
					}
					changes.ReadChanged(sub.Organization, sub.Member)
					synctest.Wait()
					if p.sends.Load() != int64(2*cycle) {
						t.Fatal("member read did not reconcile before repair")
					}
					<-time.After(time.Second - time.Nanosecond)
					synctest.Wait()
					if p.sends.Load() != int64(2*cycle) {
						t.Fatal("periodic repair preceded fixed deadline")
					}
					<-time.After(time.Nanosecond)
					synctest.Wait()
					generation, _ := baseline.Snapshot()
					c.mu.Lock()
					watch := c.latest.watch
					c.mu.Unlock()
					if p.sends.Load() != int64(2*cycle+1) || watch != uint64(2*cycle) || generation != uint64(cycle) {
						t.Fatal("repair deadline moved or repair escaped connection isolation")
					}
				}
				cancel()
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.Canceled) || q.Stats().Registrations != 0 {
					t.Fatalf("repair lifecycle exit: %v", err)
				}
				c.watch.Release() // Idempotent; the timer worker is already joined.
				<-time.After(interval)
				synctest.Wait()
				if p.sends.Load() != 5 {
					t.Fatal("repair survived disconnect")
				}
			})
		})
	}
}

func TestCostHybridCoverageSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sends, fail := 0, false
		c := &costHybridConnection{costModelConnection: &costModelConnection{changes: new(unread.Changes), sender: &retentionSender{send: func(realtime.Outgoing) error {
			if fail {
				return errors.New("send failed")
			}
			sends++
			return nil
		}}}, incarnation: 2, coverage: &hybridCoverage{frame: hybridFrame{incarnation: 2, request: 1, boundary: hybridBoundary{sequence: 100}}}}
		c.Watch(kernel.ID{1}, kernel.ID{2})
		defer c.watch.Release()
		send := func(request, incarnation uint64, sequence int64) error {
			c.pending = map[string]hybridFrame{"frame": {request: request, incarnation: incarnation, boundary: hybridBoundary{sequence: sequence}}}
			return c.Send(t.Context(), realtime.Outgoing{Name: "sidebar", Ephemeral: true, Data: []byte("frame")})
		}
		if err := send(2, 2, 90); err != nil || sends != 0 || c.coverage.frame.boundary.sequence != 100 {
			t.Fatal("unavailable coverage rolled back or sent model")
		}
		generation, _ := c.watch.Snapshot()
		if generation != 1 {
			t.Fatal("unavailable coverage failed to request fresh repair")
		}
		for _, pair := range [][2]uint64{{1, 2}, {2, 1}} {
			if err := send(pair[0], pair[1], 101); err != nil || sends != 0 || c.coverage.frame.request != 1 {
				t.Fatal("old request/incarnation escaped send guard")
			}
		}
		fail = true
		if send(2, 2, 101) == nil || c.coverage.frame.request != 1 || c.latest.request != 0 {
			t.Fatal("failed hybrid send installed model")
		}
		fail = false
		if err := send(2, 2, 101); err != nil || sends != 1 || c.latest.boundary.sequence != 101 || c.coverage.frame.request != 2 {
			t.Fatal("fresh repair did not install after successful send")
		}
		if c.Send(t.Context(), realtime.Outgoing{Name: "sidebar", Ephemeral: true, Data: []byte("frame")}) == nil {
			t.Fatal("consumed hybrid token accepted twice")
		}
		if got := c.rejections(); got != (costHybridRejections{repair: 1, ignored: 2}) {
			t.Fatalf("rejected verdict counts: got %+v, want repair=1 ignored=2", got)
		}
	})
}

func TestCostHybridRejectedCompletion(t *testing.T) {
	for _, verdict := range []string{"repair", "ignored"} {
		t.Run(verdict, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sub := realtime.Subscription{Organization: kernel.ID{1}, Member: kernel.ID{2}, Interests: []realtime.Interest{realtime.InterestSidebar}}
				p := &costWriterProbe{t: t, sub: sub}
				changes := new(unread.Changes)
				c := &costHybridConnection{costModelConnection: &costModelConnection{changes: changes, sender: p}, organization: sub.Organization, incarnation: 1, coverage: &hybridCoverage{frame: hybridFrame{incarnation: 1}}}
				frameCount := func() int {
					c.mu.Lock()
					defer c.mu.Unlock()
					return len(p.frames)
				}
				before := sidebarOracleModel{organization: sub.Organization, member: sub.Member,
					channels: []sidebarOracleEntry{{id: kernel.ID{3}, count: sidebarOracleDisplay(1)}}}
				after := hybridClone(before)
				after.channels[0].count = sidebarOracleDisplay(2)
				calls := 0
				c.snapshot = func(context.Context) (hybridFrame, error) {
					calls++
					if calls == 1 {
						return hybridFrame{boundary: hybridBoundary{sequence: 100}, model: before}, nil
					}
					if verdict == "repair" && calls == 2 {
						// Inject stale coverage through the production completion path.
						return hybridFrame{boundary: hybridBoundary{sequence: 99}, model: before}, nil
					}
					return hybridFrame{boundary: hybridBoundary{sequence: 101}, model: after}, nil
				}
				q := realtime.NewRecountScheduler(1, 1)
				defer q.Close()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				stream := realtime.Stream{Hub: realtime.NewHub(), Events: p, Authorizer: p, Sidebar: c, Recounts: q}
				go func() { _, err := stream.Run(ctx, sub, 0, c); done <- err }()
				synctest.Wait()
				if frameCount() != 1 || c.exact(before) != nil {
					t.Fatal("initial recount did not install the original model")
				}
				want := costHybridRejections{repair: 1}
				if verdict == "repair" {
					changes.ReadChanged(sub.Organization, sub.Member)
					<-time.After(realtime.SidebarInterval)
				} else {
					// Stream.Run serializes increasing versions in one incarnation,
					// so force ignored by sending a duplicate directly while it is idle.
					// Do not publish a read: only the rejection's wake can expose the change.
					c.mu.Lock()
					c.pending = map[string]hybridFrame{"duplicate": c.latest}
					c.mu.Unlock()
					if err := c.Send(ctx, realtime.Outgoing{Name: "sidebar", Ephemeral: true, Data: []byte("duplicate")}); err != nil {
						t.Fatal(err)
					}
					want = costHybridRejections{ignored: 1}
				}
				synctest.Wait()
				if got := c.rejections(); got != want {
					t.Errorf("rejected verdict counts: got %+v, want %+v", got, want)
				}
				if frameCount() != 1 || p.sends.Load() != 1 || c.exact(before) != nil || c.exact(after) == nil {
					t.Fatal("rejected completion added a probe frame or replaced the installed model")
				}
				<-time.After(realtime.SidebarInterval - time.Nanosecond)
				synctest.Wait()
				if frameCount() != 1 {
					t.Fatal("rejection bypassed the recount throttle")
				}
				<-time.After(time.Nanosecond)
				synctest.Wait()
				if frameCount() != 2 || p.sends.Load() != 2 || c.exact(after) != nil {
					t.Fatalf("%s rejection lost the change at the next recount deadline: frames=%d model=%v", verdict, frameCount(), c.exact(after))
				}
				if got := c.rejections(); got != want {
					t.Errorf("installed recount changed rejection counts: got %+v, want %+v", got, want)
				}
				cancel()
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.Canceled) || q.Stats().Registrations != 0 {
					t.Fatalf("rejection lifecycle exit: %v", err)
				}
			})
		})
	}
}
