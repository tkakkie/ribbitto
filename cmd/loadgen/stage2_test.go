package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestStage2Protocol(t *testing.T) {
	p := fixedStage2()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	// Pin the entire pre-registration, including applicability, scopes and nonnumeric gates.
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "d2f2e0845d9fe03875f7474de8d0b16ed30a785afb567a929b5e510ca7f9de34" {
		t.Fatalf("protocol changed: %s", got)
	}
	p.Gates[0].Limits[0] = 999
	p.Gates[0].Arms[0] = "changed"
	p.Requirements[0] = "changed"
	next := fixedStage2()
	if next.Gates[0].Limits[0] != 50 || next.Gates[0].Arms[0] != "async" || next.Requirements[0] == "changed" {
		t.Fatal("protocol shares mutable state")
	}
}

func stage2WindowFixture() stage2Window {
	p := fixedStage2()
	c := stage2Config{p.Groups[0], p.Arms[1], 1, 42, 43}
	metrics := json.RawMessage(`{"database":{},"pool":{},"runtime":{},"streams":{},"scheduler":{},"delivery":{}}`)
	e := stage2Effective{p.Arms[1], 1000, metrics}
	gates := make([]stage2Verdict, 0, len(p.Gates))
	for _, gate := range p.Gates {
		gates = append(gates, stage2Verdict{gate.ID, "inconclusive", map[string]float64{"count": 0}})
	}
	return stage2Window{Version: 1, Kind: "window", Protocol: p, Config: c, Effective: e,
		Samples: []stage2Sample{{0, "traffic", metrics}}, Operations: []stage2Operation{{Operation: "post", Endpoint: "feed", HTTP: stage2Latency{1, 1, 2, 3}}},
		Delivery: stage2Delivery{Message: stage2ReceiptLatency{100, 1, -5, 1, 2, 3}, PostFreshness: stage2ReceiptLatency{1, 1, -2, -2, -2, -2}, ReadFreshness: stage2ReceiptLatency{1, 1, -3, -3, -3, -3}},
		Attempts: []stage2Attempt{{Operation: "replacement", LogicalID: 1, Attempt: 2, Status: 503, Outcome: "refused_capacity"}}, Gates: gates}
}

func stage2ManifestFixture() stage2Manifest {
	w := stage2WindowFixture()
	p, e, gates := w.Protocol, w.Effective, w.Gates
	m := stage2Manifest{Version: 1, Kind: "manifest", Protocol: p, Gates: gates}
	for _, group := range p.Groups {
		for _, arm := range p.Arms {
			for round := 1; round <= p.Rounds; round++ {
				m.Windows = append(m.Windows, stage2WindowRef{"completed", "", "window.json", strings.Repeat("a", 64), stage2Config{group, arm, round, 42, 43}, &stage2Effective{arm, group.Streams, e.Metrics}})
			}
		}
	}
	return m
}

