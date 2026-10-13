package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStage2Slots(t *testing.T) {
	start := time.Unix(123, 0)
	for _, group := range fixedStage2().Groups {
		s, err := newStage2Schedule(group, 42, start)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := newStage2Schedule(group, 42, start)
		other, _ := newStage2Schedule(group, 43, start)
		shifted, _ := newStage2Schedule(group, 42, start.Add(time.Hour))
		if !reflect.DeepEqual(s, again) || reflect.DeepEqual(s, other) {
			t.Fatal("schedule must repeat by seed and vary across seeds")
		}
		counts, previous := map[string]int{}, map[string]stage2Slot{}
		for i, slot := range s.slots {
			counts[slot.Operation+"."+slot.Endpoint]++
			if i > 0 && slot.At.Before(s.slots[i-1].At) || slot.At.Before(start) || !slot.At.Before(start.Add(600*time.Second)) || slot.Expires.After(start.Add(600*time.Second)) {
				t.Fatal("unordered or out-of-window slot")
			}
			if shifted.slots[i].At.Sub(slot.At) != time.Hour || shifted.slots[i].Endpoint != slot.Endpoint {
				t.Fatal("start must only translate slots")
			}
			if prev, ok := previous[slot.Operation]; ok && (prev.Expires != slot.At || prev.Endpoint == slot.Endpoint || prev.LogicalID+1 != slot.LogicalID) {
				t.Fatal("fixed-rate slots must alternate endpoints and preserve logical IDs")
			}
			previous[slot.Operation] = slot
		}
		for kind, rate := range map[string]int{"post": group.PostsPerSecond, "read": group.ReadsPerSecond, "page": group.PagesPerSecond, "replacement": group.ReplacementsPerSecond} {
			if counts[kind+".feed"] != rate*300 || counts[kind+".topic"] != rate*300 {
				t.Fatalf("wrong fixed load for %s: %v", kind, counts)
			}
		}
	}
}

func TestStage2MissedAndLateness(t *testing.T) {
	start := time.Unix(123, 0)
	slot := stage2Slot{"post", "feed", 7, start, start.Add(time.Second)}
	s := stage2Schedule{slots: []stage2Slot{slot}}
	if len(s.poll(start.Add(-time.Nanosecond), func(stage2Slot) bool { t.Fatal("early dispatch"); return true })) != 0 {
		t.Fatal("early slot")
	}
	for _, tc := range []struct {
		late     time.Duration
		capacity bool
		missed   uint64
	}{{50 * time.Millisecond, true, 0}, {time.Second, true, 1}, {0, false, 1}} {
		s.next = 0
		due := s.poll(start.Add(tc.late), func(stage2Slot) bool { return tc.capacity })
		if tc.missed == 0 {
			due[0].finish(start, due[0].Dispatch, start.Add(tc.late+time.Millisecond), time.Time{}, 1, 200, "succeeded")
		}
		ops, attempts := stage2Summarize(due)
		op := ops[0]
		if op.Scheduled != 1 || op.Missed != tc.missed || op.Executed != 1-tc.missed || op.HTTP.Count != 1-tc.missed || op.DispatchLateness.Count != 1-tc.missed || op.ScheduledToResponse.Count != 1 {
			t.Fatalf("missed slot must remain counted: %+v", op)
		}
		if tc.missed == 1 && (op.HTTP != (stage2Latency{}) || op.DispatchLateness != (stage2Latency{}) || op.ScheduledToResponse.P50MS != float64(tc.late)/float64(time.Millisecond)) {
			t.Fatalf("miss must retain only scheduled-to-detection time: %+v", op)
		}
		if tc.missed == 0 && (op.DispatchLateness.P50MS != 50 || op.ScheduledToResponse.P50MS != 51 || attempts[0].OffsetNS != tc.late.Nanoseconds()) {
			t.Fatalf("incorrect operation clocks: %+v %+v", op, attempts)
		}
		if len(s.poll(start.Add(2*time.Second), func(stage2Slot) bool { return true })) != 0 {
			t.Fatal("slot dispatched twice")
		}
	}
	full, _ := newStage2Schedule(fixedStage2().Groups[0], 42, start)
	due := full.poll(start.Add(600*time.Second), func(stage2Slot) bool { t.Fatal("expired dispatch"); return true })
	ops, _ := stage2Summarize(due)
	for _, op := range ops {
		if op.Missed != op.Scheduled || op.ScheduledToResponse.Count != op.Scheduled || op.HTTP != (stage2Latency{}) || op.DispatchLateness != (stage2Latency{}) || op.Scheduled == 0 {
			t.Fatalf("overload silently lost slots: %+v", op)
		}
	}
}

