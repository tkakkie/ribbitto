package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPartition(t *testing.T) {
	all := []string{".", "./cmd/loadgen", "./cmd/ribbitto", "./cmd/seed", "./internal/kernel", "./internal/realtime", "./internal/realtime/realtimepg", "./internal/web", "./tools/new"}
	parts, err := partition(all, shards())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"loadgen":  {"./cmd/loadgen", "./cmd/seed"},
		"cmd":      {"./cmd/ribbitto"},
		"web-1/3":  {"./internal/web"},
		"web-2/3":  {"./internal/web"},
		"web-3/3":  {"./internal/web"},
		"realtime": {"./internal/realtime"},
		rest:       {".", "./internal/kernel", "./internal/realtime/realtimepg", "./tools/new"},
	} {
		if !slices.Equal(parts[name], want) {
			t.Errorf("shard %s = %v, want %v", name, parts[name], want)
		}
	}
	if err := covers(parts, all, shards()); err != nil {
		t.Fatal(err)
	}
}

func TestPartitionRefusesAMissingNamedPackage(t *testing.T) {
	_, err := partition([]string{"./cmd/loadgen", "./cmd/ribbitto", "./internal/realtime", "./internal/web"}, shards())
	if err == nil || !strings.Contains(err.Error(), "./cmd/seed") {
		t.Fatalf("partition without ./cmd/seed: err = %v", err)
	}
	t.Run("cmd package missing", func(t *testing.T) {
		_, err := partition([]string{"./cmd/loadgen", "./cmd/seed", "./internal/realtime", "./internal/web"}, shards())
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
			err := covers(tc.parts, all, nil)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("covers = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestShardsAreDisjoint(t *testing.T) {
	var named []string
	for _, s := range shards() {
		named = append(named, s.packages...)
	}
	parts, err := partition(named, shards())
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
		matrix    = "        shard: [loadgen, cmd, web-1/3, web-2/3, web-3/3, realtime]\n"
		restStep  = "          go -C tools run ./testshard -run .. rest\n"
		shardStep = "          go -C tools run ./testshard -run .. \"$SHARD\"\n"
		valid     = matrix + restStep + shardStep
	)
	for _, tc := range []struct {
		name, source, want string
	}{
		{"every shard once", valid, ""},
		{"named shard omitted", strings.Replace(valid, "web-2/3, ", "", 1), "shard web-2/3 0 times"},
		{"named shard repeated", strings.Replace(valid, "web-2/3, ", "web-2/3, web-2/3, ", 1), "shard web-2/3 2 times"},
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
		{"other packages", strings.Replace(valid, `-run .. rest`, `-run .. ./tools/new`, 1), "rest step's lines appear 0 times"},
		{"cannot fail", strings.Replace(valid, `-run .. rest`, `-run .. rest || true`, 1), "rest step's lines appear 0 times"},
		{"lines apart", strings.Replace(valid, `-run .. rest`, "-run ..\n rest", 1), "rest step's lines appear 0 times"},
		{"another check-test", valid + "          make check-test GO_TEST_PACKAGES=./x\n", "runs in 1 places"},
		{"package selection in another step", valid + "      - run: GO_TEST_PACKAGES=./x go test ./x\n", "found GO_TEST_PACKAGES use outside testshard"},
		{"package selection in env", valid + "      - run: go test ./x\n        env:\n          GO_TEST_PACKAGES: ./x\n", "found GO_TEST_PACKAGES use outside testshard"},
		{"test flags outside runner", valid + "      - run: export GO_TEST_FLAGS=-short\n", "found GO_TEST_FLAGS use outside testshard"},
		{"extra shard invocation", valid + "      - run: go -C tools run ./testshard -run .. web\n", "found 3 testshard invocations, want 2"},
		{"package selection only in a comment", valid + "      # GO_TEST_PACKAGES=./x\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := scheduled(tc.source, shards())
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("scheduled = %v, want %q", err, tc.want)
			}
		})
	}
}

func browserShards() []shard {
	var specs []shard
	for k := 1; k <= 3; k++ {
		selector := fmt.Sprintf("%d/3", k)
		flags := "-p 2 -run ^TestBrowser$"
		if k == 1 {
			flags = "-p 2"
		}
		specs = append(specs, shard{"web-" + selector, []string{"./internal/web"}, selector, flags})
	}
	return specs
}

func TestBrowserShardCoverage(t *testing.T) {
	all := []string{"./internal/web", "./new"}
	specs := browserShards()
	parts, err := partition(all, specs)
	if err != nil {
		t.Fatal(err)
	}
	if err := covers(parts, all, specs); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func([]shard) []shard
	}{
		{"empty packages", func(s []shard) []shard { s[0].packages = nil; return s }},
		{"unsharded flags", func(s []shard) []shard { s[0].browser = ""; s[0].flags = "-short"; return s[:1] }},
		{"dropped", func(s []shard) []shard { return s[:2] }},
		{"duplicated", func(s []shard) []shard { return append(s, s[0]) }},
		{"mismatched total", func(s []shard) []shard { s[2].browser = "3/4"; return s }},
		{"duplicate selector", func(s []shard) []shard { s[2].browser = "2/3"; return s }},
		{"bad selector", func(s []shard) []shard { s[2].browser = "invalid"; return s }},
		{"out of range", func(s []shard) []shard { s[2].browser = "4/3"; return s }},
		{"unsharded overlap", func(s []shard) []shard { s[2].browser = ""; return s }},
		{"repeated non-browser tests", func(s []shard) []shard { s[2].flags = "-p 2"; return s }},
		{"missing non-browser tests", func(s []shard) []shard { s[0].flags = s[1].flags; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := partition(all, tc.edit(browserShards())); err == nil {
				t.Fatal("accepted invalid shard list")
			}
		})
	}
	delete(parts, specs[1].name)
	if err := covers(parts, all, specs); err == nil {
		t.Fatal("accepted missing package slice")
	}
}

