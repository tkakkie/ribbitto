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
	// heartbeats counts heartbeats; heartbeatErr, if set, fails them.
	heartbeats   int
	heartbeatErr error
}

func newRecorder() *recorder { return &recorder{change: make(chan struct{}, 100)} }

func (r *recorder) Heartbeat(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.heartbeatErr != nil {
		return r.heartbeatErr
	}
	r.heartbeats++
	select {
	case r.change <- struct{}{}:
	default: // a test that counts heartbeats polls heartbeatCount
	}
	return nil
}

func (r *recorder) heartbeatCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.heartbeats
}

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

	// Event 4 is another channel's: it is skipped, and the cursor moves past
	// it. Event 5 is delivered after it, so once 5 is received the loop has
	// finished with 4; cancelling right after 3 could land before that.
	log.append(posted(3, channelA), posted(4, channelB), posted(5, channelA))
	hub.Raise(orgA, 5)
	send.waitFor(t, 1, 2, 3, 5)

	cancel()
	got := <-done
	if got.cursor != 5 || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Run = %d, %v; want 5, context.Canceled", got.cursor, got.err)
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
	denied := make(chan struct{})
	authorize := func(e domain.Event) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		allowed := member
		member = false // the membership is removed after the first send
		if e.Seq == 2 {
			close(denied)
		}
		return allowed, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(authorize), Renderer: rendererFunc(render)}, 0, send)
	send.waitFor(t, 1)
	// Cancel only once event 2 has been checked: the loop checks
	// cancellation before every event, so cancelling earlier would stop it
	// before event 2 and leave the cursor at 1.
	<-denied
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

// Cancellation between two events of the same batch stops the loop before
// the second, even when the authorizer and renderer ignore the context.
func TestStreamChecksCancellationBetweenEvents(t *testing.T) {
	cause := errors.New("session ended")
	ctx, cancel := context.WithCancelCause(t.Context())
	log := &fakeLog{events: []domain.Event{posted(1, channelA), posted(2, channelA)}}
	send := &cancellingSender{recorder: newRecorder(), cancel: func() { cancel(cause) }}
	s := Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}
	cursor, err := s.Run(ctx, sub, 0, send)
	if !errors.Is(err, cause) || cursor != 1 || !slices.Equal(send.ids(), []int64{1}) {
		t.Fatalf("Run = %d, %v, sent %v; want 1, %v, only event 1", cursor, err, send.ids(), cause)
	}
}

// cancellingSender cancels the stream's context after its first send.
type cancellingSender struct {
	*recorder
	cancel func()
}

func (c *cancellingSender) Send(ctx context.Context, out Outgoing) error {
	if err := c.recorder.Send(ctx, out); err != nil {
		return err
	}
	c.cancel()
	return nil
}

// While idle the loop sends a heartbeat each time Heartbeat passes, and an
// event still arrives at once.
func TestStreamSendsHeartbeatsWhileIdle(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{events: []domain.Event{posted(1, channelA)}}
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render), Heartbeat: 5 * time.Millisecond}, 0, send)
	send.waitFor(t, 1)
	for deadline := time.Now().Add(5 * time.Second); send.heartbeatCount() < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d heartbeats while idle, want at least 2", send.heartbeatCount())
		}
	}
	log.append(posted(2, channelA))
	hub.Raise(orgA, 2)
	send.waitFor(t, 1, 2)
	cancel()
	if got := <-done; got.cursor != 2 || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Run = %d, %v; want 2, context.Canceled", got.cursor, got.err)
	}
}

// A heartbeat that cannot be written (a client that stopped reading) stops
// the loop without moving the cursor.
func TestStreamStopsOnAFailedHeartbeat(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{events: []domain.Event{posted(1, channelA)}}
	send := newRecorder()
	failure := errors.New("i/o timeout")
	send.heartbeatErr = failure
	got := <-runAsync(t.Context(), Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render), Heartbeat: 5 * time.Millisecond}, 0, send)
	if got.cursor != 1 || !errors.Is(got.err, failure) {
		t.Fatalf("Run = %d, %v; want 1 and the heartbeat's error", got.cursor, got.err)
	}
}

// Wakeups that write nothing, such as another channel's events, do not put
// the heartbeat off: it counts from the stream's last write.
func TestStreamHeartbeatsThroughOtherChannelsTraffic(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	send := newRecorder()
	runAsync(ctx, Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render), Heartbeat: 50 * time.Millisecond}, 0, send)
	// Another channel's event every 5 ms, far more often than the heartbeat.
	go func() {
		for seq := int64(1); ctx.Err() == nil; seq++ {
			log.append(posted(seq, channelB))
			hub.Raise(orgA, seq)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); send.heartbeatCount() < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d heartbeats under another channel's traffic, want at least 2", send.heartbeatCount())
		}
	}
	if ids := send.ids(); len(ids) != 0 {
		t.Fatalf("sent %v, want only heartbeats: every event was another channel's", ids)
	}
}

// When the stream's own context has ended, its cause wins, even if the
// heartbeat deadline ended the wait just before.
func TestHeartbeatDue(t *testing.T) {
	ended, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("session ended")
	cancel(cause)
	other := errors.New("hub failed")
	for _, tt := range []struct {
		name    string
		ctx     context.Context
		err     error
		wantDue bool
		wantErr error
	}{
		{"deadline only", t.Context(), errHeartbeatDue, true, nil},
		{"deadline, and the stream ended too", ended, errHeartbeatDue, false, cause},
		{"the stream ended", ended, cause, false, cause},
		{"woken", t.Context(), nil, false, nil},
		{"another error", t.Context(), other, false, other},
	} {
		t.Run(tt.name, func(t *testing.T) {
			due, err := heartbeatDue(tt.ctx, tt.err)
			if due != tt.wantDue || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("heartbeatDue = %t, %v; want %t, %v", due, err, tt.wantDue, tt.wantErr)
			}
		})
	}
}

// slowFilteredLog returns full batches of another channel's events, slowly,
// as a long backlog would, and never runs out.
type slowFilteredLog struct{ delay time.Duration }

func (l slowFilteredLog) EventsAfter(_ context.Context, org domain.ID, after int64, limit int) ([]domain.Event, error) {
	time.Sleep(l.delay)
	events := make([]domain.Event, limit)
	for i := range events {
		events[i] = domain.Event{OrganizationID: org, Seq: after + int64(i) + 1, Kind: domain.EventMessagePosted, ChannelID: channelB}
	}
	return events, nil
}

// Draining full batches that are all filtered out still sends heartbeats
// on time, before the stream ever catches up.
func TestStreamHeartbeatsWhileDrainingFilteredBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	send := newRecorder()
	runAsync(ctx, Stream{Hub: NewHub(), Events: slowFilteredLog{delay: 2 * time.Millisecond}, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render), BatchSize: 10, Heartbeat: 30 * time.Millisecond}, 0, send)
	for deadline := time.Now().Add(5 * time.Second); send.heartbeatCount() < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d heartbeats while draining, want at least 2", send.heartbeatCount())
		}
	}
	if ids := send.ids(); len(ids) != 0 {
		t.Fatalf("sent %v, want only heartbeats", ids)
	}
}