func TestStage2SummaryExecutedSamples(t *testing.T) {
	start := time.Unix(123, 0)
	for _, outcome := range []string{"succeeded", "changed", "failed", "timeout", "noop", "unverified"} {
		t.Run(outcome, func(t *testing.T) {
			slot := stage2Slot{Operation: "read", Endpoint: "feed", At: start}
			observations := []stage2Observation{{Slot: slot, Dispatch: start.Add(25 * time.Millisecond), Completed: start.Add(125 * time.Millisecond), Outcome: outcome}}
			for range 4 {
				observations = append(observations, stage2Observation{Slot: slot, Completed: start.Add(time.Second), Outcome: "missed"})
			}
			ops, _ := stage2Summarize(observations)
			op := ops[2]
			if op.HTTP != (stage2Latency{1, 100, 100, 100}) {
				t.Fatalf("missed slots must not lower executed HTTP percentiles: %+v", op.HTTP)
			}
			if op.DispatchLateness != (stage2Latency{1, 25, 25, 25}) {
				t.Fatalf("dispatch lateness must include every executed outcome and exclude misses: %+v", op.DispatchLateness)
			}
			if op.Scheduled != 5 || op.Executed != 1 || op.Missed != 4 || op.ScheduledToResponse != (stage2Latency{5, 1000, 1000, 1000}) {
				t.Fatalf("misses must retain counts and scheduled-to-detection time: %+v", op)
			}
		})
	}
}

type stage2FakeHTTP struct {
	now    *time.Time
	status int
	err    error
	body   *stage2FakeBody
}

func (f stage2FakeHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	*f.now = f.now.Add(2 * time.Millisecond)
	if f.err != nil && f.err != io.ErrUnexpectedEOF {
		return nil, f.err
	}
	body := f.body
	if body == nil {
		body = &stage2FakeBody{now: f.now, err: f.err}
	}
	return &http.Response{StatusCode: f.status, Body: body}, nil
}

type stage2FakeBody struct {
	now        *time.Time
	read       bool
	err        error
	reader     io.Reader
	consumed   int
	closedAt   time.Time
	closeDelay time.Duration
	closeErr   error
}

func (b *stage2FakeBody) Read(p []byte) (int, error) {
	*b.now = b.now.Add(3 * time.Millisecond)
	if b.reader != nil {
		n, err := b.reader.Read(p)
		b.consumed += n
		return n, err
	}
	if b.read {
		if b.err != nil {
			return 0, b.err
		}
		return 0, io.EOF
	}
	b.read = true
	return copy(p, "state"), nil
}
func (b *stage2FakeBody) Close() error {
	b.closedAt = *b.now
	*b.now = b.now.Add(b.closeDelay)
	return b.closeErr
}

