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
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "662ecf275f2bd7b24bca070457dc49cf45d84b8479e47dc83a6ae1e940a77a6a" {
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
	gates := []stage2Verdict{{"message-delivery", "inconclusive", map[string]float64{"count": 0}}}
	w := stage2Window{Version: 1, Kind: "window", Protocol: p, Config: c, Effective: e,
		Samples: []stage2Sample{{0, "traffic", metrics}}, Operations: []stage2Operation{{Operation: "post", Endpoint: "feed", HTTP: stage2Latency{1, 1, 2, 3}}},
		Delivery: stage2Delivery{Message: stage2Latency{1, -1, 2, 3}},
		Attempts: []stage2Attempt{{Operation: "replacement", LogicalID: 1, Attempt: 2, Status: 503, Outcome: "refused_capacity"}}, Gates: gates}
	m := stage2Manifest{Version: 1, Kind: "manifest", Protocol: p, Windows: []stage2WindowRef{{"window.json", strings.Repeat("a", 64), c, e}}, Gates: gates}
	for i, record := range []any{w, m} {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		// Fixed examples prevent changing the version-1 wire shape unnoticed.
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != [2]string{"62dbde37e816d74f5d5e1d3f7e8fa650e36b8699e31fee4e3f8510ca9fd0da00", "4291b63345b2d2b0cd4b9d22c365345f0b3677ddc5e3d8c11c74397ae8b83567"}[i] {
			t.Errorf("version-1 schema changed: %s", got)
		}
		dst := reflect.New(reflect.TypeOf(record)).Interface()
		if err := decodeStage2(raw, dst); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record, reflect.ValueOf(dst).Elem().Interface()) {
			t.Fatal("round trip changed result")
		}
		for _, pair := range [][2]string{{`"Version":1`, `"Version":2`}, {`"Round":1`, `"Round":0`}, {`"Streams":1000`, `"Streams":999`}, {`"P95MS":2`, `"P95MS":"bad"`}, {`"Version":1`, `"Version":1,"Version":1`}, {`"State":"inconclusive"`, `"State":"unknown"`}} {
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
