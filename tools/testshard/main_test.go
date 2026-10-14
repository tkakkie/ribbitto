package main

import (
	"slices"
	"strings"
	"testing"
)

func TestPartition(t *testing.T) {
	all := []string{".", "./cmd/loadgen", "./cmd/ribbitto", "./cmd/seed", "./internal/kernel", "./internal/web", "./tools/new"}
	parts, err := partition(all)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"loadgen": {"./cmd/loadgen", "./cmd/seed"},
		"web":     {"./cmd/ribbitto", "./internal/web"},
		rest:      {".", "./internal/kernel", "./tools/new"},
	} {
		if !slices.Equal(parts[name], want) {
			t.Errorf("shard %s = %v, want %v", name, parts[name], want)
		}
	}
	if err := covers(parts, all); err != nil {
		t.Fatal(err)
	}
}

func TestPartitionRefusesAMissingNamedPackage(t *testing.T) {
	_, err := partition([]string{"./cmd/loadgen", "./cmd/ribbitto", "./internal/web"})
	if err == nil || !strings.Contains(err.Error(), "./cmd/seed") {
		t.Fatalf("partition without ./cmd/seed: err = %v", err)
	}
}

func TestCovers(t *testing.T) {
	all := []string{"./a", "./b"}
	for _, tc := range []struct {
		name  string
		parts map[string][]string
		want  string
	}{
		{"exactly once", map[string][]string{"x": {"./a"}, rest: {"./b"}}, ""},
		{"overlap", map[string][]string{"x": {"./a"}, rest: {"./a", "./b"}}, "./a is in shards"},
		{"missing", map[string][]string{"x": {"./a"}, rest: {}}, "./b is in no shard"},
		{"unknown package", map[string][]string{"x": {"./a", "./c"}, rest: {"./b"}}, "shards list 3 packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := covers(tc.parts, all)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("covers = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestShardsAreDisjoint(t *testing.T) {
	var named []string
	for _, s := range shards {
		named = append(named, s.packages...)
	}
	parts, err := partition(named)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts[rest]) != 0 {
		t.Fatalf("rest = %v, want none", parts[rest])
	}
}
