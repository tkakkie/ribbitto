// Command testshard splits the application module's Go test packages
// between CI's parallel jobs. It prints one shard's package patterns for
// make check-test; -check fails unless the shards together list every
// package of go list ./... exactly once and the workflow runs each shard in
// exactly one job. The named shards hold the slowest packages; "rest" is
// everything else, so a package added later runs in CI without editing this
// list or the workflow. make check runs -check.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The named shards hold the packages that take longest on CI (#992), so
// each job finishes at about the same time; "rest" runs in the checks job.
// cmd and web run separately so their browser tests do not compete for CPUs.
var shards = []struct {
	name     string
	packages []string
}{
	{"loadgen", []string{"./cmd/loadgen", "./cmd/seed"}},
	{"cmd", []string{"./cmd/ribbitto"}},
	{"web", []string{"./internal/web"}},
	{"realtime", []string{"./internal/realtime"}},
}

const rest = "rest"

const workflow = ".github/workflows/ci.yml"

// The workflow names the shards it runs in a test matrix line, and runs each
// shard with one of the two steps below. -check reads the workflow without
// its comment lines and requires each step's lines exactly once, so a shard
// dropped, repeated, replaced by another package list or made unable to fail
// (Muse Code on #1007) fails make check.
var (
	matrixLine = regexp.MustCompile(`(?m)^\s*shard: \[(.*)\]\s*$`)
	steps      = map[string][]string{
		rest:     {`packages=$(go -C tools run ./testshard .. rest)`, `test -n "$packages"`, `make check-test GO_TEST_PACKAGES="$packages"`},
		"matrix": {`packages=$(go -C tools run ./testshard .. "$SHARD")`, `test -n "$packages"`, `make check-test GO_TEST_PACKAGES="$packages"`},
	}
)

func main() {
	check := flag.Bool("check", false, "fail unless the shards list every package exactly once")
	flag.Parse()
	if *check && flag.NArg() == 1 || !*check && flag.NArg() == 2 {
		if err := run(flag.Arg(0), flag.Arg(1), *check); err != nil {
			fmt.Fprintln(os.Stderr, "testshard:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "usage: testshard <repository root> <shard> | testshard -check <repository root>")
	os.Exit(2)
}

func run(root, shard string, check bool) error {
	all, err := list(root)
	if err != nil {
		return err
	}
	parts, err := partition(all)
	if err != nil {
		return err
	}
	if check {
		if err := covers(parts, all); err != nil {
			return err
		}
		source, err := os.ReadFile(filepath.Join(root, workflow))
		if err != nil {
			return fmt.Errorf("reading the workflow: %w", err)
		}
		return scheduled(string(source))
	}
	packages, err := pick(parts, shard)
	if err != nil {
		return err
	}
	// One line: make check-test passes the list in a variable, and a newline
	// there would end the recipe line.
	fmt.Println(strings.Join(packages, " "))
	return nil
}

func pick(parts map[string][]string, shard string) ([]string, error) {
	packages, ok := parts[shard]
	if !ok {
		return nil, fmt.Errorf("unknown shard %q", shard)
	}
	// An empty list would make go test test only the root package and pass.
	if len(packages) == 0 {
		return nil, fmt.Errorf("shard %q has no packages", shard)
	}
	return packages, nil
}

// list returns the module's packages as ./-relative patterns, the form the
// named shards use.
func list(root string) ([]string, error) {
	out, err := goList(root, "-m")
	if err != nil {
		return nil, err
	}
	module := strings.TrimSpace(out)
	paths, err := goList(root, "./...")
	if err != nil {
		return nil, err
	}
	var all []string
	for _, path := range strings.Fields(paths) {
		switch {
		case path == module:
			all = append(all, ".")
		case strings.HasPrefix(path, module+"/"):
			all = append(all, "./"+strings.TrimPrefix(path, module+"/"))
		default:
			return nil, fmt.Errorf("package %s is outside module %s", path, module)
		}
	}
	return all, nil
}

func goList(root string, args ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// partition assigns every package to one shard. A named package that no
// longer exists is an error, so a rename cannot leave a shard quietly empty.
func partition(all []string) (map[string][]string, error) {
	parts := map[string][]string{}
	named := map[string]string{}
	for _, s := range shards {
		for _, p := range s.packages {
			if other, ok := named[p]; ok {
				return nil, fmt.Errorf("%s is in shards %s and %s", p, other, s.name)
			}
			if !slices.Contains(all, p) {
				return nil, fmt.Errorf("shard %s names %s, which go list ./... does not list", s.name, p)
			}
			named[p] = s.name
			parts[s.name] = append(parts[s.name], p)
		}
	}
	parts[rest] = []string{}
	for _, p := range all {
		if _, ok := named[p]; !ok {
			parts[rest] = append(parts[rest], p)
		}
	}
	return parts, nil
}

// covers checks the partition itself, so a later change to partition cannot
// drop or repeat a package unnoticed.
func covers(parts map[string][]string, all []string) error {
	seen := map[string]string{}
	for name, packages := range parts {
		for _, p := range packages {
			if other, ok := seen[p]; ok {
				return fmt.Errorf("%s is in shards %s and %s", p, other, name)
			}
			seen[p] = name
		}
	}
	for _, p := range all {
		if _, ok := seen[p]; !ok {
			return fmt.Errorf("%s is in no shard", p)
		}
	}
	if len(seen) != len(all) {
		return fmt.Errorf("shards list %d packages, go list ./... lists %d", len(seen), len(all))
	}
	return nil
}

// scheduled checks that the workflow runs every shard exactly once: each
// named shard once in the test matrix and rest once in its own step.
func scheduled(source string) error {
	var kept, trimmed []string
	for _, line := range strings.Split(source, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
			trimmed = append(trimmed, strings.TrimSpace(line))
		}
	}
	source = strings.Join(kept, "\n")
	lines := matrixLine.FindAllStringSubmatch(source, -1)
	if len(lines) != 1 {
		return fmt.Errorf("%s: want one test matrix line \"shard: [...]\", found %d", workflow, len(lines))
	}
	var want, got []string
	for _, s := range shards {
		want = append(want, s.name)
	}
	for _, name := range strings.Split(lines[0][1], ",") {
		got = append(got, strings.TrimSpace(name))
	}
	for _, name := range got {
		if !slices.Contains(want, name) {
			return fmt.Errorf("%s: the test matrix names unknown shard %q", workflow, name)
		}
	}
	for _, name := range want {
		if n := len(slices.DeleteFunc(slices.Clone(got), func(g string) bool { return g != name })); n != 1 {
			return fmt.Errorf("%s: the test matrix runs shard %s %d times, want once", workflow, name, n)
		}
	}
	for _, name := range []string{rest, "matrix"} {
		if n := runs(trimmed, steps[name]); n != 1 {
			return fmt.Errorf("%s: the %s step's lines appear %d times, want once: %q", workflow, name, n, steps[name])
		}
	}
	// No other step may pick its own package list.
	if n := strings.Count(source, "make check-test"); n != len(steps) {
		return fmt.Errorf("%s: make check-test runs in %d places, want %d", workflow, n, len(steps))
	}
	return nil
}

// runs counts where want appears as consecutive lines.
func runs(lines, want []string) int {
	n := 0
	for i := range lines {
		if i+len(want) <= len(lines) && slices.Equal(lines[i:i+len(want)], want) {
			n++
		}
	}
	return n
}
