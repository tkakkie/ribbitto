package main

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A caller owns the stream until close. Application is explicit: the later
// oracle supplies this callback; mere receipt must never acknowledge a frame.
type stage2Stream struct {
	d              *stage2Driver
	member, cursor uint64
	apply          func(uint64, string, string) bool
	mu             sync.Mutex
	closed         bool
	body           io.ReadCloser
	cancel         context.CancelFunc
	done           chan struct{}
	err            error // Read only after done; retained even during planned cancellation.
}

func (d *stage2Driver) establishStream(ctx context.Context, member uint64, start time.Time, now func() time.Time, apply func(uint64, string, string) bool) (*stage2Stream, []stage2Attempt, error) {
	if member >= uint64(len(d.members)) || apply == nil || !d.members[member].stream.TryLock() {
		return nil, nil, fmt.Errorf("stage-2 stream unavailable")
	}
	s := &stage2Stream{d: d, member: member, apply: apply}
	o, err := d.drive(ctx, stage2Observation{Slot: stage2StreamPage(member)}, start, now, "")
	// Establishment's page is not a scheduled page operation.
	for i := range o.Attempts {
		o.Attempts[i].Operation = "establishment"
	}
	m := d.members[member]
	m.mu.Lock()
	s.cursor = m.pages[m.streamScope].cursor
	m.mu.Unlock()
	if err == nil && o.Outcome == "succeeded" && s.cursor > 0 {
		o.Slot.Operation = "establishment"
		s.connect(ctx, &o, start, time.Time{}, now, 1, 10*time.Second)
		if o.Outcome == "succeeded" {
			return s, o.Attempts, nil
		}
	}
	m.stream.Unlock()
	return nil, o.Attempts, fmt.Errorf("establishing stage-2 stream failed")
}

func (s *stage2Stream) connect(ctx context.Context, o *stage2Observation, start, offline time.Time, now func() time.Time, attempt uint64, budget time.Duration) {
	streamCtx, cancel := context.WithCancel(ctx)
	// The header deadline ends at admission; an admitted stream stays alive.
	timer := time.AfterFunc(budget, cancel)
	endpoint := s.d.transport.origin.Scheme + "://" + s.d.transport.origin.Host + "/organizations/" + url.PathEscape(s.d.data.Slug) + "/events"
	q := url.Values{"want": {"sidebar,messages,typing"}, "channel": {s.d.data.Stage2.HotChannelID}}
	if scope := s.d.members[s.member].streamScope; scope != "feed" {
		q.Set("topic", scope)
	}
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		timer.Stop()
		cancel()
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", strconv.FormatUint(s.cursor, 10))
	req.AddCookie(&http.Cookie{Name: "__Host-session", Value: s.d.data.Accounts[s.member].Tokens[0]})
	dispatch := now()
	r, err := s.d.transport.RoundTrip(req)
	timer.Stop()
	status, outcome := 0, "failed"
	if err == nil {
		status = r.StatusCode
		if status == 200 && r.ProtoMajor == 1 && r.ProtoMinor == 1 && strings.Split(r.Header.Get("Content-Type"), ";")[0] == "text/event-stream" && streamCtx.Err() == nil {
			outcome = "succeeded"
			s.body, s.cancel, s.done = r.Body, cancel, make(chan struct{})
			s.err = nil
			go s.consume(streamCtx)
		} else {
			_ = r.Body.Close()
		}
	}
	if outcome != "succeeded" {
		if streamCtx.Err() != nil && ctx.Err() == nil {
			outcome = "timeout"
		}
		cancel()
	}
	o.finish(start, dispatch, now(), offline, attempt, status, outcome)
}

func (s *stage2Stream) consume(ctx context.Context) {
	defer close(s.done)
	defer func() { _ = s.body.Close() }()
	markers := regexp.MustCompile(`stage2-post:(?:feed|topic):[0-9]+`)
	reset := readReset(s.body, nil, func(seq uint64, event, data string) {
		if !s.apply(seq, event, data) || seq == 0 || seq > (1<<63)-1 || (event != "message" && event != "messages-moved") {
			return
		}
		s.cursor = max(s.cursor, seq)
		s.d.applyFrame(s.member, seq, "")
		for _, marker := range markers.FindAllString(data, -1) {
			s.d.applyFrame(s.member, seq, marker)
		}
	})
	if reset {
		s.err = fmt.Errorf("stage-2 stream reset")
	} else if ctx.Err() == nil {
		s.err = fmt.Errorf("stage-2 stream disconnected")
	}
}

func (s *stage2Stream) stop() error {
	if s.cancel != nil {
		s.cancel()
		_ = s.body.Close()
		<-s.done // Join intake before releasing member ownership.
		s.cancel = nil
	}
	return s.err
}

func (s *stage2Stream) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.stop()
	if !s.closed {
		s.closed = true
		s.d.members[s.member].stream.Unlock()
	}
	return err
}

// The caller chooses an existing stream. Busy and expired slots never wait;
// only 503 retries, within the same logical replacement's two-second budget.
// wait is context-aware virtual time in tests, a timer in the later runner.
func (s *stage2Stream) replace(ctx context.Context, o stage2Observation, start time.Time, seed uint64, now func() time.Time, wait func(context.Context, time.Duration) bool) (stage2Observation, error) {
	if o.Outcome == "missed" {
		return o, nil
	}
	if !now().Before(o.Slot.Expires) {
		o.Dispatch, o.Completed, o.Outcome = time.Time{}, now(), "missed"
		return o, nil
	}
	fail := func(err error) (stage2Observation, error) {
		o.Dispatch, o.Completed, o.Outcome = now(), now(), "failed"
		return o, err
	}
	if o.Slot.Operation != "replacement" || o.Slot.Endpoint != stage2StreamPage(s.member).Endpoint || !s.mu.TryLock() {
		return fail(fmt.Errorf("stage-2 replacement unavailable"))
	}
	defer s.mu.Unlock()
	if s.closed || s.cancel == nil || ctx.Err() != nil {
		return fail(fmt.Errorf("stage-2 stream not connected or caller cancelled"))
	}
	o.Member, o.MemberSelected = s.member, true
	offline := now()
	if err := s.stop(); err != nil {
		return fail(err)
	}
	rng := rand.New(rand.NewPCG(seed, o.Slot.LogicalID))
	deadline := offline.Add(2 * time.Second)
	for attempt := uint64(1); ; attempt++ {
		delay := 250*time.Millisecond + time.Duration(rng.Int64N(int64(250*time.Millisecond)+1))
		if !now().Add(delay).Before(deadline) || !wait(ctx, delay) || ctx.Err() != nil {
			o.Dispatch, o.Completed, o.Outcome = now(), now(), "timeout"
			if len(o.Attempts) > 0 {
				o.Dispatch = start.Add(time.Duration(o.Attempts[0].OffsetNS))
			}
			return o, nil
		}
		s.connect(ctx, &o, start, offline, now, attempt, deadline.Sub(now()))
		if o.Outcome != "failed" || o.Attempts[len(o.Attempts)-1].Status != 503 || !now().Before(deadline) {
			return o, nil
		}
	}
}
