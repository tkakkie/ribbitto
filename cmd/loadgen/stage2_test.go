package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestStage2Protocol(t *testing.T) {
	p := fixedStage2()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	// Pin the entire pre-registration, including scopes and nonnumeric gates.
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "1911275ad35eae2ccf365dfc022ea3829363254311db8eb0413021f515201215" {
		t.Fatalf("protocol changed: %s", got)
	}
	p.Gates[0].Limits[0] = 999
	p.Requirements[0] = "changed"
	next := fixedStage2()
	if next.Gates[0].Limits[0] != 50 || next.Requirements[0] == "changed" {
		t.Fatal("protocol shares mutable state")
	}
}

func TestStage2Schema(t *testing.T) {
	p := fixedStage2()
	c := stage2Config{p.Groups[0], p.Arms[1], 1, 42, 43}
	metrics := json.RawMessage(`{"database":{},"pool":{},"runtime":{},"streams":{},"scheduler":{},"delivery":{}}`)
	e := stage2Effective{p.Arms[1], 1000, metrics}
	gates := make([]stage2Verdict, 0, len(p.Gates))
	for _, gate := range p.Gates {
		gates = append(gates, stage2Verdict{gate.ID, "inconclusive", map[string]float64{"count": 0}})
	}
	w := stage2Window{Version: 1, Kind: "window", Protocol: p, Config: c, Effective: e,
		Samples: []stage2Sample{{0, "traffic", metrics}}, Operations: []stage2Operation{{Operation: "post", Endpoint: "feed", HTTP: stage2Latency{1, 1, 2, 3}}},
		Delivery: stage2Delivery{Message: stage2ReceiptLatency{100, 1, -5, 1, 2, 3}, PostFreshness: stage2ReceiptLatency{1, 1, -2, -2, -2, -2}, ReadFreshness: stage2ReceiptLatency{1, 1, -3, -3, -3, -3}},
		Attempts: []stage2Attempt{{Operation: "replacement", LogicalID: 1, Attempt: 2, Status: 503, Outcome: "refused_capacity"}}, Gates: gates}
	m := stage2Manifest{Version: 1, Kind: "manifest", Protocol: p, Gates: gates}
	for _, group := range p.Groups {
		for _, arm := range p.Arms {
			for round := 1; round <= p.Rounds; round++ {
				m.Windows = append(m.Windows, stage2WindowRef{"completed", "", "window.json", strings.Repeat("a", 64), stage2Config{group, arm, round, 42, 43}, &stage2Effective{arm, group.Streams, metrics}})
			}
		}
	}
	for i, record := range []any{w, m} {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		// Fixed examples prevent changing the version-1 wire shape unnoticed.
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != [2]string{"d04667221273b7641b8a15435b22fbf93d6eeab683580c8d0d77cb3785c8dcf8", "87eff8c0f68647db6803ca4169e9af26acff718f28b6213d13482d4f3d8f535a"}[i] {
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
