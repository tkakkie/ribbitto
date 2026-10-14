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

func TestPick(t *testing.T) {
	parts := map[string][]string{"x": {"./a"}, rest: {}}
	if got, err := pick(parts, "x"); err != nil || !slices.Equal(got, []string{"./a"}) {
		t.Fatalf("pick x = %v, %v", got, err)
	}
	for shard, want := range map[string]string{"nope": "unknown shard", rest: "has no packages"} {
		if _, err := pick(parts, shard); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("pick %s: err = %v, want %q", shard, err, want)
		}
	}
}

func TestScheduled(t *testing.T) {
	const restStep = "packages=$(go -C tools run ./testshard .. rest)\n"
	for _, tc := range []struct {
		name, source, want string
	}{
		{"every shard once", "        shard: [loadgen, web]\n" + restStep, ""},
		{"named shard omitted", "        shard: [loadgen]\n" + restStep, "shard web 0 times"},
		{"named shard repeated", "        shard: [loadgen, web, web]\n" + restStep, "shard web 2 times"},
		{"unknown shard", "        shard: [loadgen, web, rest]\n" + restStep, `unknown shard "rest"`},
		{"no matrix", restStep, "found 0"},
		{"two matrices", "shard: [loadgen, web]\nshard: [loadgen, web]\n" + restStep, "found 2"},
		{"rest not run", "        shard: [loadgen, web]\n", "runs in 0 steps"},
		{"rest run twice", "        shard: [loadgen, web]\n" + restStep + restStep, "runs in 2 steps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := scheduled(tc.source)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("scheduled = %v, want %q", err, tc.want)
			}
		})
	}
}
