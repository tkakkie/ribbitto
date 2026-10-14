package realtime

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

var (
	channelA = kernel.ID{0xca}
	channelB = kernel.ID{0xcb}
	sub      = Subscription{Interests: []Interest{InterestMessages}, Organization: orgA, OrganizationSlug: "acme", Account: account1, Channel: channelA}
)

// Synthetic kinds: realtime routes on the envelope, so its tests need no
// publisher's kind names.
const (
	kindPosted EventKind = "test.posted"
	kindMoved  EventKind = "test.moved"
	kindJoined EventKind = "test.joined"
)

func posted(seq int64, channel kernel.ID) Event {
	return Event{OrganizationID: orgA, Seq: seq, Kind: kindPosted, ChannelID: channel}
}

// fakeLog is an event log that can grow while a stream runs. failOn makes
// the read of that call number (1-based) fail. maxReads, if set, fails every
// read after that many, so a loop that spins stops instead of hanging.
type fakeLog struct {
	mu       sync.Mutex
	events   []Event
	calls    int
	failOn   int
	err      error
	maxReads int
	// boundary is the replay boundary: a read after a lower cursor returns
	// ErrCursorExpired, as the PostgreSQL reader does.
	boundary int64
}

var errTooManyReads = errors.New("fakeLog: too many reads")

func (l *fakeLog) EventsAfter(_ context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls == l.failOn {
		return nil, l.err
	}
	if l.maxReads > 0 && l.calls > l.maxReads {
		return nil, errTooManyReads
	}
	if after < l.boundary {
		return nil, ErrCursorExpired
	}
	var out []Event
	for _, e := range l.events {
		if e.OrganizationID == org && e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (l *fakeLog) readCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

func (l *fakeLog) expire(boundary int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.boundary = boundary
}

func (l *fakeLog) append(events ...Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, events...)
}

type authorizerFunc func(Event) (bool, error)

func (f authorizerFunc) MayReceive(_ context.Context, _ kernel.ID, _ string, e Event) (bool, error) {
	return f(e)
}

type rendererFunc func(Event) (Outgoing, error)

func (f rendererFunc) Render(_ context.Context, e Event) (Outgoing, error) {
	return f(e)
}

func allowAll(Event) (bool, error) { return true, nil }

func render(e Event) (Outgoing, error) {
	return Outgoing{ID: e.Seq, Name: "message"}, nil
}

// recorder is a Sender that records what it sent and can fail on one
// sequence.
type recorder struct {
	mu     sync.Mutex
	sent   []int64
	names  []string
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
	r.names = append(r.names, out.Name)
	r.change <- struct{}{}
	return nil
}

// eventNames returns the SSE event name of each send, in order.
func (r *recorder) eventNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.names)
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
	log := &fakeLog{events: []Event{posted(1, channelA), posted(2, channelA)}}
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
func TestStreamDrainsBatchesOnAColdHub(t *testing.T) { testStreamDrainsBatchesOnAColdHub(t, false) }

func testStreamDrainsBatchesOnAColdHub(t *testing.T, scheduled bool) {
	testStreamColdHubEntry(t, scheduled, false)
}

func testStreamColdHubEntry(t *testing.T, scheduled, reserved bool) {
	denied := posted(5, channelA)
	denied.AudienceMemberID = &kernel.ID{2}
	log := &fakeLog{events: []Event{
		posted(1, channelA),
		posted(2, channelB), // another channel
		{OrganizationID: orgA, Seq: 3, Kind: kindJoined},
		{OrganizationID: orgA, Seq: 4, Kind: "future.kind"}, // unregistered: the reader leaves it unrouted
		denied,
		posted(6, channelA),
		posted(7, channelA),
	}}
	authorize := func(e Event) (bool, error) {
		return e.AudienceMemberID == nil || *e.AudienceMemberID == sub.Member, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runCompatibilityAsync(ctx, t, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(authorize), Renderer: rendererFunc(render), BatchSize: 2}, 0, send, scheduled, reserved)
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
	testStreamRechecksAccessBeforeEachSend(t, false)
}

