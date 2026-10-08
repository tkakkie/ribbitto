package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Recorded hand-over times keep deadline and delayed-delivery assertions
// independent of worker scheduling and machine speed.
func TestRecovery(t *testing.T) {
	terminated := time.Unix(100, 0)
	at := func(seconds int) time.Time { return terminated.Add(time.Duration(seconds) * time.Second) }
	for _, tc := range []struct {
		name                      string
		initial, cursor, final    uint64
		established               []time.Time
		arrivals                  map[uint64][]time.Time
		reset                     uint64
		drain, deadline           int
		reconnect, recovery, full float64
		through, all              uint64
	}{
		{"post-readiness and second reconnect", 7, 8, 9, []time.Time{at(-1), at(2), at(6)}, map[uint64][]time.Time{7: {at(1)}, 8: {at(4), at(7)}, 9: {at(8)}}, 0, 9, 5, 2, 4, 8, 2, 3},
		{"delayed reconnect above cursor", 7, 8, 9, []time.Time{at(-2), at(6)}, map[uint64][]time.Time{8: {at(-1)}, 9: {at(7)}}, 0, 9, 8, 6, 6, 7, 0, 1},
		{"idle current", 7, 8, 8, []time.Time{at(-2), at(2)}, map[uint64][]time.Time{8: {at(-1)}}, 0, 9, 5, 2, 2, 2, 0, 0},
		{"empty expected set", 7, 7, 7, []time.Time{at(-1), at(2)}, nil, 0, 9, 5, 2, 2, 2, 0, 0},
		{"reset", 7, 8, 8, []time.Time{at(-1), at(2)}, map[uint64][]time.Time{8: {at(3)}}, 1, 9, 5, 4, 5, 5, 1, 1},
		{"not reconnected", 7, 7, 7, []time.Time{at(-1)}, nil, 0, 9, 5, 0, 0, 0, 0, 0},
		{"recovery deadline", 7, 8, 9, []time.Time{at(2)}, map[uint64][]time.Time{9: {at(6)}}, 0, 9, 5, 2, 0, 6, 0, 1},
		{"drain ends before recovery", 7, 8, 9, []time.Time{at(2)}, map[uint64][]time.Time{9: {at(10)}}, 0, 9, 20, 2, 0, 0, 0, 0},
		{"drain ends before full catch-up", 7, 8, 9, []time.Time{at(2)}, map[uint64][]time.Time{8: {at(4)}, 9: {at(10)}}, 0, 9, 5, 2, 4, 0, 1, 1},
		{"deadline inclusive", 7, 8, 9, []time.Time{at(2)}, map[uint64][]time.Time{8: {at(5)}, 9: {at(9)}}, 0, 9, 5, 2, 5, 9, 1, 2},
		{"reconnect beyond drain", 7, 7, 7, []time.Time{at(10)}, nil, 0, 9, 5, 0, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := restartResult{SIGTERM: terminated, ReadyAt: at(3), RecoveryCursor: tc.cursor}
			records := []streamRecord{{EstablishedAt: tc.established, ArrivedAt: tc.arrivals, Reset: tc.reset}}
			if tc.reset > 0 {
				records = append(records, streamRecord{EstablishedAt: []time.Time{at(4)}, ArrivedAt: map[uint64][]time.Time{tc.final: {at(5)}}})
			}
			r.measureRecovery(records, tc.initial, tc.final, at(tc.drain), time.Duration(tc.deadline)*time.Second, nil, 0)
			got := r.Recovery
			resetStreams := 0
			if tc.reset > 0 {
				resetStreams = 1
			}
			if got.ResetStreams != resetStreams {
				t.Fatalf("reset streams: %d, want %d", got.ResetStreams, resetStreams)
			}
			for _, sample := range []struct {
				got  recoveryTimes
				want float64
			}{{got.Reconnected, tc.reconnect}, {got.OutageRecovered, tc.recovery}, {got.FullyCaughtUp, tc.full}} {
				missing := 0
				if sample.want == 0 {
					missing = 1
				}
				if sample.got != (recoveryTimes{sample.want, sample.want, sample.want, missing}) {
					t.Fatalf("times: %+v, want %v with %d incomplete", sample.got, sample.want, missing)
				}
			}
			if got.DeliveriesThroughRecoveryCursorAfterReconnect != tc.through || got.AllDeliveriesAfterReconnect != tc.all {
				t.Fatalf("deliveries: %+v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil || !strings.Contains(string(encoded), `"ResetStreams":`) || !strings.Contains(string(encoded), `"deliveries_through_recovery_cursor_after_reconnect":`) || !strings.Contains(string(encoded), `"all_deliveries_after_reconnect":`) {
				t.Fatalf("JSON fields: %s: %v", encoded, err)
			}
		})
	}
	for _, r := range []restartResult{{IncompleteSetup: true, SIGTERM: terminated}, {}} {
		r.measureRecovery(nil, 7, 7, at(9), time.Second, nil, 0)
		if r.Recovery != nil {
			t.Fatal("reported a restart that did not run")
		}
	}
}