func TestStage2Schema(t *testing.T) {
	w := stage2WindowFixture()
	p, e, gates := w.Protocol, w.Effective, w.Gates
	m := stage2ManifestFixture()
	for i, record := range []any{w, m} {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		// Fixed examples prevent changing the version-1 wire shape unnoticed.
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != [2]string{"bc824fc7f3ea4df8f8c66f5c92c329af85d5efc8ae85918e896bd64ed05a2e9e", "8cba0bdbf1362c7078cef41c9606389ff0170175d542f2ced3e0f4e628ee9fd1"}[i] {
			t.Errorf("version-1 schema changed: %s", got)
		}
		dst := reflect.New(reflect.TypeOf(record)).Interface()
		if err := decodeStage2(raw, dst); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record, reflect.ValueOf(dst).Elem().Interface()) {
			t.Fatal("round trip changed result")
		}
		for _, tc := range []struct {
			name   string
			change func([]stage2Verdict) []stage2Verdict
			valid  bool
		}{
			{"empty", func(g []stage2Verdict) []stage2Verdict { return g[:0] }, false},
			{"missing", func(g []stage2Verdict) []stage2Verdict { return g[1:] }, false},
			{"duplicate", func(g []stage2Verdict) []stage2Verdict { return append(g, g[0]) }, false},
			{"duplicate replacement", func(g []stage2Verdict) []stage2Verdict { g[1] = g[0]; return g }, false},
			{"unknown", func(g []stage2Verdict) []stage2Verdict {
				return append(g, stage2Verdict{"invented", "inconclusive", map[string]float64{}})
			}, false},
			{"unknown replacement", func(g []stage2Verdict) []stage2Verdict { g[0].ID = "invented"; return g }, false},
			{"blank ID", func(g []stage2Verdict) []stage2Verdict { g[0].ID = ""; return g }, false},
			{"reordered states", func(g []stage2Verdict) []stage2Verdict {
				g[0], g[1] = g[1], g[0]
				g[0].State, g[1].State = "pass", "reject"
				return g
			}, true},
		} {
			t.Run([]string{"window", "manifest"}[i]+"/gates/"+tc.name, func(t *testing.T) {
				copyGates := tc.change(append([]stage2Verdict(nil), gates...))
				var copyRecord any
				if i == 0 {
					copy := w
					copy.Gates = copyGates
					copyRecord = copy
				} else {
					copy := m
					copy.Gates = copyGates
					copyRecord = copy
				}
				raw, err := json.Marshal(copyRecord)
				if err != nil {
					t.Fatal(err)
				}
				dst := reflect.New(reflect.TypeOf(copyRecord)).Interface()
				if err := decodeStage2(raw, dst); (err == nil) != tc.valid {
					t.Fatalf("gate verdicts valid=%v, decode error: %v", tc.valid, err)
				}
				if tc.valid && !reflect.DeepEqual(copyRecord, reflect.ValueOf(dst).Elem().Interface()) {
					t.Fatal("round trip changed gate verdicts")
				}
			})
		}
		for _, pair := range [][2]string{{`"Version":1`, `"Version":2`}, {`"Round":1`, `"Round":0`}, {`"Streams":1000`, `"Streams":999`}, {`"P95MS":2`, `"P95MS":"bad"`}, {`"Version":1`, `"Version":1,"Version":1`}, {`"State":"inconclusive"`, `"State":"unknown"`},
			{`"Arms":["async"]`, `"Arms":["sync","async"]`},
			{`"Early":1`, `"Early":101`}, {`"Early":1`, `"Early":0`}, {`"MinMS":-5`, `"MinMS":0`}, {`"MinMS":0`, `"MinMS":-1`}, {`"MinMS":0`, `"MinMS":1`},
			{`"MinMS":-2`, `"MinMS":0`}, {`"MinMS":-3`, `"MinMS":0`},
			{`"Count":100,"Early":1,"MinMS":-5`, `"Count":0,"Early":0,"MinMS":1`}, {`"Count":100,"Early":1,"MinMS":-5`, `"Count":100,"Early":0,"MinMS":2`},
			{`"File":"window.json"`, `"File":""`}, {`"SHA256":"` + strings.Repeat("a", 64) + `"`, `"SHA256":"aa"`},
			{`"SHA256":"` + strings.Repeat("a", 64) + `"`, `"SHA256":"` + strings.Repeat("a", 64) + `zz"`},
			{`"State":"completed"`, `"State":"unknown"`}, {`"State":"completed"`, `"State":"failed"`}, {`"State":"completed"`, `"State":"unrun"`}, {`"Reason":""`, `"Reason":"unexpected"`}} {
			bad := strings.Replace(string(raw), pair[0], pair[1], 1)
			if bad != string(raw) && decodeStage2([]byte(bad), dst) == nil {
				t.Fatalf("accepted invalid %s", pair[1])
			}
		}
		var tree any
		if err := json.Unmarshal(raw, &tree); err != nil {
			t.Fatal(err)
		}
		stage2MissingFields(t, tree, tree, dst)
	}
	t.Run("sync inconclusive gates", func(t *testing.T) {
		copy := w
		copy.Config.Arm, copy.Effective.Startup = p.Arms[0], p.Arms[0]
		raw, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		var decoded stage2Window
		if err := decodeStage2(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(copy, decoded) {
			t.Fatal("round trip changed sync inconclusive verdicts")
		}
	})
	for _, tc := range []struct {
		name   string
		change func(*stage2Manifest)
		valid  bool
	}{
		{"failed without evidence", func(m *stage2Manifest) {
			m.Windows[0] = stage2WindowRef{State: "failed", Reason: "startup failed", Config: m.Windows[0].Config}
		}, true},
		{"failed with evidence", func(m *stage2Manifest) { m.Windows[0].State, m.Windows[0].Reason = "failed", "interrupted" }, true},
		{"unrun", func(m *stage2Manifest) {
			m.Windows[0] = stage2WindowRef{State: "unrun", Reason: "previous failure", Config: m.Windows[0].Config}
		}, true},
		{"missing window", func(m *stage2Manifest) { m.Windows = m.Windows[1:] }, false},
		{"extra window", func(m *stage2Manifest) { m.Windows = append(m.Windows, m.Windows[0]) }, false},
		{"duplicate different seeds", func(m *stage2Manifest) { m.Windows[1] = m.Windows[0]; m.Windows[1].Config.TrafficSeed++ }, false},
		{"completed no effective", func(m *stage2Manifest) { m.Windows[0].Effective = nil }, false},
		{"failed blank reason", func(m *stage2Manifest) { m.Windows[0].State, m.Windows[0].Reason = "failed", " " }, false},
		{"failed partial artifact", func(m *stage2Manifest) {
			m.Windows[0].State, m.Windows[0].Reason, m.Windows[0].File = "failed", "interrupted", ""
		}, false},
		{"unrun blank reason", func(m *stage2Manifest) { m.Windows[0] = stage2WindowRef{State: "unrun", Config: m.Windows[0].Config} }, false},
		{"unrun effective", func(m *stage2Manifest) {
			m.Windows[0] = stage2WindowRef{State: "unrun", Reason: "stopped", Config: m.Windows[0].Config, Effective: &e}
		}, false},
		{"unrun file", func(m *stage2Manifest) {
			m.Windows[0] = stage2WindowRef{State: "unrun", Reason: "stopped", Config: m.Windows[0].Config, File: "window.json"}
		}, false},
		{"unrun hash", func(m *stage2Manifest) {
			m.Windows[0] = stage2WindowRef{State: "unrun", Reason: "stopped", Config: m.Windows[0].Config, SHA256: strings.Repeat("a", 64)}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := m
			copy.Windows = append([]stage2WindowRef(nil), m.Windows...)
			tc.change(&copy)
			raw, err := json.Marshal(copy)
			if err != nil {
				t.Fatal(err)
			}
			var decoded stage2Manifest
			err = decodeStage2(raw, &decoded)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, decode error: %v", tc.valid, err)
			}
			if tc.valid && !reflect.DeepEqual(copy, decoded) {
				t.Fatal("round trip changed manifest state/evidence")
			}
		})
	}
}