func testStreamRechecksAccessBeforeEachSend(t *testing.T, scheduled bool) {
	log := &fakeLog{events: []Event{posted(1, channelA), posted(2, channelA)}}
	var mu sync.Mutex
	member := true
	denied := make(chan struct{})
	authorize := func(e Event) (bool, error) {
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
	done := runCompatibilityAsync(ctx, t, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(authorize), Renderer: rendererFunc(render)}, 0, send, scheduled)
	send.waitFor(t, 1)
	// Cancel only once event 2 has been checked: the loop checks
	// cancellation before every event, so cancelling earlier would stop it
	// before event 2 and hide the revocation sentinel.
	<-denied
	got := receive(t, done)
	cancel()
	if got.cursor != 1 || !errors.Is(got.err, ErrMembershipRevoked) || !slices.Equal(send.ids(), []int64{1}) {
		t.Fatalf("Run = %d, %v, sent %v; want cursor 1, revocation and only event 1", got.cursor, got.err, send.ids())
	}
}

// A render can wait on the database. Access lost while it waits must still
// stop that event: the check comes after the render, right before the send
// (#262).
func TestStreamDeniesAccessLostWhileRendering(t *testing.T) {
	testStreamDeniesAccessLostWhileRendering(t, false)
}

func testStreamDeniesAccessLostWhileRendering(t *testing.T, scheduled bool) {
	testStreamRenderRevocationEntry(t, scheduled, false)
}

func testStreamRenderRevocationEntry(t *testing.T, scheduled, reserved bool) {
	log := &fakeLog{events: []Event{posted(1, channelA), posted(2, channelA), posted(3, channelA)}}
	var mu sync.Mutex
	member := true
	authorize := func(e Event) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return e.Seq != 2 || member, nil
	}
	rendering, release := make(chan struct{}), make(chan struct{})
	renderSlowly := func(e Event) (Outgoing, error) {
		if e.Seq == 2 {
			close(rendering)
			<-release
		}
		return render(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runCompatibilityAsync(ctx, t, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(authorize), Renderer: rendererFunc(renderSlowly)}, 0, send, scheduled, reserved)
	<-rendering
	mu.Lock()
	member = false // the membership is removed while event 2 renders
	mu.Unlock()
	close(release)
	got := <-done
	cancel()
	if got.cursor != 1 || !errors.Is(got.err, ErrMembershipRevoked) || !slices.Equal(send.ids(), []int64{1}) {
		t.Fatalf("Run = %d, %v, sent %v; want cursor 1, revocation and only event 1", got.cursor, got.err, send.ids())
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
	testStreamErrorsDoNotAdvanceTheCursor(t, false)
}

func testStreamErrorsDoNotAdvanceTheCursor(t *testing.T, scheduled bool) {
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
			authorize: func(e Event) (bool, error) {
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
			render: func(e Event) (Outgoing, error) {
				if e.Seq == 2 {
					return Outgoing{}, failure
				}
				return render(e)
			},
		},
		{
			// The render comes before the check, so its error stops the loop
			// even for an event the check would deny; nothing is skipped
			// without a decision.
			name: "renderer, on an event that would be denied",
			authorize: func(e Event) (bool, error) {
				return e.Seq != 2, nil
			},
			render: func(e Event) (Outgoing, error) {
				if e.Seq == 2 {
					return Outgoing{}, failure
				}
				return render(e)
			},
		},
		{name: "sender", authorize: allowAll, render: render, failSend: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, compatibilityCase(scheduled, func(t *testing.T) {
			log := tt.log
			if log == nil {
				log = &fakeLog{}
			}
			log.events = []Event{posted(1, channelA), posted(2, channelA), posted(3, channelA)}
			send := newRecorder()
			if tt.failSend {
				send.failOn, send.err = 2, failure
			}
			s := Stream{Hub: NewHub(), Events: log, Authorizer: tt.authorize, Renderer: tt.render, BatchSize: 1}
			selected := sub
			s = compatibilityStream(t, s, &selected, scheduled)
			// Bounded, so a loop that skips instead of failing ends the test
			// rather than waiting on the hub forever.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cursor, err := s.Run(ctx, selected, 0, send)
			if !errors.Is(err, failure) {
				t.Fatalf("Run error = %v, want %v", err, failure)
			}
			if cursor != 1 {
				t.Fatalf("cursor = %d, want 1: it must not move past the failed event", cursor)
			}
			if got := send.ids(); !slices.Equal(got, []int64{1}) {
				t.Fatalf("sent %v, want only event 1", got)
			}
		}))
	}
}

