package realtime

import (
	"context"
	"testing"
	"testing/synctest"
)

func TestRecountSchedulerCancelRemovesPendingAndEligibility(t *testing.T) {
	for _, state := range []string{"queued", "running"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := NewRecountScheduler(1, 2)
				busy, queued := q.Register(), q.Register()
				gate := make(chan struct{})
				// Close before releasing the gate also makes failed state assertions safe.
				t.Cleanup(func() {
					go q.Close()
					synctest.Wait()
					close(gate)
					synctest.Wait()
				})
				busy.Submit(1, func(context.Context) (Outgoing, error) {
					<-gate
					return Outgoing{}, nil
				})
				synctest.Wait()
				target := queued
				if state == "running" {
					target = busy
				}
				target.Submit(2, func(context.Context) (Outgoing, error) { return Outgoing{}, nil })
				target.Cancel()
				q.mu.Lock()
				pending, eligible := target.pending != nil, len(q.ready)
				q.mu.Unlock()
				if pending {
					t.Fatal("Cancel retained pending computation")
				}
				if eligible != 0 {
					t.Fatalf("Cancel retained %d queued eligibility entries, want 0", eligible)
				}
			})
		})
	}
}