func TestStage2GateApplicability(t *testing.T) {
	p := fixedStage2()
	for i, gate := range p.Gates {
		for _, arm := range p.Arms {
			for _, state := range []string{"pass", "reject", "inconclusive"} {
				t.Run(arm.Name+"/"+gate.ID+"/"+state, func(t *testing.T) {
					w := stage2WindowFixture()
					w.Config.Arm, w.Effective.Startup = arm, arm
					w.Gates[i].State = state
					raw, err := json.Marshal(w)
					if err != nil {
						t.Fatal(err)
					}
					var decoded stage2Window
					err = decodeStage2(raw, &decoded)
					if !slices.Contains(gate.Arms, arm.Name) && state != "inconclusive" {
						want := fmt.Sprintf("gate %q does not apply to %s window: must be inconclusive", gate.ID, arm.Name)
						if err == nil || err.Error() != want {
							t.Fatalf("expected non-applicable gate rejection %q, got: %v", want, err)
						}
					} else if err != nil {
						t.Fatalf("applicable or inconclusive gate rejected: %v", err)
					}
				})
			}
		}
		for _, state := range []string{"pass", "reject", "inconclusive"} {
			t.Run("manifest/"+gate.ID+"/"+state, func(t *testing.T) {
				m := stage2ManifestFixture()
				m.Gates[i].State = state
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				var decoded stage2Manifest
				if err := decodeStage2(raw, &decoded); err != nil {
					t.Fatalf("manifest aggregate gate rejected: %v", err)
				}
			})
		}
	}
}

