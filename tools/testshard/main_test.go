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
	all := []string{".", "./cmd/argonreview", "./cmd/loadgen", "./cmd/ribbitto", "./cmd/seed", "./internal/kernel", "./internal/realtime", "./internal/realtime/realtimepg", "./internal/web", "./tools/new"}
	parts, err := partition(all, shards())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"loadgen":        {"./cmd/loadgen", "./cmd/seed"},
		"cmd":            {"./cmd/ribbitto"},
		"argonreview":    {"./cmd/argonreview"},
		"web-nonbrowser": {"./internal/web"},
		"web-1/3":        {"./internal/web"},
		"web-2/3":        {"./internal/web"},
		"web-3/3":        {"./internal/web"},
		"realtime":       {"./internal/realtime"},
		rest:             {".", "./internal/kernel", "./internal/realtime/realtimepg", "./tools/new"},
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
		matrix    = "        shard: [loadgen, cmd, argonreview, web-nonbrowser, web-1/3, web-2/3, web-3/3, realtime]\n"
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
		{"non-browser shard omitted", strings.Replace(valid, "web-nonbrowser, ", "", 1), "shard web-nonbrowser 0 times"},
		{"non-browser shard repeated", strings.Replace(valid, "web-nonbrowser, ", "web-nonbrowser, web-nonbrowser, ", 1), "shard web-nonbrowser 2 times"},
		{"cmd shard omitted", strings.Replace(valid, "cmd, ", "", 1), "shard cmd 0 times"},
		{"cmd shard repeated", strings.Replace(valid, "cmd, ", "cmd, cmd, ", 1), "shard cmd 2 times"},
		{"argonreview shard omitted", strings.Replace(valid, "argonreview, ", "", 1), "shard argonreview 0 times"},
		{"argonreview shard repeated", strings.Replace(valid, "argonreview, ", "argonreview, argonreview, ", 1), "shard argonreview 2 times"},
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

func TestArgonreviewShard(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]shard) []shard
		want string
	}{
		{"valid", func(s []shard) []shard { return s }, ""},
		{"timeout missing", func(s []shard) []shard { s[2].flags = "-p 2"; return s }, "argonreview must run only"},
		{"timeout altered", func(s []shard) []shard { s[2].flags = "-p 2 -timeout 30m"; return s }, "argonreview must run only"},
		{"other shard timeout", func(s []shard) []shard { s[0].flags += " -timeout 20m"; return s }, "shard loadgen: flags"},
		{"other package", func(s []shard) []shard { s[2].packages = []string{"./other"}; return s }, "argonreview must run only"},
		{"browser selector", func(s []shard) []shard { s[2].browser = "1/3"; return s }, "argonreview must run only"},
		{"package in two shards", func(s []shard) []shard { return append(s, shard{"extra", []string{"./cmd/argonreview"}, "", "-p 2"}) }, "./cmd/argonreview: whole-package range overlaps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateShards(tc.edit(shards()))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validateShards = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("package in no shard", func(t *testing.T) {
		specs := []shard{{"argonreview", []string{"./cmd/argonreview"}, "", argonreviewFlags}}
		err := covers(map[string][]string{"argonreview": {"./cmd/argonreview"}}, []string{"./cmd/argonreview", "./new"}, specs)
		if err == nil || !strings.Contains(err.Error(), "./new is in no shard") {
			t.Fatalf("covers = %v, want ./new in no shard", err)
		}
	})
}

