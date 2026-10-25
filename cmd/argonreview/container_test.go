package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeContainerRunner struct {
	runFn func(context.Context, []string, []string, io.Writer, io.Writer) (int, error)
}

func (f fakeContainerRunner) run(ctx context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
	return f.runFn(ctx, args, env, stdout, stderr)
}

func containerFixture(t *testing.T) string {
	t.Helper()
	dir := resolved(t, t.TempDir())
	gitT(t, dir, "init", "--quiet", "--template=")
	write(t, filepath.Join(dir, "go.mod"), "module example.invalid/check\n\ngo 1.27.3\n")
	write(t, filepath.Join(dir, "tools/go.mod"), "require github.com/a-h/templ v0.3.1070\nrequire github.com/sqlc-dev/sqlc v1.31.1\n")
	write(t, filepath.Join(dir, "check.go"), "package check\n")
	return dir
}

func TestContainerValidation(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		valid   bool
	}{
		{".", true}, {"./...", true}, {"./internal/foo/...", true}, {"./cmd/argonreview", true}, {"./a_b-c/x1", true},
		{"-run", false}, {"-x", false}, {"a b", false}, {"../x", false}, {"./a/../b", false}, {"", false},
		{"-foo", false}, {"/absolute", false}, {"./.../x", false}, {"./foo/.../..", false}, {"./a//b", false},
		{"./a/", false}, {"./..", false}, {"./a/./b", false}, {"./a\nb", false}, {"./a;true", false}, {"all", false},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Fatal("Docker reached during argument refusal")
				return 0, nil
			}}
			err := validateContainerCheck("tree", "test", tc.pattern)
			if (err == nil) != tc.valid {
				t.Fatalf("validation %q: %v", tc.pattern, err)
			}
			if !tc.valid {
				if _, err := runContainerCheck(context.Background(), "tree", "test", tc.pattern, runner, t.TempDir()); err == nil {
					t.Fatal("invalid pattern accepted")
				}
			}
		})
	}
	for _, tc := range []struct{ tree, check string }{{"", "build"}, {"tree", "shell"}, {"tree", "-x"}, {"tree", ""}, {"x\x00y", "test"}} {
		if err := validateContainerCheck(tc.tree, tc.check, "./..."); err == nil {
			t.Fatalf("accepted %q %q", tc.tree, tc.check)
		}
	}
}

func TestContainerDispatchAndTrust(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"container-check", "/tree", "build", "./..."}, 1},
		{[]string{"container-check", "/tree", "vet", "."}, 1},
		{[]string{"container-check", "/tree", "test", "./internal/foo/..."}, 1},
		{[]string{"container-check"}, 2}, {[]string{"container-check", "/tree", "test"}, 2},
		{[]string{"container-check", "/tree", "test", "./...", "extra"}, 2},
		{[]string{"container-check", "/tree", "run", "./..."}, 2},
		{[]string{"container-check", "/tree", "test", "-x"}, 2},
		{[]string{"verify", "/tree", "test", "./..."}, 2},
	} {
		var out, stderr bytes.Buffer
		if code := run(tc.args, nil, &out, &stderr); code != tc.code || out.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("%v: %d, %q, %q", tc.args, code, out.String(), stderr.String())
		}
	}
}

