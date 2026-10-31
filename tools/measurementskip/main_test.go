package main

import (
	"strings"
	"testing"
)

func TestMeasurementSkipScanFixtures(t *testing.T) {
	entry := measurementSkip{"example/p", "TestCost", "set RIBBITTO_COST=1"}
	for _, tc := range []struct {
		name, source, problem string
		allowed               []measurementSkip
	}{
		{"matching", `func TestCost(t *testing.T) { t.Skip("set " + "RIBBITTO_COST=1") }`, "", []measurementSkip{entry}},
		{"new gate", `func TestNew(t *testing.T) { t.Skip("set RIBBITTO_NEW=1") }`, "new gated skip example/p/TestNew", nil},
		{"rejected formatted Skipf", `func TestCost(t *testing.T) { t.Skipf("set RIBBITTO_COST=1 %s", "now") }`, "gated measurement skips must use a constant message via t.Skip or a verb-free t.Skipf", nil},
		{"verb-free Skipf", `func TestCost(t *testing.T) { t.Skipf("set RIBBITTO_COST=1") }`, "", []measurementSkip{entry}},
		{"renamed", `func TestRenamed(t *testing.T) { t.Skip("set RIBBITTO_COST=1") }`, "stale allowlist entry example/p/TestCost", []measurementSkip{entry}},
		{"stale", `func TestCost(t *testing.T) {}`, "stale allowlist entry example/p/TestCost", []measurementSkip{entry}},
		{"ordinary", `func TestDB(t *testing.T) { t.Skip("no database") }`, "", nil},
		{"browser", `func TestBrowser(t *testing.T) { t.Skip("set RIBBITTO_REQUIRE_BROWSER=1") }`, "", nil},
		{"SkipNow", `func TestNow(t *testing.T) { t.SkipNow(); t.SkipNow("set RIBBITTO_NEW=1") }`, "new gated skip example/p/TestNow", nil},
		{"subtest", `func TestCost(t *testing.T) { t.Run("child", func(t *testing.T) { t.Skip("set RIBBITTO_COST=1") }) }`, "", []measurementSkip{entry}},
		{"fixture string", "var fixture = `t.Skip(\"set RIBBITTO_NEW=1\")`", "", nil},
		{"helper skip not seen", `func helper(t *testing.T, why string) { t.Skip(why) }; func TestCost(t *testing.T) { helper(t, "set RIBBITTO_X=1") }`, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, err := scanMeasurementSkips("example/p", "fixture_test.go", "package p\n"+tc.source)
			if err == nil {
				err = compareMeasurementSkips(found, tc.allowed)
			}
			if tc.problem == "" && err != nil || tc.problem != "" && (err == nil || !strings.Contains(err.Error(), tc.problem)) {
				t.Fatalf("comparison: %v; want %q", err, tc.problem)
			}
		})
	}
}

func TestMeasurementSkipScanRepository(t *testing.T) {
	if err := check("../.."); err != nil {
		t.Fatal(err)
	}
}
