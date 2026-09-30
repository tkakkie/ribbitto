package realtime

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
)

var (
	channelA = domain.ID{0xca}
	channelB = domain.ID{0xcb}
	sub      = Subscription{Organization: orgA, OrganizationSlug: "acme", Account: account1, Channel: channelA}
)

func posted(seq int64, channel domain.ID) domain.Event {
	return domain.Event{OrganizationID: orgA, Seq: seq, Kind: domain.EventMessagePosted, ChannelID: channel}
}

// fakeLog is an event log that can grow while a stream runs. failOn makes
// the read of that call number (1-based) fail.
type fakeLog struct {
	mu     sync.Mutex
	events []domain.Event
	calls  int
	failOn int
	err    error
}

func (l *fakeLog) EventsAfter(_ context.Context, org domain.ID, after int64, limit int) ([]domain.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls == l.failOn {
		return nil, l.err
	}
	var out []domain.Event
	for _, e := range l.events {
		if e.OrganizationID == org && e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (l *fakeLog) append(events ...domain.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, events...)
}

type authorizerFunc func(domain.Event) (bool, error)

func (f authorizerFunc) MayReceive(_ context.Context, _ domain.ID, _ string, e domain.Event) (bool, error) {
	return f(e)
}

type rendererFunc func(domain.Event) (Outgoing, error)

func (f rendererFunc) Render(_ context.Context, _ Subscription, e domain.Event) (Outgoing, error) {
	return f(e)
}

func allowAll(domain.Event) (bool, error) { return true, nil }

func render(e domain.Event) (Outgoing, error) {
	return Outgoing{ID: e.Seq, Name: "message"}, nil
}

// recorder is a Sender that records what it sent and can fail on one
// sequence.
type recorder struct {
	mu     sync.Mutex
	sent   []int64
	failOn int64
	err    error
	change chan struct{}
}

func newRecorder() *recorder { return &recorder{change: make(chan struct{}, 100)} }

func (r *recorder) Send(_ context.Context, out Outgoing) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if out.ID == r.failOn {
		return r.err
	}
	r.sent = append(r.sent, out.ID)
	r.change <- struct{}{}
	return nil
}

func (r *recorder) ids() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent)
}

// waitFor blocks until the recorder has sent exactly want.
func (r *recorder) waitFor(t *testing.T, want ...int64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !slices.Equal(r.ids(), want) {
		select {
		case <-r.change:
		case <-deadline:
			t.Fatalf("sent %v, want %v", r.ids(), want)
		}
	}
}

type result struct {
	cursor int64
	err    error
}

func runAsync(ctx context.Context, s Stream, cursor int64, send Sender) <-chan result {
	done := make(chan result, 1)
	go func() {
		c, err := s.Run(ctx, sub, cursor, send)
		done <- result{c, err}
	}()
	return done
}

func TestStreamReplaysThenDeliversLive(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{events: []domain.Event{posted(1, channelA), posted(2, channelA)}}
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 0, send)
	send.waitFor(t, 1, 2)

	log.append(posted(3, channelA), posted(4, channelB))
	hub.Raise(orgA, 4)
	send.waitFor(t, 1, 2, 3)

	cancel()
	got := <-done
	if got.cursor != 4 || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Run = %d, %v; want 4, context.Canceled", got.cursor, got.err)
	}
}

// A fresh hub knows no sequence, so the loop must drain every batch before
// it waits, and skipped events must still move the cursor.
func TestStreamDrainsBatchesOnAColdHub(t *testing.T) {
	denied := posted(5, channelA)
	log := &fakeLog{events: []domain.Event{
		posted(1, channelA),
		posted(2, channelB), // another channel
		{OrganizationID: orgA, Seq: 3, Kind: domain.EventMemberJoined},
		{OrganizationID: orgA, Seq: 4, Kind: "future.kind", ChannelID: channelA}, // unknown kind
		denied,
		posted(6, channelA),
		posted(7, channelA),
	}}
	authorize := func(e domain.Event) (bool, error) { return e.Seq != denied.Seq, nil }
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(authorize), Renderer: rendererFunc(render), BatchSize: 2}, 0, send)
	send.waitFor(t, 1, 6, 7)

	cancel()
	got := <-done
	if got.cursor != 7 || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Run = %d, %v; want 7, context.Canceled", got.cursor, got.err)
	}
	// Batches [1 2] [3 4] [5 6] [7]: the short last read ends the drain.
	if log.calls != 4 {
		t.Fatalf("reads = %d, want 4", log.calls)
	}
}

