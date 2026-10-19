// Command testshard splits the application module's Go test packages
// between CI's parallel jobs. It prints package patterns or runs make
// check-test with a shard's packages, browser selector and flags; -check
// fails unless every package is covered and the workflow runs each shard in
// exactly one job. The named shards hold the slowest packages; "rest" is
// everything else, so a package added later runs in CI without editing this
// list or the workflow. make check runs -check.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The named shards hold the packages that take longest on CI (#992), so
// each job finishes at about the same time; "rest" runs in the checks job.
// cmd and web run separately so their browser tests do not compete for CPUs.
type shard struct {
	name     string
	packages []string
	browser  string
	flags    string
}

func shards() []shard {
	return []shard{
		{"loadgen", []string{"./cmd/loadgen", "./cmd/seed"}, "", "-p 2"},
		{"cmd", []string{"./cmd/ribbitto"}, "", "-p 2"},
		{"web-1/3", []string{"./internal/web"}, "1/3", "-p 2"},
		{"web-2/3", []string{"./internal/web"}, "2/3", "-p 2 -run ^TestBrowser$"},
		{"web-3/3", []string{"./internal/web"}, "3/3", "-p 2 -run ^TestBrowser$"},
		{"realtime", []string{"./internal/realtime"}, "", "-p 2"},
	}
}

const rest = "rest"

const workflow = ".github/workflows/ci.yml"

// Exact commands keep package lists and flags under testshard's control.
var (
	matrixLine = regexp.MustCompile(`(?m)^\s*shard: \[(.*)\]\s*$`)
	steps      = map[string][]string{
		rest:     {`go -C tools run ./testshard -run .. rest`},
		"matrix": {`go -C tools run ./testshard -run .. "$SHARD"`},
	}
)

