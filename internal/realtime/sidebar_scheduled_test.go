package realtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestScheduledProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, w, h, log, send := NewRecountScheduler(1, 2), newSidebarWatch(), NewHub(), &fakeLog{}, newRecorder()
		defer q.Close()
		send.failOn = -1
		blocker, free := q.Register(), make(chan struct{})
		blocker.Submit(1, func(context.Context) (Outgoing, error) { <-free; return Outgoing{}, nil })
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		selected := sub
		selected.Interests = []Interest{InterestMessages, InterestSidebar}
		started, resume := make(chan struct{}, 1), make(chan struct{})
		stream := Stream{Hub: h, Events: log, Renderer: rendererFunc(render), Recounts: q, Heartbeat: SidebarInterval / 2,
			Authorizer: authorizerFunc(allowAll), Sidebar: watchedSidebar{sidebarSource{sidebarTrigger,
				func(context.Context, Subscription) (Outgoing, error) {
					started <- struct{}{}
					<-resume
					return Outgoing{}, nil
				}},
				func(kernel.ID, kernel.ID) SidebarWatch { return w }}}
		done := make(chan result, 1)
		go func() { c, err := stream.Run(ctx, selected, 0, send); done <- result{c, err} }()
		synctest.Wait()
		for seq := int64(1); seq <= 2; seq++ {
			log.append(posted(seq, channelA))
			h.Raise(orgA, seq)
			w.publish()
			synctest.Wait()
			<-time.After(SidebarInterval / 2) // Only virtual time in this bubble.
			synctest.Wait()
			if len(send.ids()) != int(seq) || send.heartbeatCount() != int(seq) {
				t.Errorf("queued/running delivery: frames=%v heartbeats=%d", send.ids(), send.heartbeatCount())
			}
			if seq == 1 {
				close(free)
				<-started
				blocker.Cancel()
			}
		}
		for attempt := 1; attempt <= 3; attempt++ {
			resume <- struct{}{}
			synctest.Wait()
			if len(send.ids()) != 2+attempt {
				t.Errorf("invalidated completion made no progress: frames=%v attempt=%d", send.ids(), attempt)
			}
			<-time.After(SidebarInterval - time.Nanosecond)
			synctest.Wait()
			if len(started) != 0 {
				t.Error("recount started before SidebarInterval")
			}
			<-time.After(time.Nanosecond)
			synctest.Wait()
			if len(started) != 1 {
				t.Error("later invalidation lost at fixed SidebarInterval")
			} else {
				<-started
			}
			w.publish()
			synctest.Wait()
		}
		cancel()
		got := <-done // The uncooperative computation is still gated.
		before, snapshots := len(send.ids()), w.snapshots
		resume <- struct{}{}
		synctest.Wait()
		if got.cursor != 2 || !errors.Is(got.err, context.Canceled) || len(send.ids()) != before || w.releases != 1 || w.snapshots != snapshots {
			t.Errorf("late completion after exit: Run=%+v frames=%v releases=%d", got, send.ids(), w.releases)
		}
		q.mu.Lock()
		remaining := len(q.streams)
		q.mu.Unlock()
		if remaining != 0 {
			t.Errorf("exit retained %d registrations", remaining)
		}
	})
}

func TestScheduledBetweenBatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, send := NewRecountScheduler(1, 1), newRecorder()
		defer q.Close()
		send.failOn = -1
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reads, batches, compute := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
		stream := Stream{Hub: NewHub(), Recounts: q, BatchSize: 1, Heartbeat: SidebarInterval,
			Authorizer: authorizerFunc(allowAll), Sidebar: sidebarSource{sidebarTrigger,
				func(context.Context, Subscription) (Outgoing, error) { <-compute; return Outgoing{}, nil }},
			Events: sidebarReader(func(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
				reads <- struct{}{}
				select {
				case <-ctx.Done():
					return nil, context.Cause(ctx)
				case <-batches:
					return []Event{posted(after+1, channelB)}, nil
				}
			})}
		selected := sub
		selected.Interests = []Interest{InterestSidebar}
		done := make(chan result, 1)
		go func() { c, err := stream.Run(ctx, selected, 0, send); done <- result{c, err} }()
		<-reads
		batches <- struct{}{}
		<-reads
		<-time.After(SidebarInterval)
		batches <- struct{}{}
		<-reads
		if send.heartbeatCount() != 1 {
			t.Error("backlog starved heartbeat during computation")
		}
		close(compute)
		synctest.Wait()
		batches <- struct{}{}
		<-reads
		if len(send.ids()) != 1 {
			t.Error("full backlog starved completion between batches")
		}
		cancel()
		<-done
	})
}

func TestScheduledVersionsAndGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, w := NewRecountScheduler(1, 2), newSidebarWatch()
		defer q.Close()
		blocker, free := q.Register(), make(chan struct{})
		blocker.Submit(1, func(context.Context) (Outgoing, error) { <-free; return Outgoing{}, nil })
		synctest.Wait()
		p := &scheduledSidebar{registration: q.Register(), pending: &sidebarDelivery{dirty: true, watch: w}, version: 1}
		defer p.cancel()
		stream := Stream{Authorizer: authorizerFunc(allowAll), Sidebar: sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) { return Outgoing{}, nil }}}
		written, frames := time.Now(), 0
		send := frameSender{func(out Outgoing) error { frames++; return nil }}
		_ = stream.scheduledRecount(t.Context(), sub, p, nil, send, &written)
		w.publish() // Queued publication must be captured when computation starts.
		close(free)
		completion := <-p.registration.Completions()
		w.publish() // This publication is not represented by the completed frame.
		_ = stream.scheduledRecount(t.Context(), sub, p, &completion, send, &written)
		if p.pending.covered != 1 || !p.pending.dirty || frames != 1 {
			t.Errorf("generation capture: covered=%d dirty=%v frames=%d", p.pending.covered, p.pending.dirty, frames)
		}
		for _, version := range []uint64{1, 0} {
			completion.Version = version
			_ = stream.scheduledRecount(t.Context(), sub, p, &completion, send, &written)
		}
		if frames != 1 || p.sent != 1 {
			t.Errorf("duplicate/older completion sent: frames=%d sent=%d", frames, p.sent)
		}
	})
}

func TestScheduledExitSafety(t *testing.T) {
	failure := errors.New("failed")
	for _, tc := range []struct {
		name                            string
		read, compute, auth, send, want error
		deny, refusal, panics           bool
	}{
		{name: "refusal", refusal: true, want: ErrRecountRefused},
		{name: "reader", read: failure, want: failure},
		{name: "reset", read: ErrCursorExpired},
		{name: "error", compute: failure, want: failure},
		{name: "panic", panics: true},
		{name: "deny", deny: true, want: ErrMembershipRevoked},
		{name: "authorization", auth: failure, want: failure},
		{name: "send", send: failure, want: failure},
		{name: "heartbeat", want: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, w, send := NewRecountScheduler(1, 1), newSidebarWatch(), newRecorder()
				defer q.Close()
				if tc.refusal {
					q.Close()
				}
				frames, checks := 0, 0
				stream := Stream{Hub: NewHub(), Recounts: q, Heartbeat: SidebarInterval,
					Events: sidebarReader(func(context.Context, kernel.ID, int64, int) ([]Event, error) { return nil, tc.read }),
					Authorizer: authorizerFunc(func(e Event) (bool, error) {
						checks++
						if e.OrganizationID != sub.Organization || e.AudienceMemberID == nil || *e.AudienceMemberID != sub.Member {
							t.Error("sidebar authorization lost bound member")
						}
						return !tc.deny, tc.auth
					}), Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
						if tc.panics {
							panic("failed")
						}
						return Outgoing{}, tc.compute
					}}, func(kernel.ID, kernel.ID) SidebarWatch { return w }}}
				selected := sub
				selected.Interests = []Interest{InterestSidebar}
				send.failOn, send.err, send.heartbeatErr = -1, tc.send, failure
				if tc.send != nil {
					send.failOn = 0
				}
				cursor, err := stream.Run(t.Context(), selected, 7, frameSenderWithHeartbeat{frameSender{func(out Outgoing) error {
					if checks != frames+1 {
						t.Error("sidebar sent without immediate authorization")
					}
					frames++
					return send.Send(t.Context(), out)
				}}, send})
				if cursor != 7 || (!tc.panics && !errors.Is(err, tc.want)) || (tc.panics && (err == nil || !strings.Contains(err.Error(), "recounting sidebar: recount computation panicked"))) {
					t.Errorf("Run=%d,%v want=%v", cursor, err, tc.want)
				}
				if w.releases != 1 {
					t.Errorf("watch releases=%d", w.releases)
				}
				q.mu.Lock()
				remaining := len(q.streams)
				q.mu.Unlock()
				if remaining != 0 {
					t.Error("exit retained registration")
				}
				if (tc.deny || tc.auth != nil || tc.compute != nil || tc.panics || tc.refusal) && frames != 0 {
					t.Error("unauthorized/error frame sent")
				}
			})
		})
	}
}

type frameSenderWithHeartbeat struct {
	frameSender
	heartbeat Sender
}

func (s frameSenderWithHeartbeat) Heartbeat(ctx context.Context) error {
	return s.heartbeat.Heartbeat(ctx)
}