// The hub may be ahead of what a read returns (for example when the
// organisation's row is gone and reads come back empty). The loop must then
// block until the hub moves past the value it saw, not read the same empty
// range in a tight loop. synctest.Wait returns once the loop is durably
// blocked in the hub, so no sleep stands in for "blocked".
func TestStreamDoesNotSpinWhenTheHubIsAheadOfTheLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewHub()
		hub.Raise(orgA, 50)
		log := &fakeLog{events: []Event{posted(10, channelA)}, maxReads: 100}
		ctx, cancel := context.WithCancel(t.Context())
		send := newRecorder()
		done := runAsync(ctx, Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 10, send)
		synctest.Wait()
		// One read finds nothing after 10; the hub already reports 50, so
		// the loop reads once more and then waits for a value above 50.
		if reads := log.readCount(); reads != 2 {
			t.Fatalf("reads = %d while nothing new was committed, want exactly 2", reads)
		}

		log.append(posted(11, channelA))
		hub.Raise(orgA, 51)
		send.waitFor(t, 11)
		synctest.Wait()
		if reads := log.readCount(); reads != 3 {
			t.Fatalf("reads = %d after one raise, want 3", reads)
		}
		cancel()
		if got := <-done; got.cursor != 11 {
			t.Fatalf("cursor = %d, want 11", got.cursor)
		}
	})
}

func TestStreamRefusesANegativeCursor(t *testing.T) { testStreamRefusesANegativeCursor(t, false) }

func testStreamRefusesANegativeCursor(t *testing.T, scheduled bool) {
	log := &fakeLog{}
	s := Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}
	selected := sub
	s = compatibilityStream(t, s, &selected, scheduled)
	if _, err := s.Run(t.Context(), selected, -1, newRecorder()); err == nil || log.calls != 0 {
		t.Fatalf("Run(-1) = %v after %d reads, want a refusal before reading", err, log.calls)
	}
}

// Cancellation between two events of the same batch stops the loop before
// the second, even when the authorizer and renderer ignore the context.
func TestStreamChecksCancellationBetweenEvents(t *testing.T) {
	testStreamChecksCancellationBetweenEvents(t, false)
}

func testStreamChecksCancellationBetweenEvents(t *testing.T, scheduled bool) {
	cause := errors.New("session ended")
	ctx, cancel := context.WithCancelCause(t.Context())
	log := &fakeLog{events: []Event{posted(1, channelA), posted(2, channelA)}}
	send := &cancellingSender{recorder: newRecorder(), cancel: func() { cancel(cause) }}
	s := Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}
	selected := sub
	s = compatibilityStream(t, s, &selected, scheduled)
	cursor, err := s.Run(ctx, selected, 0, send)
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
	log := &fakeLog{events: []Event{posted(1, channelA)}}
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
	log := &fakeLog{events: []Event{posted(1, channelA)}}
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