func TestRepositoryWiring(t *testing.T) {
	for _, path := range []string{workflow, "Makefile"} {
		t.Run(path, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("../..", path))
			if err != nil {
				t.Fatal(err)
			}
			if path == workflow {
				err = scheduled(string(source), shards())
			} else {
				err = checkMakefile(string(source))
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMakefile(t *testing.T) {
	source, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	valid := string(source)
	t.Run("default packages bypass full run", func(t *testing.T) {
		err := checkMakefile(valid + "\nGO_TEST_PACKAGES ?= ./...\n")
		if err == nil || !strings.Contains(err.Error(), "found 3 GO_TEST_PACKAGES uses") {
			t.Fatalf("checkMakefile = %v, want rejected default package list", err)
		}
	})
	for _, tc := range []struct{ name, source string }{
		{"missing wiring", ""},
		{"missing runner", strings.Replace(valid, "\tgo -C tools run ./testshard -run .. argonreview\n", "", 1)},
		{"altered exclusion", strings.Replace(valid, `"/cmd/argonreview"`, `"/cmd/other"`, 1)},
		{"missing empty guard", strings.Replace(valid, "$(error no packages outside cmd/argonreview)", "", 1)},
		{"altered condition", strings.Replace(valid, "ifeq ($(origin GO_TEST_PACKAGES),undefined)", "ifdef GO_TEST_PACKAGES", 1)},
		{"altered explicit packages", strings.Replace(valid, "$(GO_TEST_FLAGS) $(GO_TEST_PACKAGES)", "$(GO_TEST_FLAGS) ./...", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkMakefile(tc.source); err == nil || !strings.Contains(err.Error(), "local check-test wiring appears 0 times") {
				t.Fatalf("checkMakefile = %v, want missing local wiring", err)
			}
		})
	}
}

func browserShards() []shard {
	const n = 3
	specs := []shard{{"web-nonbrowser", []string{"./internal/web"}, "", "-p 2 -skip " + browserTestPattern}}
	for k := 1; k <= n; k++ {
		selector := fmt.Sprintf("%d/%d", k, n)
		specs = append(specs, shard{"web-" + selector, []string{"./internal/web"}, selector, "-p 2 -run " + browserTestPattern})
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
		want string
	}{
		{"empty packages", func(s []shard) []shard { s[0].packages = nil; return s }, "invalid or repeated shard"},
		{"unsharded flags", func(s []shard) []shard { s[0].flags = "-short"; return s[:1] }, "must start with exactly -p 2"},
		{"dropped browser", func(s []shard) []shard { return s[:len(s)-1] }, "incomplete browser range"},
		{"duplicated browser", func(s []shard) []shard { return append(s, s[1]) }, "invalid or repeated shard"},
		{"dropped non-browser", func(s []shard) []shard { return s[1:] }, "one non-browser slice, got 0"},
		{"duplicated non-browser", func(s []shard) []shard { return append(s, s[0]) }, "invalid or repeated shard"},
		{"repeated non-browser range", func(s []shard) []shard { extra := s[0]; extra.name = "extra"; return append(s, extra) }, "one non-browser slice, got 2"},
		{"missing browser range", func(s []shard) []shard { return s[:1] }, "incomplete browser range"},
		{"mismatched total", func(s []shard) []shard { s[3].browser = "3/4"; s[3].name = "web-3/4"; return s }, "browser total 4 disagrees with 3"},
		{"duplicate selector", func(s []shard) []shard { s[2].browser = "3/3"; s[2].name = "other-3/3"; return s }, "repeated browser selector"},
		{"selector name mismatch", func(s []shard) []shard { s[2].browser = "3/3"; return s }, "invalid browser shard"},
		{"bad selector", func(s []shard) []shard { s[3].browser = "invalid"; return s }, "invalid browser shard"},
		{"out of range", func(s []shard) []shard { s[3].browser = "4/3"; return s }, "invalid browser shard"},
		{"unsharded overlap", func(s []shard) []shard { s[3].browser = ""; return s }, "invalid browser shard"},
		{"repeated non-browser tests", func(s []shard) []shard { s[3].flags = "-p 2"; return s }, "browser selector requires -run"},
		{"whole-package overlap", func(s []shard) []shard { s[3].flags = "-p 2"; s[3].browser = ""; return s }, "whole-package range overlaps"},
		{"missing non-browser tests", func(s []shard) []shard { s[0].flags = s[1].flags; return s }, "invalid browser shard"},
		{"skip missing", func(s []shard) []shard { s[0].flags = "-p 2"; return s }, "whole-package range overlaps"},
		{"skip pattern missing", func(s []shard) []shard { s[0].flags = "-p 2 -skip"; return s }, "selection must be exactly"},
		{"skip pattern altered", func(s []shard) []shard { s[0].flags = "-p 2 -skip ^Other$"; return s }, "-skip pattern must be"},
		{"non-browser selector", func(s []shard) []shard { s[0].browser = "1/3"; return s }, "non-browser slice must have an empty selector"},
		{"run pattern missing", func(s []shard) []shard { s[1].flags = "-p 2 -run"; return s }, "selection must be exactly"},
		{"run pattern altered", func(s []shard) []shard { s[1].flags = "-p 2 -run ^Other$"; return s }, "-run pattern must be"},
		{"run duplicated", func(s []shard) []shard { s[1].flags += " -run ^TestBrowser$"; return s }, "selection must be exactly"},
		{"skip duplicated", func(s []shard) []shard { s[0].flags += " -skip ^TestBrowser$"; return s }, "selection must be exactly"},
		{"run and skip", func(s []shard) []shard { s[1].flags += " -skip ^TestBrowser$"; return s }, "selection must be exactly"},
		{"p missing", func(s []shard) []shard { s[1].flags = "-run ^TestBrowser$"; return s }, "must start with exactly -p 2"},
		{"p duplicated", func(s []shard) []shard { s[1].flags += " -p 2"; return s }, "selection must be exactly"},
		{"p altered", func(s []shard) []shard { s[1].flags = "-p 4 -run ^TestBrowser$"; return s }, "must start with exactly -p 2"},
		{"reordered flags", func(s []shard) []shard { s[1].flags = "-run ^TestBrowser$ -p 2"; return s }, "must start with exactly -p 2"},
		{"unknown flag", func(s []shard) []shard { s[1].flags += " -short"; return s }, "selection must be exactly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := partition(all, tc.edit(browserShards())); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("partition = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("four browser slices", func(t *testing.T) {
		four := append(browserShards(), specs[len(specs)-1])
		for k := 1; k < len(four); k++ {
			four[k].browser = fmt.Sprintf("%d/%d", k, len(four)-1)
			four[k].name = "web-" + four[k].browser
		}
		parts, err := partition(all, four)
		if err != nil {
			t.Fatal(err)
		}
		if err := covers(parts, all, four); err != nil {
			t.Fatal(err)
		}
	})
	delete(parts, specs[1].name)
	if err := covers(parts, all, specs); err == nil {
		t.Fatal("accepted missing package slice")
	}
}

func TestScheduledBrowserShards(t *testing.T) {
	valid := "shard: [web-nonbrowser, web-1/3, web-2/3, web-3/3]\n" + steps[rest][0] + "\n" + steps["matrix"][0] + "\n"
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
	// both browser and non-browser flags contain an end-of-name regex anchor.
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
		{"web-nonbrowser", "0", "", ""},
		{"web-nonbrowser", "7", "", "running shard web-nonbrowser: exit status 7"},
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
