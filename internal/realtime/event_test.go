package realtime

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestMergeKinds(t *testing.T) {
	first := func([]byte) (kernel.ID, []kernel.ID, error) { return kernel.ID{1}, nil, nil }
	second := func([]byte) (kernel.ID, []kernel.ID, error) { return kernel.ID{2}, nil, nil }
	for _, tt := range []struct {
		name       string
		registries []Kinds
		want       map[EventKind]kernel.ID
		duplicate  EventKind
	}{
		{name: "disjoint", registries: []Kinds{{"first": first}, {"second": second}}, want: map[EventKind]kernel.ID{"first": {1}, "second": {2}}},
		{name: "duplicate", registries: []Kinds{{"first": first}, {"first": second}}, duplicate: "first"},
		{name: "same router", registries: []Kinds{{"first": first}, {"first": first}}, duplicate: "first"},
		{name: "nil router", registries: []Kinds{{"first": nil}, {"first": first}}, duplicate: "first"},
		{name: "no registries"},
		{name: "empty registries", registries: []Kinds{nil, {}, nil}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeKinds(tt.registries...)
			if tt.duplicate != "" {
				if err == nil || !strings.Contains(err.Error(), string(tt.duplicate)) || got != nil {
					t.Fatalf("MergeKinds = %v, %v; want no registry and error naming %q", got, err, tt.duplicate)
				}
				return
			}
			if err != nil || got == nil || len(got) != len(tt.want) {
				t.Fatalf("MergeKinds = %v, %v; want non-nil registry of %d kinds", got, err, len(tt.want))
			}
			for kind, want := range tt.want {
				router, ok := got[kind]
				if !ok || router == nil {
					t.Fatalf("missing router for %q", kind)
				}
				if channel, topics, err := router(nil); channel != want || topics != nil || err != nil {
					t.Fatalf("router for %q = %v, %v, %v; want %v, nil, nil", kind, channel, topics, err, want)
				}
			}
		})
	}
}

func TestMergeKindsIndependentMaps(t *testing.T) {
	for _, count := range []int{1, 2} {
		inputs := []Kinds{{"first": nil}, {"second": nil}}[:count]
		merged, err := MergeKinds(inputs...)
		if err != nil {
			t.Fatal(err)
		}
		if len(inputs[0]) != 1 || (count == 2 && len(inputs[1]) != 1) {
			t.Fatal("merge modified an input")
		}
		for _, input := range inputs {
			merged["result-only"] = nil
			if _, ok := input["result-only"]; ok {
				t.Fatal("result mutation changed an input")
			}
			input["input-only"] = nil
			if _, ok := merged["input-only"]; ok {
				t.Fatal("input mutation changed the result")
			}
			delete(merged, "first")
			if _, ok := inputs[0]["first"]; !ok {
				t.Fatal("result deletion changed an input")
			}
			delete(input, "second")
			if count == 2 {
				if _, ok := merged["second"]; !ok {
					t.Fatal("input deletion changed the result")
				}
			}
		}
	}
}
