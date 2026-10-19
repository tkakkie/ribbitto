package main

import (
	"slices"
	"strings"
	"testing"
)

func TestPartition(t *testing.T) {
	all := []string{".", "./cmd/loadgen", "./cmd/ribbitto", "./cmd/seed", "./internal/kernel", "./internal/realtime", "./internal/realtime/realtimepg", "./internal/web", "./tools/new"}
	parts, err := partition(all)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"loadgen":  {"./cmd/loadgen", "./cmd/seed"},
		"cmd":      {"./cmd/ribbitto"},
		"web":      {"./internal/web"},
		"realtime": {"./internal/realtime"},
		rest:       {".", "./internal/kernel", "./internal/realtime/realtimepg", "./tools/new"},
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
	_, err := partition([]string{"./cmd/loadgen", "./cmd/ribbitto", "./internal/realtime", "./internal/web"})
	if err == nil || !strings.Contains(err.Error(), "./cmd/seed") {
		t.Fatalf("partition without ./cmd/seed: err = %v", err)
	}
	t.Run("cmd package missing", func(t *testing.T) {
		_, err := partition([]string{"./cmd/loadgen", "./cmd/seed", "./internal/realtime", "./internal/web"})
		if err == nil || !strings.Contains(err.Error(), "shard cmd names ./cmd/ribbitto") {
			t.Fatalf("partition without ./cmd/ribbitto: err = %v", err)
		}
	})
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
	const (
		matrix    = "        shard: [loadgen, cmd, web, realtime]\n"
		restStep  = "          packages=$(go -C tools run ./testshard .. rest)\n          test -n \"$packages\"\n          make check-test GO_TEST_PACKAGES=\"$packages\"\n"
		shardStep = "          packages=$(go -C tools run ./testshard .. \"$SHARD\")\n          test -n \"$packages\"\n          make check-test GO_TEST_PACKAGES=\"$packages\"\n"
		valid     = matrix + restStep + shardStep
	)
	for _, tc := range []struct {
		name, source, want string
	}{
		{"every shard once", valid, ""},
		{"named shard omitted", strings.Replace(valid, "web, ", "", 1), "shard web 0 times"},
		{"named shard repeated", strings.Replace(valid, "web, ", "web, web, ", 1), "shard web 2 times"},
		{"cmd shard omitted", strings.Replace(valid, "cmd, ", "", 1), "shard cmd 0 times"},
		{"cmd shard repeated", strings.Replace(valid, "cmd, ", "cmd, cmd, ", 1), "shard cmd 2 times"},
		{"unknown shard", strings.Replace(valid, "realtime]", "realtime, rest]", 1), `unknown shard "rest"`},
		{"no matrix", restStep + shardStep, "found 0"},
		{"two matrices", matrix + valid, "found 2"},
		{"rest not run", matrix + shardStep, "rest step's lines appear 0 times"},
		{"rest run twice", valid + restStep, "rest step's lines appear 2 times"},
		{"matrix shard not run", matrix + restStep, "matrix step's lines appear 0 times"},
		// Muse Code on #1007: sentinels kept only in comments, or a step
		// that tests other packages or cannot fail, must not pass.
		{"steps only in comments", matrix + "# " + strings.ReplaceAll(strings.TrimSuffix(restStep+shardStep, "\n"), "\n", "\n# ") + "\n", "appear 0 times"},
		{"matrix only in a comment", "# " + valid, "found 0"},
		{"other packages", strings.Replace(valid, `GO_TEST_PACKAGES="$packages"`, `GO_TEST_PACKAGES="./tools/new"`, 1), "rest step's lines appear 0 times"},
		{"cannot fail", strings.Replace(valid, `test -n "$packages"`, `test -n "$packages" || true`, 1), "rest step's lines appear 0 times"},
		{"lines apart", strings.Replace(valid, `test -n "$packages"`, "echo\n          test -n \"$packages\"", 1), "rest step's lines appear 0 times"},
		{"another check-test", valid + "          make check-test GO_TEST_PACKAGES=./x\n", "runs in 3 places"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := scheduled(tc.source)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("scheduled = %v, want %q", err, tc.want)
			}
		})
	}
}