func (l slowFilteredLog) EventsAfter(_ context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
	time.Sleep(l.delay)
	events := make([]Event, limit)
	for i := range events {
		events[i] = Event{OrganizationID: org, Seq: after + int64(i) + 1, Kind: kindPosted, ChannelID: channelB}
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

// A gap resets the stream before anything of that batch is delivered,
// whether it comes before the batch's first event or inside the batch.
func TestStreamResetsOnAGap(t *testing.T) { testStreamResetsOnAGap(t, false) }

func testStreamResetsOnAGap(t *testing.T, scheduled bool) {
	for _, tt := range []struct {
		name   string
		events []Event
	}{
		{"before the first event", []Event{posted(12, channelA)}},
		{"inside a batch", []Event{posted(11, channelA), posted(13, channelA)}},
	} {
		t.Run(tt.name, compatibilityCase(scheduled, func(t *testing.T) {
			log := &fakeLog{events: tt.events}
			send := newRecorder()
			// Bounded, so a regression that delivers past the gap and then
			// waits fails instead of hanging.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			got := <-runCompatibilityAsync(ctx, t, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 10, send, scheduled)
			if got.err != nil || got.cursor != 10 {
				t.Fatalf("Run = %d, %v; want 10 and the reset sent", got.cursor, got.err)
			}
			if ids := send.ids(); !slices.Equal(ids, []int64{10}) {
				t.Fatalf("sent %v, want only the reset (id 10): nothing may be delivered past the gap", ids)
			}
		}))
	}
}

// cancelOnRead cancels the stream's context during the read, as an ended
// session would, before the read reports its result.
type cancelOnRead struct {
	log    *fakeLog
	cancel context.CancelCauseFunc
	cause  error
}

func (c cancelOnRead) EventsAfter(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
	c.cancel(c.cause)
	return c.log.EventsAfter(ctx, org, after, limit)
}

// A cursor below the replay boundary gets one reset with the cursor
// unchanged and nothing delivered (#161), without a database: at the
// boundary the cursor is still valid, and an open stream whose next read
// falls below a boundary raised meanwhile resets too. A stream whose
// context ended while the reset was due sends nothing.
func TestStreamResetsBelowTheBoundary(t *testing.T) { testStreamResetsBelowTheBoundary(t, false) }

func testStreamResetsBelowTheBoundary(t *testing.T, scheduled bool) {
	t.Run("at the boundary", compatibilityCase(scheduled, func(t *testing.T) {
		log := &fakeLog{boundary: 5, events: []Event{posted(6, channelA)}}
		ctx, cancel := context.WithCancel(t.Context())
		send := newRecorder()
		done := runCompatibilityAsync(ctx, t, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 5, send, scheduled)
		send.waitFor(t, 6)
		cancel()
		if got := <-done; got.cursor != 6 || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Run = %d, %v; want 6, context.Canceled", got.cursor, got.err)
		}
	}))
	t.Run("below the boundary", compatibilityCase(scheduled, func(t *testing.T) {
		log := &fakeLog{boundary: 5, events: []Event{posted(6, channelA)}}
		send := newRecorder()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		got := <-runCompatibilityAsync(ctx, t, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 4, send, scheduled)
		if got.err != nil || got.cursor != 4 || !slices.Equal(send.ids(), []int64{4}) || !slices.Equal(send.eventNames(), []string{"reset"}) {
			t.Fatalf("Run = %d, %v, sent %v %v; want 4, nil and only a reset with id 4", got.cursor, got.err, send.eventNames(), send.ids())
		}
	}))
	t.Run("an open stream's next read falls below", compatibilityCase(scheduled, func(t *testing.T) {
		hub := NewHub()
		log := &fakeLog{events: []Event{posted(1, channelA)}}
		send := newRecorder()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		done := runCompatibilityAsync(ctx, t, Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 0, send, scheduled)
		send.waitFor(t, 1)
		if scheduled {
			synctest.Wait()
		} else {
			waitForHubWaiters(t, hub, orgA, 1)
		}
		// Retention expires the log past the stream's cursor, then a new
		// event wakes it.
		log.expire(5)
		log.append(posted(2, channelA), posted(3, channelA), posted(4, channelA), posted(5, channelA), posted(6, channelA))
		hub.Raise(orgA, 6)
		got := <-done
		if got.err != nil || got.cursor != 1 || !slices.Equal(send.ids(), []int64{1, 1}) || !slices.Equal(send.eventNames(), []string{"message", "reset"}) {
			t.Fatalf("Run = %d, %v, sent %v %v; want 1, nil and message 1 then a reset with id 1", got.cursor, got.err, send.eventNames(), send.ids())
		}
	}))
	t.Run("the context ends while the reset is due", compatibilityCase(scheduled, func(t *testing.T) {
		cause := errors.New("session ended")
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		log := cancelOnRead{log: &fakeLog{boundary: 5}, cancel: cancel, cause: cause}
		send := newRecorder()
		got := <-runCompatibilityAsync(ctx, t, Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 4, send, scheduled)
		cursor, err := got.cursor, got.err
		if cursor != 4 || !errors.Is(err, cause) || len(send.ids()) != 0 {
			t.Fatalf("Run = %d, %v, sent %v; want 4, %v and nothing sent", cursor, err, send.ids(), cause)
		}
	}))
}

// A topic subscription delivers an event without routing topics (a post
// written before topics) only when its shared render's topic is the
// subscription's: that render is the fallback, since events with routing
// topics are routed before rendering (TestStreamSubscriptionInterest). Other
// topics' events are skipped without authorization and still move the
// cursor (#304).
func TestStreamTopicSubscriptionSkipsOtherTopics(t *testing.T) {
	topicA, topicB := kernel.ID{0x7a}, kernel.ID{0x7b}
	topics := map[int64]kernel.ID{1: topicA, 2: topicB, 3: topicA}
	renderTopic := func(e Event) (Outgoing, error) {
		out, err := render(e)
		out.Topic = topics[e.Seq]
		return out, err
	}
	var authorized []int64
	authorize := func(e Event) (bool, error) {
		authorized = append(authorized, e.Seq)
		return true, nil
	}
	log := &fakeLog{events: []Event{posted(1, channelA), posted(2, channelA), posted(3, channelA)}}
	topicSub := sub
	topicSub.Topic = &topicA
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := make(chan result, 1)
	go func() {
		c, err := Stream{Hub: NewHub(), Events: log, Authorizer: authorizerFunc(authorize), Renderer: rendererFunc(renderTopic)}.Run(ctx, topicSub, 0, send)
		done <- result{c, err}
	}()
	send.waitFor(t, 1, 3)
	cancel()
	got := <-done
	if got.cursor != 3 || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Run = %d, %v; want 3, context.Canceled", got.cursor, got.err)
	}
	if !slices.Equal(authorized, []int64{1, 3}) {
		t.Fatalf("authorized %v, want only the topic's events", authorized)
	}
}