func TestContainerArgumentsGolden(t *testing.T) {
	for _, check := range []string{"build", "vet", "test"} {
		t.Run(check, func(t *testing.T) {
			for _, name := range []string{"GH_TOKEN", "RIBBITTO_DATABASE_URL", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "GOPATH", "GOMODCACHE", "GOENV", "HOME", "PATH"} {
				t.Setenv(name, "dummy-excluded")
			}
			want := []string{
				"run", "--pull=never", "--name", "argonreview-check-fixed",
				"--network", "none", "--label", "argonreview.owner=verify", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user", "1000:1000",
				"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777",
				"--tmpfs", "/home/check:rw,exec,nosuid,nodev,size=1g,uid=1000,gid=1000,mode=0700",
				"--mount", "type=bind,src=/var/cache/argonreview/go-mod,dst=/go/pkg/mod,readonly",
				"--mount", "type=bind,src=/tmp/copy/tree,dst=/work,readonly", "-w", "/work",
				"-e", "GOPROXY=off", "-e", "GOTOOLCHAIN=local", "-e", "GOWORK=off", "-e", "GOFLAGS=-mod=readonly", "-e", "CGO_ENABLED=0",
				"-e", "HOME=/home/check", "-e", "GOCACHE=/home/check/cache", "-e", "GOTMPDIR=/tmp", "-e", "GOMODCACHE=/go/pkg/mod", "-e", "GOPATH=/go",
				"--memory", "2g", "--memory-swap", "2g", "--pids-limit", "256", "--cpus", "2",
				"sha256:acb981ac1cf26d383cfe4a4791d5ca5ecd4507e594992585230b0c7d7e4768d1", "sh", "-c", `set -eu
mkdir /tmp/work
cp -R /work/. /tmp/work
cd /tmp/work
/usr/local/bin/templ generate >&2
/usr/local/bin/sqlc generate >&2
/usr/local/bin/tailwindcss -i web/styles/app.css -o web/static/css/app.css --minify >&2
exec go "$@"
`, "sh", check,
			}
			if check == "build" {
				want = append(want, "-o", "/dev/null")
			}
			if check == "test" {
				want = append(want, "-count=1", "-json")
			}
			want = append(want, "./...")
			if got := containerArguments("/tmp/copy/tree", "argonreview-check-fixed", check, "./..."); !reflect.DeepEqual(got, want) {
				t.Fatalf("args:\n%q\nwant:\n%q", got, want)
			}
			if got, want := dockerEnvironment(), []string{"PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin", "HOME=/nonexistent", "DOCKER_CONFIG=/nonexistent", "DOCKER_HOST=unix:///var/run/docker.sock"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("Docker environment differs: %q", got)
			}
			if containerTimeout != 10*time.Minute || containerCleanupTimeout != 20*time.Second || containerOutputLimit != 4<<20 || containerFileLimit != 20000 || containerCopyLimit != 128<<20 {
				t.Fatal("trusted limits changed")
			}
		})
	}
}

func TestContainerPrelude(t *testing.T) {
	if !strings.HasPrefix(containerPrelude, "set -eu\n") || !strings.HasSuffix(containerPrelude, "exec go \"$@\"\n") {
		t.Fatal("prelude must fail on generator errors and exec positional Go arguments")
	}
	generators := 0
	for line := range strings.SplitSeq(containerPrelude, "\n") {
		if strings.HasPrefix(line, "/usr/local/bin/") {
			generators++
			if !strings.HasSuffix(line, " >&2") {
				t.Fatalf("generator stdout contaminates test2json: %q", line)
			}
		}
	}
	if generators != 3 {
		t.Fatalf("got %d generators", generators)
	}
	for _, check := range []string{"build", "vet", "test"} {
		for _, pattern := range []string{"./...", "./internal/foo/..."} {
			args := containerArguments("copy", "name", check, pattern)
			if strings.Contains(containerPrelude, pattern) || args[len(args)-1] != pattern || args[len(args)-2] == pattern {
				t.Fatal("package pattern must be a separate positional argument")
			}
		}
	}
}

