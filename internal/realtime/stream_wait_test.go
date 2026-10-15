package realtime

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

type waitResult struct {
	levels streamLevels
	err    error
}

func TestStreamWaitOtherWakes(t *testing.T) {
	t.Run("sync", func(t *testing.T) {
		for _, wake := range []string{"durable", "presence", "typing", "member", "context"} {
			t.Run(wake, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h, watch := NewHub(), newSidebarWatch()
					stream, written := Stream{Hub: h}, time.Now()
					selected := sub
					selected.Interests = []Interest{InterestPresence, InterestTyping}
					ctx, cancel := context.WithCancelCause(t.Context())
					defer cancel(nil)
					// Turn an accidental idle spin into an assertion.
					captures := 0
					h.afterWaitRead = func() {
						captures++
						if captures > 3 {
							cancel(errors.New("capture spin"))
						}
					}
					done := make(chan waitResult, 1)
					go func() {
						levels, err := stream.wait(ctx, selected, streamLevels{}, nil, &written, time.Time{}, watch, 0)
						done <- waitResult{levels, err}
					}()
					synctest.Wait()
					if len(done) != 0 || h.Waiting(orgA) != 1 {
						t.Fatal("idle wait returned or spun without work")
					}
					want, cause := streamLevels{}, errors.New("session ended")
					switch wake {
					case "durable":
						h.Raise(orgA, 1)
						want.durable = 1
					case "presence":
						h.RaiseGeneration(orgA, InterestPresence, 1)
						want.presence = 1
					case "typing":
						h.RaiseGeneration(orgA, InterestTyping, 1)
						want.typing = 1
					case "member":
						watch.publish()
					case "context":
						cancel(cause)
					}
					synctest.Wait()
					if len(done) != 1 {
						t.Fatal("existing wake lost")
					}
					got := <-done
					if got.levels != want || (wake == "context" && !errors.Is(got.err, cause)) || (wake != "context" && got.err != nil) || h.Waiting(orgA) != 0 {
						t.Fatalf("existing wake changed: %+v, want levels=%+v", got, want)
					}
				})
			})
		}
	})
}

func TestStreamWaitDeadlines(t *testing.T) {
	for _, name := range []string{"sync"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, send := NewHub(), newRecorder()
				stream := Stream{Hub: h, Heartbeat: time.Second / 2, Authorizer: authorizerFunc(allowAll)}
				start := time.Now()
				written, recount := start, start.Add(5*time.Second/4)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan waitResult, 3)
				go func() {
					levels, err := stream.wait(ctx, sub, streamLevels{}, send, &written, recount, nil, 0)
					done <- waitResult{levels, err}
				}()
				for step := 1; step <= 5; step++ {
					<-time.After(time.Second / 4) // synctest virtual time, no wall-clock sleep.
					synctest.Wait()
					if send.heartbeatCount() != step/2 {
						t.Fatalf("heartbeat deadline moved: step=%d heartbeats=%d, want %d", step, send.heartbeatCount(), step/2)
					}
					if step < 5 && len(done) != 0 {
						t.Fatal("recount returned before fixed deadline")
					}
				}
				if len(done) != 1 {
					t.Fatal("fixed recount deadline did not fire")
				}
				got := <-done
				if got.err != nil || got.levels != (streamLevels{}) || !written.Equal(start.Add(time.Second)) || h.Waiting(orgA) != 0 {
					t.Fatalf("deadline wait=%+v written=%v waiters=%d", got, written.Sub(start), h.Waiting(orgA))
				}
			})
		})
	}
}
