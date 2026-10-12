package realtime

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type waitResult struct {
	levels     streamLevels
	completion *RecountCompletion
	err        error
}

// Gating the watch stops capture between the hub levels and the combined select.
type captureWatch struct {
	*sidebarWatch
	capture func()
}

func (w captureWatch) Snapshot() (uint64, <-chan struct{}) {
	w.capture()
	return w.sidebarWatch.Snapshot()
}

func TestStreamWaitCompletion(t *testing.T) {
	for _, phase := range []string{"before capture", "during capture", "after capture", "blocked"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := NewHub()
				stream, written := Stream{Hub: h}, time.Now()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				completions := make(chan RecountCompletion, 1)
				want := RecountCompletion{Version: 7, Outgoing: Outgoing{Data: []byte("counts")}, Err: errors.New("recount failed")}
				gate, resume := make(chan struct{}), make(chan struct{})
				capture := sync.OnceFunc(func() { close(gate); <-resume })
				var watch SidebarWatch
				if phase == "during capture" {
					watch = captureWatch{newSidebarWatch(), capture}
				}
				if phase == "after capture" {
					h.afterWaitRead = capture
				}
				if phase == "before capture" {
					completions <- want
				}
				done := make(chan waitResult, 2)
				go func() {
					// This goroutine is the writer: it owns both receives.
					for range 2 {
						levels, completion, err := stream.wait(ctx, sub, streamLevels{}, nil, &written, time.Time{}, watch, 0, completions)
						done <- waitResult{levels, completion, err}
					}
				}()
				if phase == "during capture" || phase == "after capture" {
					<-gate
					completions <- want
					close(resume)
				}
				synctest.Wait()
				if phase == "blocked" {
					if len(done) != 0 || h.Waiting(orgA) != 1 {
						t.Fatal("wait did not block before completion")
					}
					completions <- want
					synctest.Wait()
				}
				if len(done) != 1 {
					t.Fatalf("completion wake lost or repeated: returns=%d, want 1", len(done))
				}
				got := <-done
				if got.err != nil || got.levels != (streamLevels{}) || got.completion == nil || got.completion.Version != want.Version || got.completion.Err != want.Err || !bytes.Equal(got.completion.Outgoing.Data, want.Outgoing.Data) {
					t.Fatalf("writer completion=%+v, want unchanged payload and levels", got)
				}
				if len(completions) != 0 || h.Waiting(orgA) != 1 {
					t.Fatal("consumed completion kept waking instead of blocking")
				}
				cancel()
				got = <-done
				if got.completion != nil || !errors.Is(got.err, context.Canceled) || h.Waiting(orgA) != 0 {
					t.Fatalf("second wait=%+v, want cancellation only and no waiters", got)
				}
			})
		})
	}
}

func TestStreamWaitCompletionAtContextEnd(t *testing.T) {
	for _, wake := range []string{"heartbeat", "recount", "parent", "parent without deadline"} {
		t.Run(wake, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Both select inputs are ready; 64 trials expose a dropped
				// receipt with near certainty, allowing either select choice.
				for range 64 {
					h, send := NewHub(), newRecorder()
					stream := Stream{Hub: h, Authorizer: authorizerFunc(allowAll)}
					written := time.Now()
					recount := written.Add(time.Second)
					if wake == "heartbeat" {
						stream.Heartbeat = time.Second
						recount = written.Add(2 * time.Second)
					}
					if wake == "parent without deadline" {
						recount = time.Time{}
					}
					ctx, cancel := context.WithCancel(t.Context())
					completions := make(chan RecountCompletion, 1)
					want := RecountCompletion{Version: 7}
					h.afterWaitRead = sync.OnceFunc(func() {
						if wake == "parent" || wake == "parent without deadline" {
							cancel()
						} else {
							<-time.After(time.Second) // synctest virtual time.
							synctest.Wait()           // deadline cancellation has finished.
						}
						completions <- want
					})
					_, completion, err := stream.wait(ctx, sub, streamLevels{}, send, &written, recount, nil, 0, completions)
					cancel()
					if len(completions) == 0 {
						if completion == nil || completion.Version != want.Version {
							t.Fatal("consumed completion lost when wait context ended")
						}
					} else if completion != nil {
						t.Fatal("returned completion remains buffered")
					}
					if (wake == "heartbeat" || wake == "recount") && err != nil {
						t.Fatalf("deadline wait returned error: %v", err)
					}
					if (wake == "parent" || wake == "parent without deadline") && !errors.Is(err, context.Canceled) {
						t.Fatalf("parent cancellation returned error %v, want context.Canceled", err)
					}
					if h.Waiting(orgA) != 0 {
						t.Fatal("completion/context wake leaked a waiter")
					}
				}
			})
		})
	}
}