func TestContainerToolVersions(t *testing.T) {
	const templ = "github.com/a-h/templ v0.3.1070"
	const sqlc = "github.com/sqlc-dev/sqlc v1.31.1"
	const equal = "require " + templ + "\nrequire " + sqlc + "\n"
	repositoryTools, err := os.ReadFile("../../tools/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"equal", equal, true},
		{"block", "require (\n" + templ + "\n" + sqlc + "\n)\n", true},
		{"indirect", "// require " + templ + "\nrequire ( // tools\n" + templ + " // indirect\n" + sqlc + " // indirect\n)\n", true},
		{"single indirect", strings.ReplaceAll(equal, "\n", " // indirect\n"), true},
		{"unterminated require", "require (\n" + templ + "\n" + sqlc + "\n", false},
		{"replace templ", equal + "replace github.com/a-h/templ => ../templ\n", false},
		{"replace sqlc", equal + "replace " + sqlc + " => ../sqlc\n", false},
		{"block replace", equal + "replace (\n" + templ + " => ../templ\n)\n", false},
		{"unrelated replace", "replace example.com/other => ../other\n" + equal, true},
		{"unrelated block replace", "replace (\nexample.com/other v1.0.0 => ../other\n)\n" + equal, true},
		{"other blocks", equal + "tool (\ngithub.com/a-h/templ\n)\nexclude (\n" + sqlc + "\n)\n", true},
		{"repository tools", string(repositoryTools), true},
		{"tab indented block", "require (\n\t" + templ + "\n\t" + sqlc + "\n)\n", true},
		{"commented header", "require ( // tools \"(`\n\t" + templ + "\n\t" + sqlc + "\n)\n", true},
		{"unrelated tool", equal + "tool github.com/a-h/templ/cmd/templ\n", true},
		{"unrelated exclude", equal + "exclude " + templ + "\n", true},
		{"unknown block", equal + "unknown (\n\t" + templ + "\n)\n", true},
		{"empty args", equal + "require\nreplace\n", true},
		{"require no space", equal + "require(\n" + templ + "\n)\n", false},
		{"replace no space", equal + "replace(\ngithub.com/a-h/templ => ../templ\n)\n", false},
		{"one line replace", equal + "replace ( github.com/a-h/templ => ../templ )\n", false},
		{"quoted replace", equal + "replace \"github.com/a-h/templ\" => ../templ\n", false},
		{"backtick replace", equal + "replace `github.com/a-h/templ` => ../templ\n", false},
		{"quoted require", equal + "require \"github.com/a-h/templ\" v0.3.1071\n", false},
		{"backtick require", equal + "require `github.com/a-h/templ` v0.3.1071\n", false},
		{"unterminated replace", equal + "replace (\nexample.com/other => ../other\n", false},
		{"unterminated tool", equal + "tool (\ngithub.com/a-h/templ/cmd/templ\n", false},
		{"replace header in block", equal + "require (\nreplace (\n)\n", false},
		{"require header in block", equal + "replace (\nrequire (\n)\n", false},
		{"nested header", equal + "tool (\nexclude (\n)\n", false},
		{"close without block", equal + ")\n", false},
		{"directive close in block", equal + "tool (\nrequire )\n)\n", false},
		{"header extra space", equal + "tool  (\n)\n", false},
		{"header tab", equal + "tool\t(\n)\n", false},
		{"parenthesis in directive", equal + "tool( (\n)\n", false},
		{"templ older", strings.ReplaceAll(equal, "v0.3.1070", "v0.3.1069"), false},
		{"templ newer", strings.ReplaceAll(equal, "v0.3.1070", "v0.3.1071"), false},
		{"sqlc older", strings.ReplaceAll(equal, "v1.31.1", "v1.31.0"), false},
		{"sqlc newer", strings.ReplaceAll(equal, "v1.31.1", "v1.31.2"), false},
		{"missing templ", "require " + sqlc + "\n// require " + templ, false},
		{"missing sqlc", "require " + templ, false},
		{"missing both", "", false},
		{"duplicate", equal + "require " + templ, false},
		{"block duplicate", equal + "require (\n" + sqlc + "\n)\n", false},
		{"conflicting", equal + "require github.com/sqlc-dev/sqlc v1.31.2", false},
		{"malformed", equal + "require github.com/a-h/templ", false},
		{"missing file", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := containerFixture(t)
			write(t, filepath.Join(tree, "tools/go.mod"), tc.data)
			if tc.name == "missing file" {
				if err := os.Remove(filepath.Join(tree, "tools/go.mod")); err != nil {
					t.Fatal(err)
				}
			}
			runs := 0
			runner := fakeContainerRunner{func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
				if !tc.valid {
					t.Fatal("Docker ran after version refusal")
				}
				if args[0] == "run" {
					runs++
				}
				return 0, nil
			}}
			_, err := runContainerCheck(context.Background(), tree, "build", ".", runner, t.TempDir())
			if (err == nil) != tc.valid || tc.valid && runs != 1 || err != nil && !strings.Contains(err.Error(), "the verify image needs a rebuild") {
				t.Fatalf("version check: runs %d, error %v", runs, err)
			}
		})
	}
}

func TestContainerGoVersion(t *testing.T) {
	for _, tc := range []struct {
		data  string
		valid bool
	}{
		{"module x\n\ngo 1.27.3\n", true}, {"go\t1.27.3 // image\n", true},
		{"go 1.27\n", false}, {"go 1.27.2\n", false}, {"module x\n", false}, {"go\n", false},
		{"go 1.27.3\ngo 1.27.3\n", false}, {"go 1.27.3 extra\n", false},
	} {
		if err := checkContainerGoVersion([]byte(tc.data)); (err == nil) != tc.valid {
			t.Fatalf("%q: %v", tc.data, err)
		}
	}
	dir := containerFixture(t)
	write(t, filepath.Join(dir, "go.mod"), "module x\ngo 1.27.2\n")
	runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("Docker ran for mismatched Go version")
		return 0, nil
	}}
	if _, err := runContainerCheck(context.Background(), dir, "build", "./...", runner, t.TempDir()); err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("version mismatch: %v", err)
	}
}

func TestContainerRunAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name                                                                     string
		status                                                                   int
		fail, timeout, interrupt, overflow, cleanupFail, stopFail, lateInterrupt bool
	}{
		{name: "success"}, {name: "nonzero", status: 1}, {name: "OOM", status: 137}, {name: "abnormal", status: -1},
		{name: "runner error", fail: true}, {name: "timeout", timeout: true}, {name: "interrupt", interrupt: true},
		{name: "output limit", overflow: true}, {name: "cleanup fails", fail: true, cleanupFail: true},
		{name: "cleanup fails after test failure", status: 1, cleanupFail: true},
		{name: "run never created container", status: 125, cleanupFail: true},
		{name: "cleanup fails after success", cleanupFail: true}, {name: "stop fails", stopFail: true}, {name: "late interrupt", lateInterrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := containerFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.timeout {
				var end context.CancelFunc
				ctx, end = context.WithDeadline(ctx, time.Now().Add(2*time.Second))
				defer end()
			}
			var copyDir, name string
			var calls []string
			runner := fakeContainerRunner{func(runCtx context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
				calls = append(calls, args[0])
				if !reflect.DeepEqual(env, dockerEnvironment()) {
					t.Fatal("environment changed")
				}
				if args[0] != "run" {
					if runCtx.Err() != nil {
						t.Fatal("cleanup reused canceled context")
					}
					if _, err := os.Stat(copyDir); err != nil {
						t.Fatal("source removed before container cleanup")
					}
					want := []string{"stop", "--time", "2", name}
					if args[0] == "rm" {
						want = []string{"rm", "-f", name}
					}
					if !reflect.DeepEqual(args, want) {
						t.Fatalf("cleanup %q", args)
					}
					if tc.lateInterrupt && args[0] == "rm" {
						cancel()
					}
					if tc.stopFail && args[0] == "stop" {
						return 1, errors.New("stop failed")
					}
					if tc.cleanupFail {
						if tc.status == 125 {
							return 1, errors.New("No such container")
						}
						return 1, errors.New("daemon unavailable")
					}
					return 0, nil
				}
				name = args[3]
				if !strings.HasPrefix(name, "argonreview-check-") || len(name) != len("argonreview-check-")+32 {
					t.Fatalf("name %q", name)
				}
				for i, arg := range args {
					if arg == "--mount" && strings.HasSuffix(args[i+1], ",dst=/work,readonly") {
						copyDir = strings.TrimSuffix(strings.TrimPrefix(args[i+1], "type=bind,src="), ",dst=/work,readonly")
					}
				}
				if copyDir == "" || strings.HasPrefix(copyDir, dir) {
					t.Fatalf("mounted %q", copyDir)
				}
				if _, err := os.Stat(filepath.Join(copyDir, "go.mod")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(copyDir, ".git")); !os.IsNotExist(err) {
					t.Fatalf("copied .git: %v", err)
				}
				if deadline, ok := runCtx.Deadline(); !ok || time.Until(deadline) > 10*time.Minute {
					t.Fatal("no fixed wall-clock bound")
				}
				if tc.timeout {
					<-runCtx.Done()
					return -1, runCtx.Err()
				}
				if tc.interrupt {
					cancel()
					return -1, runCtx.Err()
				}
				if tc.fail {
					return -1, errors.New("lost connection")
				}
				if tc.overflow {
					_, _ = io.WriteString(stdout, strings.Repeat("x", containerOutputLimit-1))
					_, _ = io.WriteString(stderr, "too much")
					if runCtx.Err() == nil {
						t.Fatal("excess output did not cancel run")
					}
				} else {
					_, _ = io.WriteString(stdout, "compiler output")
				}
				return tc.status, nil
			}}
			report, err := runContainerCheck(ctx, dir, "build", "./...", runner, t.TempDir())
			success := tc.name == "success" || tc.name == "stop fails"
			if strings.HasPrefix(report, "verified\n") != success {
				t.Fatalf("report %q, err %v", report[:min(len(report), 300)], err)
			}
			want := []string{"run", "stop", "rm"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls %v, want %v", calls, want)
			}
			if _, err := os.Stat(filepath.Dir(copyDir)); !os.IsNotExist(err) {
				t.Fatalf("copy left behind: %v", err)
			}
			if tc.overflow && (!strings.Contains(report, "output cut off") || len(report) > containerOutputLimit+400) {
				t.Fatalf("unbounded report: %d", len(report))
			}
			if !strings.Contains(report, "Race detector was not run") || !strings.Contains(report, "database/browser tests are CI-only") {
				t.Fatal("coverage limits absent")
			}
			if tc.cleanupFail {
				want := "container removal failed"
				if tc.fail || tc.status == 125 {
					want = "docker run failed; container may never have existed"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("cleanup failure: %v, want %q", err, want)
				}
			}
		})
	}
}

