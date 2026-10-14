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
	})
}