// Access is checked for each event right before sending, including events
// read in the same batch as ones already sent.
func TestStreamRechecksAccessBeforeEachSend(t *testing.T) {
	log := &fakeLog{events: []domain.Event{posted(1, channelA), posted(2, channelA)}}
	var mu sync.Mutex
	member := true
	authorize := func(domain.Event) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		allowed := member
		member = false // the membership is removed after the first send
		return allowed, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(authorize), Renderer: rendererFunc(render)}, 0, send)
	send.waitFor(t, 1)
	cancel()
	got := <-done
	if got.cursor != 2 || !slices.Equal(send.ids(), []int64{1}) {
		t.Fatalf("Run = %d, sent %v; want cursor 2 and only event 1", got.cursor, send.ids())
	}
}

func TestStreamStopsOnCancelWhileWaiting(t *testing.T) {
	cause := errors.New("session ended")
	ctx, cancel := context.WithCancelCause(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: NewHub(), Events: &fakeLog{}, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 9, send)
	cancel(cause)
	got := <-done
	if got.cursor != 9 || !errors.Is(got.err, cause) {
		t.Fatalf("Run = %d, %v; want 9, %v", got.cursor, got.err, cause)
	}
}

// Each failure stops the loop before the event it could not deliver, so a
// reconnect resumes there; none of them may be treated as a skip.
func TestStreamErrorsDoNotAdvanceTheCursor(t *testing.T) {
	failure := errors.New("database unavailable")
	tests := []struct {
		name      string
		log       *fakeLog
		authorize authorizerFunc
		render    rendererFunc
		failSend  bool
	}{
		{
			name: "event reader",
			// The first read returns event 1, the second fails.
			log:       &fakeLog{failOn: 2, err: failure},
			authorize: allowAll,
			render:    render,
		},
		{
			name: "authorization lookup",
			authorize: func(e domain.Event) (bool, error) {
				if e.Seq == 2 {
					return false, failure
				}
				return true, nil
			},
			render: render,
		},
		{
			name:      "renderer",
			authorize: allowAll,
			render: func(e domain.Event) (Outgoing, error) {
				if e.Seq == 2 {
					return Outgoing{}, failure
				}
				return render(e)
			},
		},
		{name: "sender", authorize: allowAll, render: render, failSend: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := tt.log
			if log == nil {
				log = &fakeLog{}
			}
			log.events = []domain.Event{posted(1, channelA), posted(2, channelA), posted(3, channelA)}
			send := newRecorder()
			if tt.failSend {
				send.failOn, send.err = 2, failure
			}
			s := Stream{Hub: NewHub(), Events: log, Authorizer: tt.authorize, Renderer: tt.render, BatchSize: 1}
			cursor, err := s.Run(t.Context(), sub, 0, send)
			if !errors.Is(err, failure) {
				t.Fatalf("Run error = %v, want %v", err, failure)
			}
			if cursor != 1 {
				t.Fatalf("cursor = %d, want 1: it must not move past the failed event", cursor)
			}
			if got := send.ids(); !slices.Equal(got, []int64{1}) {
				t.Fatalf("sent %v, want only event 1", got)
			}
		})
	}
}

// The hub may be ahead of the log where no row exists (a cursor below the
// replay boundary). The loop must then block until the hub moves past the
// value it saw, not read the same empty range in a tight loop.
func TestStreamDoesNotSpinWhenTheHubIsAheadOfTheLog(t *testing.T) {
	hub := NewHub()
	hub.Raise(orgA, 50)
	log := &fakeLog{}
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 10, send)
	time.Sleep(blocked)
	log.mu.Lock()
	reads := log.calls
	log.mu.Unlock()
	if reads > 2 {
		t.Fatalf("reads = %d while nothing new was committed, want at most 2", reads)
	}

	log.append(posted(51, channelA))
	hub.Raise(orgA, 51)
	send.waitFor(t, 51)
	cancel()
	if got := <-done; got.cursor != 51 {
		t.Fatalf("cursor = %d, want 51", got.cursor)
	}
}

func TestStreamRefusesANegativeCursor(t *testing.T) {
	log := &fakeLog{}
	s := Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}
	if _, err := s.Run(t.Context(), sub, -1, newRecorder()); err == nil || log.calls != 0 {
		t.Fatalf("Run(-1) = %v after %d reads, want a refusal before reading", err, log.calls)
	}
}
