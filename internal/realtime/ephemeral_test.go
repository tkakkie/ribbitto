package realtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

type ownerFunc func(Subscription, int64) (EphemeralFrame, error)

func (f ownerFunc) Read(_ context.Context, sub Subscription, after int64) (EphemeralFrame, error) {
	return f(sub, after)
}

// Every negative scope fixture changes just one predicate. The authorizer
// allows those fixtures, so it cannot hide a missing pre-filter.
func TestEphemeralScopesAndAuthorization(t *testing.T) {
	failure := errors.New("authorization failed")
	topicA, topicB := kernel.ID{1}, kernel.ID{2}
	for _, tt := range []struct {
		name                         string
		kind, interest               Interest
		organization, channel, topic kernel.ID
		deny                         bool
		err                          error
		checks, frames               int
	}{
		{"matching", InterestTyping, InterestTyping, orgA, channelA, topicA, false, nil, 2, 2},
		{"organisation", InterestTyping, InterestTyping, orgB, channelA, topicA, false, nil, 0, 0},
		{"channel", InterestTyping, InterestTyping, orgA, channelB, topicA, false, nil, 0, 0},
		{"topic", InterestTyping, InterestTyping, orgA, channelA, topicB, false, nil, 0, 0},
		{"interest", InterestTyping, InterestPresence, orgA, channelA, topicA, false, nil, 0, 0},
		{"connect", InterestPresence, InterestPresence, orgA, channelA, topicA, false, nil, 1, 1},
		{"non-member typing", InterestTyping, InterestTyping, orgA, channelA, topicA, true, nil, 1, 0},
		{"non-member", InterestPresence, InterestPresence, orgA, channelA, topicA, true, nil, 1, 0},
		{"channel denied", InterestTyping, InterestTyping, orgA, channelA, topicA, true, nil, 1, 0},
		{"failed check", InterestTyping, InterestTyping, orgA, channelA, topicA, false, failure, 1, 0},
		{"presence", InterestPresence, InterestPresence, orgA, channelA, topicA, false, nil, 1, 1},
		{"presence organisation", InterestPresence, InterestPresence, orgB, channelA, topicA, false, nil, 0, 0},
		{"presence interest", InterestPresence, InterestTyping, orgA, channelA, topicA, false, nil, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, log, send := NewHub(), &fakeLog{maxReads: 3}, newRecorder()
				send.failOn = -1
				selected := sub
				selected.Interests, selected.Topic, selected.PresenceAfter = []Interest{tt.interest}, &topicA, "instance:7"
				checks, reads := 0, 0
				stream := Stream{Hub: h, Events: log, Owners: map[Interest]EphemeralOwner{
					tt.kind: ownerFunc(func(s Subscription, after int64) (EphemeralFrame, error) {
						reads++
						if s.PresenceAfter != "instance:7" || after != int64((reads-1)*9) {
							t.Error("connect did not pass the token and initial generation")
						}
						generation := int64(9)
						if tt.name == "matching" {
							h.RaiseGeneration(orgA, tt.kind, 10) // raise after the first snapshot
							generation += int64(reads - 1)
						}
						return EphemeralFrame{tt.organization, tt.channel, &tt.topic, generation, Outgoing{Name: string(tt.kind)}}, nil
					}),
				}, Authorizer: authorizerFunc(func(e Event) (bool, error) {
					checks++
					channel := channelA
					if tt.kind == InterestPresence {
						channel = kernel.ID{}
					}
					if reads != checks || e.OrganizationID != orgA || e.ChannelID != channel {
						t.Error("authorization did not follow the scoped owner read")
					}
					return !tt.deny, tt.err
				})}
				// Nine coalesced changes still require at most one check, denied too.
				for generation := int64(1); generation <= 9 && tt.name != "connect"; generation++ {
					h.RaiseGeneration(orgA, tt.kind, generation)
				}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan result, 1)
				go func() { cursor, err := stream.Run(ctx, selected, 0, send); done <- result{cursor, err} }()
				synctest.Wait()
				cancel()
				got := <-done
				if got.cursor != 0 || (tt.err != nil && !errors.Is(got.err, tt.err)) || checks != tt.checks || len(send.ids()) != tt.frames || log.readCount() != max(1, tt.checks) {
					t.Fatalf("Run=%+v checks=%d frames=%v reads=%d", got, checks, send.ids(), log.readCount())
				}
			})
		})
	}
}

type frameSender struct{ send func(Outgoing) error }

