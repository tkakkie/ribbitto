package realtime

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestRecountObservation(t *testing.T) {
	for _, mode := range []string{"delivered", "denied", "authorization", "failed", "cancelled", "send-cancelled", "compute", "nil"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := NewRecountScheduler(1, 2)
				defer q.Close()
				blocker, release := q.Register(), make(chan struct{})
				blocker.Submit(1, func(context.Context) (Outgoing, error) { <-release; return Outgoing{}, nil })
				synctest.Wait()
				observer := &RecountObserver{}
				stream := Stream{RecountObserver: observer, Authorizer: authorizerFunc(allowAll)}
				if mode == "nil" {
					stream.RecountObserver = nil
				}
				compute := make(chan struct{})
				failure := errors.New("failed")
				stream.Sidebar = sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
					<-compute
					if mode == "compute" {
						return Outgoing{}, failure
					}
					return Outgoing{}, nil
				}}
				p := &scheduledSidebar{registration: q.Register(), pending: &sidebarDelivery{dirty: true}, version: 1}
				defer p.cancel()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				written, sends := time.Now(), 0
				send := frameSender{func(out Outgoing) error {
					sends++
					if out.ID != 0 || out.Name != "sidebar" || !out.Ephemeral {
						t.Fatal("observation changed sidebar frame")
					}
					if mode == "send-cancelled" {
						cancel()
					}
					if mode == "failed" {
						return failure
					}
					return nil
				}}
				if err := stream.scheduledRecount(ctx, sub, p, nil, send, &written); err != nil {
					t.Fatal(err)
				}
				<-time.After(10 * time.Millisecond) // Virtual queue delay; no wall-clock sleep.
				close(release)
				synctest.Wait()
				<-time.After(100 * time.Millisecond)
				close(compute)
				synctest.Wait() // Completion remains held in the mailbox.
				if got := observer.Stats(); got.Delivered != 0 || got.Received != 0 {
					t.Fatalf("held completion counted as received/delivered: %+v", got)
				}
				<-time.After(time.Second)
				if mode == "denied" {
					stream.Authorizer = authorizerFunc(func(Event) (bool, error) { return false, nil })
				}
				if mode == "authorization" {
					stream.Authorizer = authorizerFunc(func(Event) (bool, error) { return false, failure })
				}
				if mode == "cancelled" {
					cancel()
				}
				err := stream.scheduledRecount(ctx, sub, p, nil, send, &written)
				p.cancel()
				got := observer.Stats()
				if mode == "nil" {
					if got != (RecountDeliveryStats{}) || sends != 1 || err != nil || p.sent != 1 {
						t.Fatalf("nil observer changed delivery: stats=%+v sends=%d err=%v", got, sends, err)
					}
					return
				}
				delivered, denied, failed, cancelled, received := uint64(0), uint64(0), uint64(0), uint64(0), uint64(1)
				switch mode {
				case "delivered":
					delivered = 1
				case "denied":
					denied = 1
				case "cancelled":
					cancelled, received = 1, 0
				case "send-cancelled":
					cancelled = 1
				default:
					failed = 1
				}
				if got.Submitted != 1 || got.Received != received || got.Delivered != delivered || got.Denied != denied || got.Failed != failed || got.Cancelled != cancelled {
					t.Fatalf("delivery outcomes (%s): %+v", mode, got)
				}
				if got.Queue.Count != 1 || got.Queue.Total != 10*time.Millisecond || got.Queue.Buckets[1] != 1 || got.Compute.Count != 1 || got.Compute.Total != 100*time.Millisecond || got.Compute.Buckets[2] != 1 {
					t.Fatalf("queue/compute timing: %+v", got)
				}
				if got.CompletionToSend.Count != delivered || got.SubmitToSend.Count != delivered {
					t.Fatalf("unsuccessful attempt counted in send durations: %+v", got)
				}
				if delivered == 1 && (got.CompletionToSend.Total != time.Second || got.CompletionToSend.Buckets[3] != 1 || got.SubmitToSend.Total != 1110*time.Millisecond || got.SubmitToSend.Buckets[4] != 1) {
					t.Fatalf("held completion timing: %+v", got)
				}
			})
		})
	}
}