func TestStage2HTTPAccounting(t *testing.T) {
	start := time.Unix(123, 0)
	var observations []stage2Observation
	for i, tc := range []struct {
		status  int
		err     error
		changed bool
		outcome string
	}{
		{204, nil, true, "changed"}, {204, nil, false, "noop"}, {503, nil, false, "failed"}, {0, context.DeadlineExceeded, false, "timeout"}, {204, io.ErrUnexpectedEOF, false, "failed"},
	} {
		now := start.Add(time.Millisecond)
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/read", nil)
		o := stage2Observation{Slot: stage2Slot{Operation: "read", Endpoint: "feed", LogicalID: uint64(i), At: start}}
		o = stage2HTTP(context.Background(), stage2FakeHTTP{now: &now, status: tc.status, err: tc.err}, req, o, start, func() time.Time { return now }, func(body []byte) bool {
			if string(body) != "state" {
				t.Fatal("incomplete body")
			}
			return tc.changed
		})
		if o.Outcome != tc.outcome || len(o.Attempts) != 1 || o.Attempts[0].Status != tc.status || o.Attempts[0].Attempt != 1 || o.Attempts[0].LogicalID != uint64(i) {
			t.Fatalf("incorrect attempt: %+v", o)
		}
		observations = append(observations, o)
	}
	observations = append(observations, stage2Observation{Slot: observations[0].Slot, Completed: start.Add(time.Second), Outcome: "missed"})
	ops, attempts := stage2Summarize(observations)
	op := ops[2]
	if op.Scheduled != 6 || op.Executed != 5 || op.Missed != 1 || op.Succeeded != 1 || op.Changed != 1 || op.Noop != 1 || op.Failed != 4 || op.Timeouts != 1 {
		t.Fatalf("no-op must not satisfy changed read load: %+v", op)
	}
	if op.HTTP != (stage2Latency{5, 8, 8, 8}) || op.DispatchLateness != (stage2Latency{5, 1, 1, 1}) || op.ScheduledToResponse != (stage2Latency{6, 9, 1000, 1000}) {
		t.Fatalf("failure samples must remain in percentiles: %+v", op)
	}
	w := stage2WindowFixture()
	w.Operations, w.Attempts = ops, attempts
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeStage2(raw, &stage2Window{}); err != nil {
		t.Fatal(err)
	}
}

func TestStage2HTTPStatuses(t *testing.T) {
	for _, tc := range []struct {
		operation, endpoint string
		status              int
		outcome             string
	}{
		{"post", "feed", 200, "succeeded"}, {"post", "feed", 303, "failed"},
		{"post", "topic", 303, "succeeded"}, {"post", "topic", 200, "failed"},
		{"read", "feed", 204, "changed"}, {"read", "feed", 200, "failed"}, {"read", "feed", 303, "failed"},
		{"read", "topic", 204, "changed"}, {"read", "topic", 200, "failed"},
		{"page", "feed", 200, "succeeded"}, {"page", "topic", 200, "succeeded"}, {"page", "topic", 204, "failed"},
		{"replacement", "feed", 200, "succeeded"}, {"replacement", "topic", 200, "succeeded"}, {"replacement", "topic", 503, "failed"},
	} {
		t.Run(tc.operation+"/"+tc.endpoint+"/"+http.StatusText(tc.status), func(t *testing.T) {
			start := time.Unix(123, 0)
			now := start
			body := &stage2FakeBody{now: &now, reader: strings.NewReader("")}
			req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
			req.Header.Set("HX-Request", "true")
			o := stage2Observation{Slot: stage2Slot{Operation: tc.operation, Endpoint: tc.endpoint, At: start}}
			verified := false
			o = stage2HTTP(context.Background(), stage2FakeHTTP{now: &now, status: tc.status, body: body}, req, o, start, func() time.Time { return now }, func(proof []byte) bool {
				if len(proof) != 0 {
					t.Fatal("read verifier must work with an empty response")
				}
				verified = true
				return true // Stand-in for persisted read-state proof from later slices.
			})
			if o.Outcome != tc.outcome || verified != (tc.outcome == "changed") {
				t.Fatalf("%s %s status %d: outcome=%s verified=%v, want %s", tc.operation, tc.endpoint, tc.status, o.Outcome, verified, tc.outcome)
			}
		})
	}
}