func TestScheduledBrowserShards(t *testing.T) {
	valid := "shard: [web-1/3, web-2/3, web-3/3]\n" + steps[rest][0] + "\n" + steps["matrix"][0] + "\n"
	if err := scheduled(valid, browserShards()); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		strings.Replace(valid, ", web-3/3", "", 1),
		strings.Replace(valid, "web-3/3", "web-2/3", 1),
		strings.Replace(valid, "web-3/3", "web-3/4", 1),
		strings.Replace(valid, `"$SHARD"`, `"$SHARD" GO_TEST_FLAGS=-short`, 1),
		valid + "make check-test GO_TEST_PACKAGES=./internal/web\n",
		valid + "go -C tools run ./testshard -run .. web-1/3\n",
		valid + "export GO_TEST_FLAGS=-short\n",
	} {
		if err := scheduled(source, browserShards()); err == nil {
			t.Errorf("accepted mutated workflow: %s", source)
		}
	}
}

func TestCommand(t *testing.T) {
	root := t.TempDir()
	// Exercise make's dollar expansion as well as the command arguments:
	// browser-only flags contain an end-of-name regex anchor.
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("check-test:\n\t@printf '%s\\n' $(GO_TEST_FLAGS) $(GO_TEST_PACKAGES) \"$$RIBBITTO_BROWSER_SHARD\" \"$$GOFLAGS\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIBBITTO_BROWSER_SHARD", "wrong")
	t.Setenv("GOFLAGS", "-run=X")
	for _, s := range append(shards(), browserShards()...) {
		cmd := testCommand("/repository", s)
		want := []string{"make", "check-test", "GO_TEST_PACKAGES=" + strings.Join(s.packages, " "), "GO_TEST_FLAGS=" + strings.ReplaceAll(s.flags, "$", "$$")}
		if cmd.Dir != "/repository" || !slices.Equal(cmd.Args, want) {
			t.Fatalf("command = %v in %s, want %v", cmd.Args, cmd.Dir, want)
		}
		if got := cmd.Environ(); got[len(got)-2] != "RIBBITTO_BROWSER_SHARD="+s.browser {
			t.Fatalf("selector environment = %v", got)
		}
		if got := cmd.Environ(); got[len(got)-1] != "GOFLAGS=-count=1" {
			t.Fatalf("Go flags environment = %v", got)
		}
		cmd.Dir = root
		// make check-tools runs this test under make, whose MAKELEVEL and
		// MAKEFLAGS would make this make print "Entering directory" lines.
		cmd.Env = append(cmd.Env, "MAKELEVEL=0", "MAKEFLAGS=")
		out, err := cmd.Output()
		wantOutput := strings.Join(append(append(strings.Fields(s.flags), s.packages...), s.browser, "-count=1"), "\n") + "\n"
		if err != nil || string(out) != wantOutput {
			t.Fatalf("make output = %q, %v; want %q", out, err, wantOutput)
		}
	}
}

func TestRunPropagatesFailure(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "make"), []byte("#!/bin/sh\n[ -z \"$REPORT_COUNT\" ] || printf 'browser shard 2/3: 40 of 120 scenarios\\n' > \"$RIBBITTO_BROWSER_COUNT_FILE\"\nexit \"$MAKE_EXIT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct{ shard, status, report, wantErr string }{
		{"web-2/3", "0", "yes", ""},
		{"web-2/3", "7", "yes", "running shard web-2/3: exit status 7"},
		{"web-2/3", "7", "", "running shard web-2/3: exit status 7"},
		{"web-2/3", "0", "", "shard web-2/3 succeeded without writing a browser scenario count"},
		{"cmd", "0", "", ""},
	} {
		t.Run(tc.shard+"/"+tc.status+tc.report, func(t *testing.T) {
			t.Setenv("MAKE_EXIT", tc.status)
			t.Setenv("REPORT_COUNT", tc.report)
			out, err := os.CreateTemp(t.TempDir(), "stdout")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			stdout := os.Stdout
			os.Stdout = out
			err = run("../..", tc.shard, false, true)
			os.Stdout = stdout
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("run = %v, want %q", err, tc.wantErr)
			}
			line, err := os.ReadFile(out.Name())
			want := ""
			if tc.report != "" {
				want = "browser shard 2/3: 40 of 120 scenarios\n"
			}
			if err != nil || string(line) != want {
				t.Fatalf("count = %q, %v, want %q", line, err, want)
			}
		})
	}
}

func TestCheckBrowserTests(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal/web")
	if err := os.MkdirAll(filepath.Join(dir, "testdata"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, file, source, totals string }{
		{"valid", "browser_test.go", "func TestBrowser(t *testing.T) {}", "3"},
		{"renamed", "browser_test.go", "func TestRenamed(t *testing.T) {}", "3"},
		{"not a test file", "browser.go", "func TestBrowser(t *testing.T) {}", "3"},
		{"method", "browser_test.go", "func (s suite) TestBrowser(t *testing.T) {}", "3"},
		{"wrong signature", "browser_test.go", "func TestBrowser(t testing.T) {}", "3"},
		{"untested total", "browser_test.go", "func TestBrowser(t *testing.T) {}", "4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte("package web\nimport \"testing\"\n"+tc.source), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })
			if err := os.WriteFile(filepath.Join(dir, "testdata/browser-shard-totals.txt"), []byte(tc.totals), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := checkBrowserTests(root, browserShards()); (err == nil) != (tc.name == "valid") {
				t.Fatalf("checkBrowserTests = %v", err)
			}
		})
	}
}