func TestStage2Latencies(t *testing.T) {
	type latencyCase struct {
		name    string
		summary any
		wantErr string
	}
	durations := []latencyCase{
		{"equal", stage2Latency{1, 2, 2, 2}, ""},
		{"observed zeros", stage2Latency{1, 0, 0, 0}, ""},
		{"empty", stage2Latency{}, ""},
		{"p50 above p95", stage2Latency{1, 2, 1, 3}, "invalid latency percentile order"},
		{"p95 above p99", stage2Latency{1, 1, 3, 2}, "invalid latency percentile order"},
		{"empty p50", stage2Latency{0, 1, 0, 0}, "empty latency must be zero"},
		{"empty p95", stage2Latency{0, 0, 1, 0}, "empty latency must be zero"},
		{"empty p99", stage2Latency{0, 0, 0, 1}, "empty latency must be zero"},
		{"empty percentiles", stage2Latency{0, 1, 1, 1}, "empty latency must be zero"},
		{"negative p50", stage2Latency{1, -1, 0, 0}, "negative latency"},
		{"all negative", stage2Latency{1, -3, -2, -1}, "negative latency"},
	}
	receipts := []latencyCase{
		{"equal", stage2ReceiptLatency{1, 0, 2, 2, 2, 2}, ""},
		{"observed zeros", stage2ReceiptLatency{1, 0, 0, 0, 0, 0}, ""},
		{"empty", stage2ReceiptLatency{}, ""},
		{"negative equal", stage2ReceiptLatency{1, 1, -2, -2, -2, -2}, ""},
		{"mixed signs", stage2ReceiptLatency{2, 1, -2, -1, 0, 1}, ""},
		{"early below median", stage2ReceiptLatency{100, 1, -2, 0, 1, 2}, ""},
		{"p50 above p95", stage2ReceiptLatency{1, 0, 0, 2, 1, 3}, "invalid receipt percentile order"},
		{"p95 above p99", stage2ReceiptLatency{1, 0, 0, 1, 3, 2}, "invalid receipt percentile order"},
		{"negative p50 above p95", stage2ReceiptLatency{1, 1, -3, -1, -2, 0}, "invalid receipt percentile order"},
		{"negative p95 above p99", stage2ReceiptLatency{1, 1, -3, -3, -1, -2}, "invalid receipt percentile order"},
		{"empty min", stage2ReceiptLatency{0, 0, 1, 0, 0, 0}, "empty receipt latency must be zero"},
		{"empty early", stage2ReceiptLatency{0, 1, 0, 0, 0, 0}, "empty receipt latency must be zero"},
		{"empty p50", stage2ReceiptLatency{0, 0, 0, 1, 0, 0}, "empty receipt latency must be zero"},
		{"empty p95", stage2ReceiptLatency{0, 0, 0, 0, 1, 0}, "empty receipt latency must be zero"},
		{"empty p99", stage2ReceiptLatency{0, 0, 0, 0, 0, 1}, "empty receipt latency must be zero"},
		{"empty percentiles", stage2ReceiptLatency{0, 0, 0, 1, 1, 1}, "empty receipt latency must be zero"},
		{"early exceeds count", stage2ReceiptLatency{1, 2, -1, 0, 0, 0}, "invalid signed receipt evidence"},
		{"negative min without early", stage2ReceiptLatency{1, 0, -1, 0, 0, 0}, "invalid signed receipt evidence"},
		{"zero min with early", stage2ReceiptLatency{1, 1, 0, 0, 0, 0}, "invalid signed receipt evidence"},
		{"positive min with early", stage2ReceiptLatency{1, 1, 1, 1, 1, 1}, "invalid signed receipt evidence"},
		{"min above p50", stage2ReceiptLatency{1, 0, 2, 1, 2, 3}, "invalid signed receipt evidence"},
	}
	// Discover every summary field: adding one to either record automatically
	// exercises it through decodeStage2, rather than maintaining a field list.
	w := stage2WindowFixture()
	for _, record := range []struct {
		name  string
		value reflect.Value
	}{
		{"Operation", reflect.ValueOf(&w.Operations[0]).Elem()},
		{"Delivery", reflect.ValueOf(&w.Delivery).Elem()},
	} {
		for i := 0; i < record.value.NumField(); i++ {
			field := record.value.Field(i)
			var cases []latencyCase
			switch field.Type() {
			case reflect.TypeOf(stage2Latency{}):
				cases = durations
			case reflect.TypeOf(stage2ReceiptLatency{}):
				cases = receipts
			default:
				continue
			}
			original := reflect.ValueOf(field.Interface())
			t.Run(record.name+"/"+record.value.Type().Field(i).Name, func(t *testing.T) {
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						field.Set(reflect.ValueOf(tc.summary))
						defer field.Set(original)
						raw, err := json.Marshal(w)
						if err != nil {
							t.Fatal(err)
						}
						var decoded stage2Window
						err = decodeStage2(raw, &decoded)
						if tc.wantErr == "" {
							if err != nil {
								t.Fatalf("rejected valid latency: %v", err)
							}
							if !reflect.DeepEqual(w, decoded) {
								t.Fatal("round trip changed latency evidence")
							}
						} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
							t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
						}
					})
				}
			})
		}
	}
}

// Remove and null every record field, including records inside arrays. Dynamic
// metric-section contents are server-owned; their six section fields are ours.
func stage2MissingFields(t *testing.T, node, root, dst any) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			for _, null := range []bool{false, true} {
				delete(v, key)
				if null {
					v[key] = nil
				}
				raw, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				if decodeStage2(raw, dst) == nil {
					t.Fatalf("accepted missing/null %s", key)
				}
				v[key] = value
			}
			if key != "Measured" {
				stage2MissingFields(t, value, root, dst)
			}
		}
	case []any:
		for _, value := range v {
			stage2MissingFields(t, value, root, dst)
		}
	}
}