func (s frameSender) Send(_ context.Context, out Outgoing) error { return s.send(out) }
func (frameSender) Heartbeat(context.Context) error              { return nil }

func TestEphemeralBetweenDurableBatches(t *testing.T) {
	h := NewHub()
	log := &fakeLog{events: []Event{posted(1, channelA), posted(2, channelA), posted(3, channelA), posted(4, channelA)}, failOn: 3, err: errTooManyReads}
	selected := sub
	selected.Interests = []Interest{InterestMessages, InterestPresence, InterestTyping}
	var sent []string
	reads, checks := 0, 0
	owners := map[Interest]EphemeralOwner{}
	for _, kind := range []Interest{InterestPresence, InterestTyping} {
		owners[kind] = ownerFunc(func(_ Subscription, after int64) (EphemeralFrame, error) {
			reads++
			if (reads <= 2 && after != 0) || (reads > 2 && after != 2) {
				t.Errorf("after=%d reads=%d", after, reads)
			}
			generation := int64(2)
			if reads > 2 {
				generation = 3
			}
			return EphemeralFrame{orgA, channelA, nil, generation, Outgoing{ID: 999, Name: string(kind)}}, nil
		})
	}
	stream := Stream{Hub: h, Events: log, BatchSize: 2, Renderer: rendererFunc(render), Owners: owners,
		Authorizer: authorizerFunc(func(e Event) (bool, error) {
			if e.Seq == 0 {
				checks++
			}
			return true, nil
		})}
	cursor, err := stream.Run(t.Context(), selected, 0, frameSender{func(out Outgoing) error {
		sent = append(sent, fmt.Sprintf("%s:%d", out.Name, out.ID))
		if out.Name == "message" && out.ID == 1 {
			for _, kind := range []Interest{InterestPresence, InterestTyping} {
				h.RaiseGeneration(orgA, kind, 2)
			}
		}
		if out.Ephemeral {
			if out.ID != 0 {
				t.Error("ephemeral frame carried a durable cursor")
			}
			// State changes while writing: the next durable event must not
			// wait for another frame of this kind in the same batch.
			h.RaiseGeneration(orgA, Interest(out.Name), 3)
		}
		return nil
	}})
	want := []string{"message:1", "message:2", "presence:0", "typing:0", "message:3", "message:4", "presence:0", "typing:0"}
	if cursor != 4 || !errors.Is(err, errTooManyReads) || !slices.Equal(sent, want) || checks != 4 || reads != 4 {
		t.Fatalf("cursor=%d err=%v sent=%v checks=%d reads=%d", cursor, err, sent, checks, reads)
	}
}

func TestEphemeralEmptyWakesKeepHeartbeatDeadline(t *testing.T) {
	for _, denied := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			h, send := NewHub(), newRecorder()
			send.failOn = -1
			selected := sub
			selected.Interests = []Interest{InterestTyping}
			generation, checks := int64(0), 0
			stream := Stream{Hub: h, Events: &fakeLog{}, Heartbeat: 5 * time.Second,
				Owners: map[Interest]EphemeralOwner{InterestTyping: ownerFunc(func(_ Subscription, _ int64) (EphemeralFrame, error) {
					name := ""
					if generation == 0 {
						<-time.After(time.Second)
					}
					if denied || generation == 0 {
						name = "typing"
					}
					return EphemeralFrame{orgA, channelA, nil, generation, Outgoing{Name: name}}, nil
				})}, Authorizer: authorizerFunc(func(Event) (bool, error) { checks++; return checks == 1, nil })}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan result, 1)
			go func() { cursor, err := stream.Run(ctx, selected, 12, send); done <- result{cursor, err} }()
			<-send.change // connect writes at t=1; heartbeats count from that write
			synctest.Wait()
			for generation = 1; generation <= 5; generation++ {
				<-time.After(time.Second)
				h.RaiseGeneration(orgA, InterestTyping, generation)
				synctest.Wait()
				if send.heartbeatCount() != int(generation/5) {
					t.Fatal("empty wake postponed heartbeat")
				}
			}
			if len(send.ids()) != 1 || (denied && checks != 6) || (!denied && checks != 1) {
				t.Fatalf("frames=%v checks=%d", send.ids(), checks)
			}
			cancel()
			if got := <-done; got.cursor != 12 || !errors.Is(got.err, context.Canceled) {
				t.Fatalf("Run=%+v", got)
			}
		})
	}
}