func TestStage2HTTPBoundedBody(t *testing.T) {
	start := time.Unix(123, 0)
	now := start
	size := stage2ProofBodyLimit + 8192
	body := &stage2FakeBody{now: &now, reader: strings.NewReader(strings.Repeat("x", size))}
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/read", nil)
	o := stage2Observation{Slot: stage2Slot{Operation: "read", Endpoint: "feed", At: start}}
	verified := false
	o = stage2HTTP(context.Background(), stage2FakeHTTP{now: &now, status: 204, body: body}, req, o, start, func() time.Time { return now }, func(proof []byte) bool {
		if len(proof) != stage2ProofBodyLimit || string(proof) != strings.Repeat("x", stage2ProofBodyLimit) {
			t.Fatalf("retained proof: %d bytes, want the bounded %d-byte prefix", len(proof), stage2ProofBodyLimit)
		}
		verified = true
		return true
	})
	if body.consumed != size {
		t.Fatalf("body consumed: %d bytes, want all %d", body.consumed, size)
	}
	if !verified || o.Outcome != "changed" || o.Completed != body.closedAt {
		t.Fatalf("oversized body must drain before clock end without failing: %+v", o)
	}
}

func TestStage2HTTPClockBeforeClose(t *testing.T) {
	for _, tc := range []struct {
		err     error
		outcome string
	}{{nil, "changed"}, {io.ErrClosedPipe, "failed"}, {context.DeadlineExceeded, "timeout"}} {
		t.Run(tc.outcome, func(t *testing.T) {
			start := time.Unix(123, 0)
			now := start
			body := &stage2FakeBody{now: &now, reader: strings.NewReader(""), closeDelay: 100 * time.Millisecond, closeErr: tc.err}
			req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/read", nil)
			o := stage2Observation{Slot: stage2Slot{Operation: "read", Endpoint: "feed", At: start}}
			o = stage2HTTP(context.Background(), stage2FakeHTTP{now: &now, status: 204, body: body}, req, o, start, func() time.Time { return now }, func([]byte) bool {
				now = now.Add(20 * time.Millisecond)
				return true
			})
			ops, _ := stage2Summarize([]stage2Observation{o})
			if o.Completed != start.Add(5*time.Millisecond) || ops[2].HTTP != (stage2Latency{1, 5, 5, 5}) {
				t.Fatalf("Close or verification inflated HTTP time: completed=%s HTTP=%+v, want 5ms", o.Completed.Sub(start), ops[2].HTTP)
			}
			if body.closedAt != o.Completed || now.Before(start.Add(105*time.Millisecond)) || o.Outcome != tc.outcome {
				t.Fatalf("Close must run and retain its error: closed=%s outcome=%s, want %s", body.closedAt.Sub(start), o.Outcome, tc.outcome)
			}
		})
	}
}

func TestStage2PercentilesAndOutage(t *testing.T) {
	values := make([]time.Duration, 100)
	for i := range values {
		values[i] = time.Duration(100-i) * time.Millisecond
	}
	if got := stage2Percentiles(values); got != (stage2Latency{100, 50, 95, 99}) || values[0] != 100*time.Millisecond {
		t.Fatalf("nearest rank or mutated input: %+v", got)
	}
	if stage2Percentiles(nil) != (stage2Latency{}) {
		t.Fatal("empty summary")
	}
	start := time.Unix(123, 0)
	o := stage2Observation{Slot: stage2Slot{"replacement", "topic", 9, start, start.Add(time.Second)}, Dispatch: start.Add(250 * time.Millisecond)}
	o.finish(start, o.Dispatch, start.Add(500*time.Millisecond), start, 1, 503, "failed")
	o.finish(start, start.Add(750*time.Millisecond), start.Add(time.Second), start, 2, 200, "succeeded")
	if a := o.Attempts[1]; len(o.Attempts) != 2 || a.OutageNS != int64(time.Second) || a.OffsetNS != int64(750*time.Millisecond) || a.Attempt != 2 {
		t.Fatalf("incorrect replacement clock: %+v", a)
	}
}
