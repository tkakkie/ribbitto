package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func stage2MetricFixture(second int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"database":{"queries":%d},"pool":{"acquire_count":%d,"acquire_duration_ns":%d,"empty_acquire_count":%d,"empty_acquire_wait_ns":%d,"canceled_acquire_count":%d,"acquired_conns":%d,"idle_conns":1,"total_conns":36,"max_conns":36},"runtime":{"goroutines":12,"heap_inuse_bytes":4096},"streams":{"open":1000},"scheduler":{"enabled":true,"pending":7,"processing_duration_bucket_counts":[1,2,3,4,5,6]},"delivery":{"enabled":false,"extra":42}}`, second, 1000+second*100, second*2000000, 10+second, 1000000+second*5000000, 3+second, second%37))
}

func TestStage2Telemetry(t *testing.T) {
	start := time.Unix(123, 0)
	clock := start
	wait := func(_ context.Context, delay time.Duration) bool { clock = clock.Add(delay); return true }
	buffer := stage2MetricFixture(0)
	samples, err := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, wait, func(context.Context) (json.RawMessage, error) {
		copy(buffer, stage2MetricFixture(0))
		return buffer, nil
	})
	if err != nil || len(samples) != 601 || clock.Sub(start) != 600*time.Second {
		t.Fatalf("per-second schedule: samples=%d elapsed=%s err=%v", len(samples), clock.Sub(start), err)
	}
	buffer[0] = 'x'
	for i, s := range samples {
		phase := "traffic"
		if i == 600 {
			phase = "boundary"
		}
		if s.OffsetNS != int64(i)*int64(time.Second) || s.Phase != phase || !reflect.DeepEqual(s.Metrics, stage2MetricFixture(0)) {
			t.Fatalf("full snapshot or clock lost at %d: %+v", i, s)
		}
		samples[i].Metrics = stage2MetricFixture(i)
	}
	// Neither terminal nor drain gauges belong to occupancy maxima.
	samples[600].Metrics = json.RawMessage(strings.Replace(string(samples[600].Metrics), `"acquired_conns":8`, `"acquired_conns":999`, 1))
	samples = append(samples, stage2Sample{601 * int64(time.Second), "drain", nil})
	ranges, err := stage2AggregateTelemetry(samples)
	if err != nil || len(ranges) != 11 {
		t.Fatalf("planned ranges: %d %v", len(ranges), err)
	}
	for i, r := range ranges {
		seconds, at := 60, (i-1)*60
		if i == 0 {
			seconds, at = 600, 0
		}
		if r.StartNS != int64(at)*int64(time.Second) || r.EndNS != int64(at+seconds)*int64(time.Second) || !r.BoundaryCovered || r.Samples != seconds || r.Missing != 0 || r.ElapsedNS != int64(seconds)*int64(time.Second) || len(r.ResetOffsetsNS) != 0 || r.SampledMaxNotExactPeak["acquired_conns"] != 36 || r.MeanEmptyWaitNS != 5000000 || r.WaitingPressure != .005 {
			t.Fatalf("range evidence lost: %+v", r)
		}
		want := map[string]int64{"acquire_count": int64(seconds * 100), "acquire_duration_ns": int64(seconds * 2000000), "empty_acquire_count": int64(seconds), "empty_acquire_wait_ns": int64(seconds * 5000000), "canceled_acquire_count": int64(seconds)}
		if !reflect.DeepEqual(r.Delta, want) {
			t.Fatalf("counter counts/durations: %v want %v", r.Delta, want)
		}
	}
	for _, change := range []string{"hole", "boundary", "reset", "invalid", "duplicate", "negative", "zero-empty", "jitter", "empty"} {
		t.Run(change, func(t *testing.T) {
			copySamples := append([]stage2Sample(nil), samples[:601]...)
			switch change {
			case "jitter":
				copySamples[600].OffsetNS += int64(time.Second / 2)
			case "empty":
				copySamples = nil
			case "hole":
				copySamples = append(copySamples[:30], copySamples[31:]...)
			case "boundary":
				copySamples = copySamples[:600]
			case "reset":
				copySamples[30].Metrics = stage2MetricFixture(0)
			case "invalid":
				copySamples[30].Metrics = json.RawMessage(strings.Replace(string(copySamples[30].Metrics), `"acquire_count":4000`, `"acquire_count":null`, 1))
			case "duplicate":
				copySamples[30].OffsetNS = copySamples[29].OffsetNS
			case "negative":
				copySamples[0].OffsetNS = -1
			case "zero-empty":
				for i := range copySamples {
					copySamples[i].Metrics = stage2MetricFixture(0)
				}
			}
			got, err := stage2AggregateTelemetry(copySamples)
			if change == "invalid" || change == "duplicate" || change == "negative" {
				if err == nil {
					t.Fatal("invalid telemetry accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r := got[0]
			if change == "jitter" && (r.ElapsedNS != int64(600500*time.Millisecond) || r.WaitingPressure != 3e9/600.5e9) || change == "empty" && (len(got) != 11 || r.Missing != 600 || r.BoundaryCovered) {
				t.Fatalf("planned coverage or actual duration lost: %+v", r)
			}
			if change == "hole" && (r.Missing != 1 || got[1].Missing != 1) || change == "boundary" && (r.BoundaryCovered || got[10].BoundaryCovered || len(r.Delta) != 0) || change == "reset" && (!reflect.DeepEqual(r.ResetOffsetsNS, []int64{30 * int64(time.Second)}) || r.Delta["acquire_count"] != 60000 || r.WaitingPressure != 0) || change == "zero-empty" && (r.MeanEmptyWaitNS != 0 || r.WaitingPressure != 0) {
				t.Fatalf("%s evidence hidden: %+v", change, r)
			}
		})
	}
}

func TestStage2TelemetryFailures(t *testing.T) {
	for _, failure := range []string{"scrape", "slow", "shape", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			start := time.Unix(123, 0)
			clock, calls := start, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			samples, err := stage2SampleMetrics(ctx, start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool { clock = clock.Add(delay); return true }, func(context.Context) (json.RawMessage, error) {
				calls++
				if calls == 1 {
					switch failure {
					case "scrape":
						return nil, fmt.Errorf("scrape failed")
					case "slow":
						clock = clock.Add(2500 * time.Millisecond)
					case "shape":
						return json.RawMessage(`{}`), nil
					case "cancel":
						cancel()
					}
				}
				return stage2MetricFixture(0), nil
			})
			want := 600
			if failure == "cancel" {
				want = 1
			}
			if err == nil || len(samples) != want || calls != want+map[string]int{"scrape": 1, "shape": 1}[failure] || failure == "slow" && samples[1].OffsetNS != int64(2500*time.Millisecond) {
				t.Fatalf("failure lost or backfilled: %s samples=%d calls=%d err=%v", failure, len(samples), calls, err)
			}
		})
	}
}

func TestStage2ReadMetrics(t *testing.T) {
	for _, mode := range []string{"ok", "503", "large", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/metrics" {
					t.Errorf("metrics path: %s", r.URL.Path)
				}
				if mode == "503" {
					w.WriteHeader(503)
				}
				body := string(stage2MetricFixture(0))
				if mode == "large" {
					body = strings.Repeat("x", stage2ProofBodyLimit+1)
				}
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			transport, err := newTransport(server.URL+"/", "", &counts{})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			raw, err := stage2ReadMetrics(ctx, transport)
			if (err == nil) != (mode == "ok") || mode == "ok" && !reflect.DeepEqual(raw, stage2MetricFixture(0)) {
				t.Fatalf("HTTP metrics evidence: %s %v", mode, err)
			}
		})
	}
}