func TestStreamReplacementDeliveryAndIsolation(t *testing.T) {
	for _, kind := range []Interest{InterestMessages, InterestPresence, InterestTyping} {
		t.Run(string(kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, log, member := NewHub(), &fakeLog{}, kernel.ID{1}
				selected := sub
				selected.Member, selected.Interests = member, []Interest{kind}
				stream := Stream{Hub: h, Events: log, Renderer: rendererFunc(render), Heartbeat: time.Hour,
					Authorizer: authorizerFunc(func(e Event) (bool, error) {
						return e.AudienceMemberID == nil || *e.AudienceMemberID == member || *e.AudienceMemberID == (kernel.ID{9}), nil
					}),
					Owners: map[Interest]EphemeralOwner{kind: ownerFunc(func(Subscription, int64) (EphemeralFrame, error) {
						return EphemeralFrame{Organization: orgA, Channel: channelA, Generation: max(2, h.sequence(orgA).presence.Load().latest, h.sequence(orgA).typing.Load().latest), Outgoing: Outgoing{Name: string(kind)}}, nil
					})}}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				start := func(s Subscription) (*recorder, <-chan result) {
					send, done := newRecorder(), make(chan result, 1)
					send.failOn = -1
					go func() { c, err := stream.Run(ctx, s, 0, send); done <- result{c, err} }()
					return send, done
				}
				old, done := start(selected)
				selected.Member, selected.Account = kernel.ID{9}, account2
				peer, peerDone := start(selected)
				synctest.Wait()
				before := len(old.ids())
				member = kernel.ID{2}
				log.append(posted(1, channelA))
				if kind == InterestMessages {
					h.Raise(orgA, 1)
				} else {
					h.RaiseGeneration(orgA, kind, 3)
				}
				synctest.Wait()
				select {
				case got := <-done:
					if got.cursor != map[bool]int64{true: 0, false: 1}[kind == InterestMessages] || !errors.Is(got.err, ErrMembershipRevoked) {
						t.Fatalf("replacement Run=%+v", got)
					}
				default:
					t.Fatal("replacement stream did not end")
				}
				if len(old.ids()) != before {
					t.Fatal("replacement delivered to old member")
				}
				if len(peer.ids()) != before+1 || h.Waiting(orgA) != 1 {
					t.Fatal("another member's stream stopped or missed delivery")
				}
				cancel()
				if got := <-peerDone; !errors.Is(got.err, context.Canceled) {
					t.Fatalf("peer Run=%+v", got)
				}
			})
		})
	}
}

