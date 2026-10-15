package realtime

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestStreamCheckpoint(t *testing.T) {
	for _, mode := range []string{"unread", "skipped", "organisation", "member", "deny", "lookup", "send", "render", "event authorization", "event send"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				selected := sub
				selected.Member = kernel.ID{7}
				selected.Interests = []Interest{InterestSidebar}
				failure := errors.New("failed")
				log := &fakeLog{events: []Event{posted(1, channelA), posted(2, channelA)}}
				renderer := &unreadTestRenderer{rendererFunc: rendererFunc(render)}
				stream := Stream{Hub: NewHub(), Events: log, Renderer: renderer}
				if mode == "skipped" {
					stream.Renderer = rendererFunc(render)
				}
				var frames []string
				checks := 0
				stream.Authorizer = authorizerFunc(func(e Event) (bool, error) {
					if e.Seq == 0 {
						checks++
						if mode != "skipped" && renderer.unread != 2 {
							t.Error("checkpoint authorized before batch completion")
							return false, failure
						}
						if mode == "organisation" && e.OrganizationID != selected.Organization ||
							mode == "member" && (e.AudienceMemberID == nil || *e.AudienceMemberID != selected.Member) {
							return false, failure
						}
						if mode == "deny" {
							return false, nil
						}
						if mode == "lookup" {
							return false, failure
						}
					}
					if mode == "event authorization" && e.Seq == 2 {
						return false, failure
					}
					return true, nil
				})
				if mode == "render" {
					selected.Interests = []Interest{InterestMessages}
					stream.Renderer = rendererFunc(func(e Event) (Outgoing, error) {
						if e.Seq == 2 {
							return Outgoing{}, failure
						}
						return render(e)
					})
				}
				send := frameSender{func(out Outgoing) error {
					if out.Name == "checkpoint" && checks != 1 {
						t.Error("checkpoint sent without immediate authorization")
						return failure
					}
					if mode == "send" && out.Name == "checkpoint" || mode == "event send" && len(frames) == 1 {
						return failure
					}
					frames = append(frames, out.Name)
					if out.Name == "checkpoint" && out.ID != 2 {
						t.Errorf("checkpoint=%d, want 2", out.ID)
						return failure
					}
					return nil
				}}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan result, 1)
				go func() { c, err := stream.Run(ctx, selected, 0, send); done <- result{c, err} }()
				synctest.Wait()
				cancel()
				got := <-done
				want, cursor, reason := []string{"unread", "unread", "checkpoint"}, int64(2), error(context.Canceled)
				switch mode {
				case "skipped":
					want = []string{"checkpoint"}
				case "deny":
					want, reason = []string{"unread", "unread"}, ErrMembershipRevoked
				case "lookup", "send":
					want, reason = []string{"unread", "unread"}, failure
				case "render":
					want, cursor, reason = []string{"message"}, 1, failure
				case "event authorization", "event send":
					want, cursor, reason = []string{"unread"}, 1, failure
				}
				if !slices.Equal(frames, want) || got.cursor != cursor || !errors.Is(got.err, reason) {
					t.Fatalf("frames=%v Run cursor=%d error=%v, want %v cursor=%d error=%v", frames, got.cursor, got.err, want, cursor, reason)
				}
			})
		})
	}
}

func TestStreamCheckpointInterval(t *testing.T) {
	for _, mode := range []string{"skipped", "unread"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, log, send := NewHub(), &fakeLog{}, newRecorder()
				send.failOn = -1
				var checks atomic.Int64
				stream := Stream{Hub: h, Events: log, Renderer: rendererFunc(render), BatchSize: 1, Heartbeat: time.Second,
					Authorizer: authorizerFunc(func(e Event) (bool, error) {
						if e.Seq == 0 {
							checks.Add(1)
						}
						return true, nil
					})}
				selected := sub
				if mode == "unread" {
					selected.Interests = append(slices.Clone(sub.Interests), InterestSidebar)
					stream.Renderer = &unreadTestRenderer{rendererFunc: rendererFunc(render)}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan result, 1)
				go func() { c, err := stream.Run(ctx, selected, 0, send); done <- result{c, err} }()
				check := func(want int) {
					t.Helper()
					synctest.Wait()
					if got := send.count("checkpoint"); got != want || checks.Load() != int64(want) {
						t.Fatalf("checkpoints=%d membership checks=%d, want %d", got, checks.Load(), want)
					}
				}
				check(0)
				<-time.After(time.Second / 2) // Virtual time: the ID write starts a fresh interval.
				log.append(posted(1, channelA))
				h.Raise(orgA, 1)
				check(0)
				for seq := int64(2); seq <= 5; seq++ {
					log.append(posted(seq, channelB))
					h.Raise(orgA, seq)
					check(0)
				}
				<-time.After(time.Second / 2)
				log.append(posted(6, channelB))
				h.Raise(orgA, 6)
				check(0) // One interval since connect, but only half since the ID.
				<-time.After(time.Second / 2)
				log.append(posted(7, channelB))
				h.Raise(orgA, 7)
				check(1) // Skips use the heartbeat; unread traffic uses the batch deadline.
				<-time.After(time.Second / 2)
				log.append(posted(8, channelB))
				h.Raise(orgA, 8)
				check(1) // Neither unread writes nor another batch reset the checkpoint interval.
				<-time.After(time.Second)
				check(2) // Pending idle progress replaces the next heartbeat comment.
				if ids := send.ids(); ids[len(ids)-1] != 8 || send.heartbeatCount() != 0 || (mode == "unread" && send.count("unread") != 7) {
					t.Fatalf("pending progress not covered: ids=%v frames=%v heartbeats=%d", ids, send.eventNames(), send.heartbeatCount())
				}
				cancel()
				if got := <-done; got.cursor != 8 || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("Run=%+v", got)
				}
			})
		})
	}
}
