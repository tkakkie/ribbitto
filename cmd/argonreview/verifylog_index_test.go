package main

import (
	"reflect"
	"testing"
)

func TestVerifyLogIndexSummaryOrders(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "incomplete"}[complete], func(t *testing.T) {
			r := &containerRecord{sealed: true, complete: complete, entries: []*aggregatorEntry{
				{kind: "package", pkg: "z", state: "fail"},
				{kind: "test", pkg: "q", name: "A", state: "fail"},
				{kind: "test", pkg: "p", name: "Z", state: "fail"},
				{kind: "test", pkg: "p", name: "A", state: "skip"},
				{kind: "package", pkg: "a", state: "fail"},
				{kind: "package", pkg: "skip", state: "skip"},
			}, skipped: []string{"p/A", "skip (package)"}}
			c := newVerifyLogCapture("FAILED", r)
			var got []string
			for _, item := range c.entries {
				got = append(got, item.display)
			}
			want := []string{"p/A", "p/Z", "q/A", "z: package failed", "a: package failed"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("index order = %v, want %v", got, want)
			}
			facts := recordSummaryFacts(r)
			if facts.failures[0].name != "p/Z" || facts.failures[1].name != "q/A" || !reflect.DeepEqual(facts.observed, []string{"z/ (package)", "q/A (test)", "p/Z (test)", "a/ (package)"}) || !reflect.DeepEqual(facts.skipped, r.skipped) {
				t.Fatal("fixture must distinguish summary orders from index ordinals")
			}
		})
	}
}
