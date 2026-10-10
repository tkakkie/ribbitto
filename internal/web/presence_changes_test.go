package web

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/presence"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// This adapter proves the owner/loop contract without production rendering (#768).
type presenceChangesOwner struct {
	state  *presence.State
	start  presence.Token
	reads  int
	gate   <-chan struct{}
	result presence.Changes
}

func (o *presenceChangesOwner) Read(_ context.Context, sub realtime.Subscription, after int64) (realtime.EphemeralFrame, error) {
	if o.reads == 0 {
		if err := json.Unmarshal([]byte(sub.PresenceAfter), &o.start); err != nil {
			return realtime.EphemeralFrame{}, err
		}
	} else {
		o.start.Generation = after
		if o.gate != nil {
			<-o.gate
		}
	}
	o.reads++
	o.result = o.state.After(sub.Organization, o.start)
	name := ""
	if o.result.Reset {
		name = "reset"
	} else if len(o.result.Entries) != 0 {
		name = "presence"
	}
	return realtime.EphemeralFrame{Organization: sub.Organization, Generation: o.result.Token.Generation,
		Outgoing: realtime.Outgoing{Name: name}}, nil
}

type presenceEmptyLog struct{}

func (presenceEmptyLog) EventsAfter(context.Context, kernel.ID, int64, int) ([]realtime.Event, error) {
	return nil, nil
}

type presenceChangesSender struct {
	out    []realtime.Outgoing
	cancel context.CancelFunc
}

func (s *presenceChangesSender) Send(_ context.Context, out realtime.Outgoing) error {
	s.out = append(s.out, out)
	if out.Name != "reset" {
		s.cancel()
	}
	return nil
}
func (*presenceChangesSender) Heartbeat(context.Context) error { return nil }

func TestPresenceChangesStream(t *testing.T) {
	for _, reason := range []string{"process", "boundary", "101", "100", "offline"} {
		for _, live := range []bool{false, true} {
			t.Run(reason+map[bool]string{false: "/reconnect", true: "/open"}[live], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					hub := realtime.NewHub()
					state := presence.New(hub)
					id, memberID := kernel.ID{1}, kernel.ID{2}
					closeMember := state.Open(id, memberID)
					page, _ := state.Read(id, []kernel.ID{memberID})
					token, err := json.Marshal(page.Token)
					if err != nil {
						t.Fatal(err)
					}
					gate := make(chan struct{})
					owner := &presenceChangesOwner{state: state, gate: gate}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					send := &presenceChangesSender{cancel: cancel}
					stream := realtime.Stream{Hub: hub, Events: presenceEmptyLog{}, Authorizer: streamAllow(true),
						Owners: map[realtime.Interest]realtime.EphemeralOwner{realtime.InterestPresence: owner}}
					done := make(chan struct{})
					start := func() {
						go func() {
							defer close(done)
							cursor, err := stream.Run(ctx, realtime.Subscription{Organization: id,
								Interests: []realtime.Interest{realtime.InterestPresence}, PresenceAfter: string(token)}, 7, send)
							if cursor != 7 || (err != nil && !errors.Is(err, context.Canceled)) {
								t.Errorf("run = %d, %v", cursor, err)
							}
						}()
						synctest.Wait()
					}
					if live {
						start()
						if owner.reads != 1 || len(send.out) != 0 {
							t.Fatal("initial read did not settle")
						}
					}
					advance := func(d time.Duration) { <-time.After(d); synctest.Wait() }
					switch reason {
					case "process":
						owner.state = presence.New(hub)
						owner.state.Open(id, memberID)
						owner.state.Open(id, kernel.ID{3}) // New process's generation exceeds the seen level.
					case "boundary", "offline":
						closeMember()
						advance(30 * time.Second)
						if reason == "boundary" {
							advance(presence.OfflineRetention)
						}
					case "100", "101":
						count := 100
						if reason == "101" {
							count++
						}
						for i := range count {
							state.Open(id, kernel.ID{4, byte(i)})
						}
					}
					close(gate)
					if !live {
						start()
					}
					<-done
					want := "reset"
					if reason == "100" || reason == "offline" {
						want = "presence"
					}
					if len(send.out) != 1 || send.out[0].Name != want || !send.out[0].Ephemeral || send.out[0].ID != 0 {
						t.Fatalf("frames = %+v; want one %s without id", send.out, want)
					}
					if reason == "100" && len(owner.result.Entries) != 100 {
						t.Fatalf("entries = %+v", owner.result)
					}
					if reason == "offline" && (len(owner.result.Entries) != 1 || owner.result.Entries[0] != (presence.Entry{Member: memberID, Online: false})) {
						t.Fatalf("offline reconnect read = %+v", owner.result)
					}
				})
			})
		}
	}
}