func TestStreamWaitOtherWakes(t *testing.T) {
	for _, input := range []string{"nil", "empty", "closed"} {
		for _, wake := range []string{"durable", "presence", "typing", "member", "context"} {
			t.Run(input+"/"+wake, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h, watch := NewHub(), newSidebarWatch()
					stream, written := Stream{Hub: h}, time.Now()
					selected := sub
					selected.Interests = []Interest{InterestPresence, InterestTyping}
					ctx, cancel := context.WithCancelCause(t.Context())
					defer cancel(nil)
					var completions chan RecountCompletion
					if input != "nil" {
						completions = make(chan RecountCompletion, 1)
						if input == "closed" {
							close(completions)
						}
					}
					// Turn an accidental closed-channel spin into an assertion.
					captures := 0
					h.afterWaitRead = func() {
						captures++
						if captures > 3 {
							cancel(errors.New("capture spin"))
						}
					}
					done := make(chan waitResult, 1)
					go func() {
						levels, completion, err := stream.wait(ctx, selected, streamLevels{}, nil, &written, time.Time{}, watch, 0, completions)
						done <- waitResult{levels, completion, err}
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
					if got.levels != want || got.completion != nil || (wake == "context" && !errors.Is(got.err, cause)) || (wake != "context" && got.err != nil) || h.Waiting(orgA) != 0 {
						t.Fatalf("existing wake changed: %+v, want levels=%+v", got, want)
					}
				})
			})
		}
	}
}

func TestStreamWaitDeadlines(t *testing.T) {
	for _, input := range []string{"nil", "completions"} {
		t.Run(input, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, send := NewHub(), newRecorder()
				stream := Stream{Hub: h, Heartbeat: time.Second / 2, Authorizer: authorizerFunc(allowAll)}
				start := time.Now()
				written, recount := start, start.Add(5*time.Second/4)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var completions chan RecountCompletion
				if input == "completions" {
					completions = make(chan RecountCompletion, 1)
				}
				done := make(chan waitResult, 3)
				go func() {
					for {
						levels, completion, err := stream.wait(ctx, sub, streamLevels{}, send, &written, recount, nil, 0, completions)
						done <- waitResult{levels, completion, err}
						if completion == nil || err != nil {
							return
						}
					}
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
					if completions != nil && (step == 1 || step == 3) {
						completions <- RecountCompletion{Version: uint64(step)}
						synctest.Wait()
						if len(done) != 1 {
							t.Fatal("completion did not wake deadline wait")
						}
						got := <-done
						if got.err != nil || got.completion == nil || got.completion.Version != uint64(step) {
							t.Fatalf("deadline completion=%+v", got)
						}
					}
				}
				if len(done) != 1 {
					t.Fatal("fixed recount deadline did not fire")
				}
				got := <-done
				if got.err != nil || got.completion != nil || got.levels != (streamLevels{}) || !written.Equal(start.Add(time.Second)) || h.Waiting(orgA) != 0 {
					t.Fatalf("deadline wait=%+v written=%v waiters=%d", got, written.Sub(start), h.Waiting(orgA))
				}
			})
		})
	}
}