func main() {
	execute := flag.Bool("run", false, "run make check-test with the shard configuration")
	check := flag.Bool("check", false, "fail unless every package slice is scheduled exactly once")
	flag.Parse()
	if *check && !*execute && flag.NArg() == 1 || !*check && flag.NArg() == 2 {
		if err := run(flag.Arg(0), flag.Arg(1), *check, *execute); err != nil {
			fmt.Fprintln(os.Stderr, "testshard:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "usage: testshard [-run] <repository root> <shard> | testshard -check <repository root>")
	os.Exit(2)
}

func run(root, name string, check, execute bool) error {
	all, err := list(root)
	if err != nil {
		return err
	}
	specs := shards()
	parts, err := partition(all, specs)
	if err != nil {
		return err
	}
	if check {
		if err := covers(parts, all, specs); err != nil {
			return err
		}
		if err := checkBrowserTests(root, specs); err != nil {
			return err
		}
		source, err := os.ReadFile(filepath.Join(root, workflow))
		if err != nil {
			return fmt.Errorf("reading the workflow: %w", err)
		}
		return scheduled(string(source), specs)
	}
	packages, err := pick(parts, name)
	if err != nil {
		return err
	}
	if execute {
		s := shard{name: name, packages: packages, flags: "-p 2"}
		for _, candidate := range specs {
			if candidate.name == name {
				s = candidate
			}
		}
		dir, err := os.MkdirTemp("", "ribbitto-browser-count-*")
		if err != nil {
			return fmt.Errorf("creating browser count directory: %w", err)
		}
		defer os.RemoveAll(dir)
		countFile := filepath.Join(dir, "count")
		cmd := testCommand(root, s)
		cmd.Env = append(cmd.Env, "RIBBITTO_BROWSER_COUNT_FILE="+countFile)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr := cmd.Run()
		if line, err := os.ReadFile(countFile); err == nil {
			fmt.Print(string(line))
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("reading browser count: %w", err)
		}
		if runErr != nil {
			return fmt.Errorf("running shard %s: %w", name, runErr)
		}
		return nil
	}
	// One line: make check-test passes the list in a variable, and a newline
	// there would end the recipe line.
	fmt.Println(strings.Join(packages, " "))
	return nil
}

func testCommand(root string, s shard) *exec.Cmd {
	// Make expands command-line variables before the shell sees regex anchors.
	flags := strings.ReplaceAll(s.flags, "$", "$$")
	cmd := exec.Command("make", "check-test", "GO_TEST_PACKAGES="+strings.Join(s.packages, " "), "GO_TEST_FLAGS="+flags)
	cmd.Dir = root
	// Workflow GOFLAGS must not narrow or cache a shard; -check does not read it.
	cmd.Env = append(os.Environ(), "RIBBITTO_BROWSER_SHARD="+s.browser, "GOFLAGS=-count=1")
	return cmd
}

// Read source rather than running package code: a renamed test otherwise makes
// browser-only slices pass with "no tests to run". The totals file also drives
// the web coverage test, so a new workflow total cannot miss that proof.
func checkBrowserTests(root string, specs []shard) error {
	for _, s := range specs {
		if !strings.Contains(s.flags, "-run ^TestBrowser$") {
			continue
		}
		dir := filepath.Join(root, s.packages[0])
		totals, err := os.ReadFile(filepath.Join(dir, "testdata/browser-shard-totals.txt"))
		if err != nil {
			return fmt.Errorf("reading tested browser totals: %w", err)
		}
		_, n, _ := strings.Cut(s.browser, "/")
		if !slices.Contains(strings.Fields(string(totals)), n) {
			return fmt.Errorf("browser shard total %s has no coverage test", n)
		}
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			return fmt.Errorf("listing browser tests: %w", err)
		}
		found := false
		for _, path := range files {
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return fmt.Errorf("parsing browser tests: %w", err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Name.Name != "TestBrowser" || fn.Type.TypeParams != nil || fn.Type.Results != nil || len(fn.Type.Params.List) != 1 {
					continue
				}
				param := fn.Type.Params.List[0]
				ptr, ok := param.Type.(*ast.StarExpr)
				if !ok || len(param.Names) != 1 || param.Names[0].Name != "t" {
					continue
				}
				typ, ok := ptr.X.(*ast.SelectorExpr)
				if ok {
					pkg, ok := typ.X.(*ast.Ident)
					found = found || ok && pkg.Name == "testing" && typ.Sel.Name == "T"
				}
			}
		}
		if !found {
			return fmt.Errorf("%s must declare func TestBrowser(t *testing.T) in a _test.go file", s.packages[0])
		}
	}
	return nil
}

// Browser slices must form one complete set, with non-browser tests on the
// first slice only. Names include the selector so workflow drift is visible.
func validateShards(specs []shard) error {
	names := map[string]bool{}
	groups := map[string][]shard{}
	for _, s := range specs {
		if s.name == rest || names[s.name] || len(s.packages) == 0 {
			return fmt.Errorf("invalid or repeated shard %q", s.name)
		}
		names[s.name] = true
		for _, p := range s.packages {
			groups[p] = append(groups[p], s)
		}
	}
	for p, group := range groups {
		seen := map[int]bool{}
		for _, s := range group {
			if s.browser == "" && len(group) == 1 {
				if s.flags != "-p 2" {
					return fmt.Errorf("shard %s: unsharded packages must run all tests", s.name)
				}
				continue
			}
			k, n, ok := strings.Cut(s.browser, "/")
			index, errK := strconv.Atoi(k)
			total, errN := strconv.Atoi(n)
			if !ok || errK != nil || errN != nil || total != len(group) || index < 1 || index > total || seen[index] || s.browser != fmt.Sprintf("%d/%d", index, total) || !strings.HasSuffix(s.name, "-"+s.browser) || len(s.packages) != 1 {
				return fmt.Errorf("%s: invalid browser shard %q in %s", p, s.browser, s.name)
			}
			seen[index] = true
			want := "-p 2 -run ^TestBrowser$"
			if index == 1 {
				want = "-p 2"
			}
			if s.flags != want {
				return fmt.Errorf("shard %s: flags %q, want %q", s.name, s.flags, want)
			}
		}
	}
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

// partition assigns every package to its declared shards. A named package that no
// longer exists is an error, so a rename cannot leave a shard quietly empty.
func partition(all []string, specs []shard) (map[string][]string, error) {
	if err := validateShards(specs); err != nil {
		return nil, err
	}
	parts := map[string][]string{}
	named := map[string]string{}
	for _, s := range specs {
		for _, p := range s.packages {
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
func covers(parts map[string][]string, all []string, specs []shard) error {
	if err := validateShards(specs); err != nil {
		return err
	}
	remaining := map[string][]string{}
	for name, packages := range parts {
		remaining[name] = slices.Clone(packages)
	}
	for _, s := range specs {
		if !slices.Equal(remaining[s.name], s.packages) {
			return fmt.Errorf("shard %s: packages %v, want %v", s.name, remaining[s.name], s.packages)
		}
		delete(remaining, s.name)
	}
	seen := map[string]string{}
	for _, s := range specs {
		for _, p := range s.packages {
			seen[p] = s.name
		}
	}
	for name, packages := range remaining {
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
func scheduled(source string, specs []shard) error {
	if err := validateShards(specs); err != nil {
		return err
	}
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
	for _, s := range specs {
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
	// Only the runner may invoke make check-test or choose its packages and flags.
	if n := strings.Count(source, "make check-test"); n != 0 {
		return fmt.Errorf("%s: make check-test runs in %d places, want 0", workflow, n)
	}
	for _, name := range []string{"GO_TEST_FLAGS", "GO_TEST_PACKAGES"} {
		if strings.Contains(source, name) {
			return fmt.Errorf("%s: found %s use outside testshard", workflow, name)
		}
	}
	if n := strings.Count(source, "go -C tools run ./testshard"); n != len(steps) {
		return fmt.Errorf("%s: found %d testshard invocations, want %d", workflow, n, len(steps))
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