func TestRecoveryDistribution(t *testing.T) {
	terminated := time.Unix(100, 0)
	records := make([]streamRecord, 101)
	for i := range 100 {
		records[i].EstablishedAt = []time.Time{terminated.Add(time.Duration(100-i) * time.Second)}
	}
	r := restartResult{SIGTERM: terminated, RecoveryCursor: 7}
	r.measureRecovery(records, 7, 7, terminated.Add(100*time.Second), 100*time.Second, nil, 0)
	want := recoveryTimes{50, 95, 100, 1}
	if r.Recovery.Reconnected != want || r.Recovery.OutageRecovered != want || r.Recovery.FullyCaughtUp != want {
		t.Fatalf("distribution: %+v", r.Recovery)
	}
}

func TestRecoveryFlags(t *testing.T) {
	for _, value := range []string{"0", "-1ns", "5m1ns"} {
		if err := run([]string{"-recover-deadline=" + value}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "finite limits") {
			t.Fatalf("accepted deadline %s: %v", value, err)
		}
	}
	if err := run([]string{"-recover-deadline=1s"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires -restart-after") {
		t.Fatalf("accepted deadline without restart: %v", err)
	}
}

func TestLiveAgain(t *testing.T) {
	terminated := time.Unix(100, 0)
	at := func(seconds int) time.Time { return terminated.Add(time.Duration(seconds) * time.Second) }
	for _, tc := range []struct {
		name        string
		established []time.Time
		arrivals    map[uint64][]time.Time
		first       time.Time
		reset       uint64
		want        recoveryTimes
	}{
		{"reconnect before later post", []time.Time{at(-1), at(2)}, map[uint64][]time.Time{9: {at(4)}}, at(3), 0, recoveryTimes{4, 4, 4, 0}},
		{"in-flight post excluded", []time.Time{at(2)}, map[uint64][]time.Time{8: {at(3)}, 9: {at(5)}}, at(4), 0, recoveryTimes{5, 5, 5, 0}},
		// The old POST retries after reconnect and commits above the new POST.
		{"retry excluded", []time.Time{at(2)}, map[uint64][]time.Time{12: {at(4)}, 9: {at(6)}}, at(3), 0, recoveryTimes{6, 6, 6, 0}},
		{"retry alone incomplete", []time.Time{at(2)}, map[uint64][]time.Time{12: {at(4)}}, at(3), 0, recoveryTimes{0, 0, 0, 1}},
		{"drain inclusive", []time.Time{at(2)}, map[uint64][]time.Time{9: {at(9)}}, at(3), 0, recoveryTimes{9, 9, 9, 0}},
		{"after drain incomplete", []time.Time{at(2)}, map[uint64][]time.Time{9: {at(9).Add(time.Nanosecond)}}, at(3), 0, recoveryTimes{0, 0, 0, 1}},
		{"late reconnect no later post", []time.Time{at(7)}, map[uint64][]time.Time{9: {at(8)}}, at(6), 0, recoveryTimes{0, 0, 0, 1}},
		{"first attempt at reconnect excluded", []time.Time{at(2)}, map[uint64][]time.Time{9: {at(4)}}, at(2), 0, recoveryTimes{0, 0, 0, 1}},
		{"first reconnect and earliest arrival", []time.Time{at(6), at(2)}, map[uint64][]time.Time{9: {at(5), at(7)}, 10: {at(4)}}, at(3), 0, recoveryTimes{4, 4, 4, 0}},
		{"post between reconnects", []time.Time{at(2), at(6)}, map[uint64][]time.Time{9: {at(5), at(7)}}, at(4), 0, recoveryTimes{5, 5, 5, 0}},
		{"arrival must follow reconnect", []time.Time{at(2)}, map[uint64][]time.Time{9: {at(1), at(2)}}, at(3), 0, recoveryTimes{0, 0, 0, 1}},
		{"unrelated event", []time.Time{at(2)}, map[uint64][]time.Time{11: {at(4)}}, at(3), 0, recoveryTimes{0, 0, 0, 1}},
		{"no reconnect", []time.Time{at(-1)}, map[uint64][]time.Time{9: {at(4)}}, at(3), 0, recoveryTimes{0, 0, 0, 1}},
		{"reset excluded", []time.Time{at(2)}, map[uint64][]time.Time{9: {at(4)}}, at(3), 1, recoveryTimes{}},
		{"reset incomplete excluded", nil, nil, at(3), 1, recoveryTimes{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := streamRecord{EstablishedAt: tc.established, ArrivedAt: tc.arrivals, Reset: tc.reset,
				Sequences: map[uint64]*receipt{8: {Marker: "old"}, 9: {Marker: "next"}, 10: {Marker: "later"}, 12: {Marker: "old"}}}
			// An unanswered POST can still commit and deliver with a lost response.
			posts := map[string]*post{"old": {sent: at(1), answered: true}, "next": {sent: tc.first}, "later": {sent: at(3)}}
			r := restartResult{SIGTERM: terminated, RecoveryCursor: 8}
			r.measureRecovery([]streamRecord{rec}, 7, 12, at(9), time.Second, posts, 1)
			if r.Recovery.LiveAgain == nil || *r.Recovery.LiveAgain != tc.want || r.Recovery.ResetStreams != int(tc.reset) {
				t.Fatalf("live again: %+v, resets: %d; want %+v, resets: %d", r.Recovery.LiveAgain, r.Recovery.ResetStreams, tc.want, tc.reset)
			}
		})
	}
}

func TestLiveAgainDistributionAndIdle(t *testing.T) {
	terminated := time.Unix(100, 0)
	records := make([]streamRecord, 102)
	for i := range 100 {
		records[i] = streamRecord{EstablishedAt: []time.Time{terminated.Add(time.Second)},
			Sequences: map[uint64]*receipt{9: {Marker: "next"}},
			ArrivedAt: map[uint64][]time.Time{9: {terminated.Add(time.Duration(102-i) * time.Second)}}}
	}
	records[101].Reset = 1
	posts := map[string]*post{"next": {sent: terminated.Add(2 * time.Second)}}
	for _, rate := range []int{1, 0} {
		r := restartResult{SIGTERM: terminated, RecoveryCursor: 8}
		r.measureRecovery(records, 7, 9, terminated.Add(102*time.Second), time.Second, posts, rate)
		if rate > 0 {
			if r.Recovery.LiveAgain == nil || *r.Recovery.LiveAgain != (recoveryTimes{52, 97, 102, 1}) {
				t.Fatalf("live-again distribution: %+v", r.Recovery.LiveAgain)
			}
		} else {
			// Idle is N/A regardless of other retained recovery state.
			encoded, err := json.Marshal(r.Recovery)
			if err != nil || r.Recovery.LiveAgain != nil || !strings.Contains(string(encoded), `"LiveAgain":null`) {
				t.Fatalf("idle live again: %s: %v", encoded, err)
			}
		}
	}
}
