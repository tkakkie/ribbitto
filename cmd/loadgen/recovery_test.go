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
		{"post-readiness and second reconnect", 7, 8, 9, []time.Time{at(-1), at(2), at(6)}, map[uint64][]time.Time{8: {at(4), at(7)}, 9: {at(8)}}, 0, 9, 5, 2, 4, 8, 2, 3},
		{"delayed reconnect above cursor", 7, 8, 9, []time.Time{at(-2), at(6)}, map[uint64][]time.Time{8: {at(-1)}, 9: {at(7)}}, 0, 9, 8, 6, 6, 7, 0, 1},
		{"idle current", 7, 8, 8, []time.Time{at(-2), at(2)}, map[uint64][]time.Time{8: {at(-1)}}, 0, 9, 5, 2, 2, 2, 0, 0},
		{"empty expected set", 7, 7, 7, []time.Time{at(-1), at(2)}, nil, 0, 9, 5, 2, 2, 2, 0, 0},
		{"reset", 7, 8, 8, []time.Time{at(-1), at(2)}, map[uint64][]time.Time{8: {at(3)}}, 1, 9, 5, 2, 0, 0, 1, 1},
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
			r.measureRecovery(records, tc.initial, tc.final, at(tc.drain), time.Duration(tc.deadline)*time.Second)
			got := r.Recovery
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
			if err != nil || !strings.Contains(string(encoded), `"deliveries_through_recovery_cursor_after_reconnect":`) || !strings.Contains(string(encoded), `"all_deliveries_after_reconnect":`) {
				t.Fatalf("JSON fields: %s: %v", encoded, err)
			}
		})
	}
	for _, r := range []restartResult{{IncompleteSetup: true, SIGTERM: terminated}, {}} {
		r.measureRecovery(nil, 7, 7, at(9), time.Second)
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
	r.measureRecovery(records, 7, 7, terminated.Add(100*time.Second), 100*time.Second)
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