type unreadTestRenderer struct {
	rendererFunc
	unread int
	after  func()
}

func (r *unreadTestRenderer) RenderUnread(context.Context, Event) (Outgoing, error) {
	r.unread++
	if r.after != nil {
		r.after()
	}
	return Outgoing{Name: "unread", Ephemeral: true}, nil
}

func TestUnreadSelection(t *testing.T) {
	topic, other, member := kernel.ID{1}, kernel.ID{2}, kernel.ID{3}
	for _, tc := range []struct {
		name                              string
		messages, sidebar                 bool
		channel                           kernel.ID
		selected                          *kernel.ID
		topics                            []kernel.ID
		audience                          *kernel.ID
		denyAudience, denyMember, revoked bool
		want                              string
	}{
		{name: "feed", messages: true, sidebar: true, channel: channelA, topics: []kernel.ID{topic}, want: "message"},
		{name: "topic", messages: true, sidebar: true, channel: channelA, selected: &topic, topics: []kernel.ID{topic}, want: "message"},
		{name: "other topic", messages: true, sidebar: true, channel: channelA, selected: &other, topics: []kernel.ID{topic}, want: "unread"},
		{name: "older topic", sidebar: true, channel: channelA, selected: &topic, topics: []kernel.ID{topic}, want: "unread"},
		{name: "channel list", sidebar: true, channel: channelA, want: "unread"},
		{name: "other channel", messages: true, sidebar: true, channel: channelB, want: "unread"},
		{name: "no sidebar", messages: true, channel: channelB},
		{name: "audience", sidebar: true, channel: channelA, audience: &other, denyAudience: true},
		{name: "non-member", sidebar: true, channel: channelA, denyMember: true},
		{name: "revoked during render", sidebar: true, channel: channelA, revoked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := Subscription{Organization: orgA, Member: member, Channel: tc.channel, Topic: tc.selected}
			if tc.messages {
				sub.Interests = append(sub.Interests, InterestMessages)
			}
			if tc.sidebar {
				sub.Interests = append(sub.Interests, InterestSidebar)
			}
			e := posted(7, channelA)
			e.Topics, e.AudienceMemberID = tc.topics, tc.audience
			r := &unreadTestRenderer{rendererFunc: rendererFunc(render)}
			revoked := false
			r.after = func() { revoked = tc.revoked }
			var checks []kernel.ID
			stream := Stream{Renderer: r, Authorizer: authorizerFunc(func(e Event) (bool, error) {
				checks = append(checks, *e.AudienceMemberID)
				if revoked || tc.denyMember && *e.AudienceMemberID == member || tc.denyAudience && *e.AudienceMemberID == other {
					return false, nil
				}
				return true, nil
			})}
			send := newRecorder()
			send.failOn = -1
			sent, err := stream.deliver(t.Context(), sub, e, send)
			if sent != (tc.want != "") || errors.Is(err, ErrMembershipRevoked) != (tc.denyMember || tc.revoked) {
				t.Fatalf("sent=%v err=%v", sent, err)
			}
			want := []string{}
			if tc.want != "" {
				want = append(want, tc.want)
			}
			if !slices.Equal(send.eventNames(), want) || r.unread > 1 || (r.unread == 1) != (tc.sidebar && tc.want != "message") {
				t.Fatalf("frames=%v unread renders=%d, want %v", send.eventNames(), r.unread, want)
			}
			if tc.sidebar && tc.want != "message" && (len(checks) == 0 || checks[len(checks)-1] != member) {
				t.Fatalf("missing final bound check: %v", checks)
			}
		})
	}
}