func TestContainerNamesAreUnique(t *testing.T) {
	dir := containerFixture(t)
	names := map[string]bool{}
	runner := fakeContainerRunner{func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
		if args[0] != "run" {
			return 0, nil
		}
		name := args[3]
		if names[name] {
			t.Fatal("container name reused")
		}
		names[name] = true
		return 0, nil
	}}
	for range 2 {
		if _, err := runContainerCheck(context.Background(), dir, "vet", ".", runner, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestContainerValidationCleanup(t *testing.T) {
	for _, tc := range []string{"missing go.mod", "wrong version", "refused name"} {
		t.Run(tc, func(t *testing.T) {
			tree := containerFixture(t)
			switch tc {
			case "missing go.mod":
				if err := os.Remove(filepath.Join(tree, "go.mod")); err != nil {
					t.Fatal(err)
				}
			case "wrong version":
				write(t, filepath.Join(tree, "go.mod"), "go 1.27.1\n")
			case "refused name":
				write(t, filepath.Join(tree, "sub/AGENTS.md"), "refused")
			}
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Fatal("Docker ran after refusal")
				return 0, nil
			}}
			copyParent := t.TempDir()
			if _, err := runContainerCheck(context.Background(), tree, "test", "./...", runner, copyParent); err == nil {
				t.Fatal("validation passed")
			}
			entries, err := os.ReadDir(copyParent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary copy left: %v, %v", entries, err)
			}
		})
	}
}

func TestContainerEndToEndVerdicts(t *testing.T) {
	for _, tc := range []struct {
		check, output, verdict string
		status                 int
	}{
		{"build", "build output", "verified", 0}, {"vet", "vet output", "FAILED", 1},
		{"test", containerEvents("p", "TestOne", "pass"), "verified", 0},
		{"test", containerEvents("p", "TestDB", "skip"), "NOT VERIFIED: 1 skipped\np/TestDB", 0},
		{"test", containerEvents("p", "TestOne", "pass"), "FAILED", 137},
		{"build", "", "FAILED", 1}, {"vet", "", "FAILED", 1}, {"test", "", "FAILED", 1}, // Generator failure: no Go output.
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.check, tc.verdict, tc.status), func(t *testing.T) {
			tree := containerFixture(t)
			runner := fakeContainerRunner{func(_ context.Context, args, _ []string, stdout, stderr io.Writer) (int, error) {
				if args[0] == "run" {
					_, _ = io.WriteString(stderr, "generator diagnostic")
					_, _ = io.WriteString(stdout, tc.output)
					return tc.status, nil
				}
				return 0, nil
			}}
			report, err := runContainerCheck(context.Background(), tree, tc.check, ".", runner, t.TempDir())
			if err != nil || !strings.HasPrefix(report, tc.verdict+"\n") {
				t.Fatalf("report %q, err %v", report, err)
			}
			if !strings.Contains(report, "Ran on regenerated sources (templ, sqlc, Tailwind); whether the committed generated files are current is checked by CI.") ||
				!strings.Contains(report, "--- stdout\n"+tc.output+"\n--- stderr\ngenerator diagnostic") {
				t.Fatalf("scope or stream separation: %q", report)
			}
		})
	}
}

func TestContainerSourceCleanupFailure(t *testing.T) {
	tree := containerFixture(t)
	runner := fakeContainerRunner{func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
		if args[0] == "run" {
			for _, arg := range args {
				if !strings.HasSuffix(arg, ",dst=/work,readonly") {
					continue
				}
				copyDir := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
				if err := os.Chmod(copyDir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(copyDir, 0o700); err != nil {
						t.Error(err)
					}
				})
			}
		}
		return 0, nil
	}}
	report, err := runContainerCheck(context.Background(), tree, "build", ".", runner, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "removing source copy") || !strings.HasPrefix(report, "FAILED: source cleanup failed\n") {
		t.Fatalf("source cleanup failure: report %q, error %v", report, err)
	}
	if !strings.Contains(report, "build check result could not be confirmed.") || strings.Contains(report, "Ran on regenerated sources") {
		t.Fatalf("cleanup must identify the check without claiming regeneration: %q", report)
	}
}
