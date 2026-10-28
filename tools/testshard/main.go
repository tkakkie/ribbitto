// Command testshard splits the application module's Go test packages
// between CI's parallel jobs. It prints package patterns or runs make
// check-test with a shard's packages, browser selector and flags; -check
// fails unless every package is covered and the workflow runs each shard in
// exactly one job, with disjoint browser and argonreview execution ranges.
// It also pins the local full-run wiring so argonreview gets the same timeout.
// The named shards hold the slowest packages; "rest" is everything else, so
// a package added later runs in CI without editing this list or the workflow.
// make check runs -check.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The named shards hold the packages that take longest on CI (#992), so
// each job finishes at about the same time; "rest" runs in its own test-rest job.
// cmd and web run separately so their browser tests do not compete for CPUs;
// argonreview needs more margin than Go's default package timeout (#1948).
type shard struct {
	name          string
	packages      []string
	browser       string
	flags         string
	kind, pattern string
}

const browserTestName = "TestBrowser"
const browserTestPattern = "^" + browserTestName + "$"
const argonreviewFlags = "-p 2 -timeout 20m"
const argonreviewPackage = "./cmd/argonreview"
const agyCommandPattern = "^TestAgyCheckCommand"
const containerDeadlinePattern = "^TestContainer(Deadline|Timeout|Late|Status125|Runner|Cancellation|Child|Context|Command)"
const aggregationPattern = "^(TestAggregator|TestContainer(Summary|Aggregation))"

func argonreviewUnion(specs []shard) string {
	var patterns []string
	for _, s := range specs {
		if s.kind == "-run" {
			patterns = append(patterns, s.pattern)
		}
	}
	return "(" + strings.Join(patterns, "|") + ")"
}

func shards() []shard {
	specs := []shard{
		{"loadgen", []string{"./cmd/loadgen", "./cmd/seed"}, "", "-p 2", "", ""},
		{"cmd", []string{"./cmd/ribbitto"}, "", "-p 2", "", ""},
		{"argonreview-agy-command", []string{argonreviewPackage}, "", argonreviewFlags, "-run", agyCommandPattern},
		{"argonreview-container-deadline", []string{argonreviewPackage}, "", argonreviewFlags, "-run", containerDeadlinePattern},
		{"argonreview-aggregation", []string{argonreviewPackage}, "", argonreviewFlags, "-run", aggregationPattern},
		{"argonreview-rest", []string{argonreviewPackage}, "", argonreviewFlags, "-skip", ""},
		{"web-nonbrowser", []string{"./internal/web"}, "", "-p 2 -skip " + browserTestPattern, "", ""},
		{"web-1/3", []string{"./internal/web"}, "1/3", "-p 2 -run " + browserTestPattern, "", ""},
		{"web-2/3", []string{"./internal/web"}, "2/3", "-p 2 -run " + browserTestPattern, "", ""},
		{"web-3/3", []string{"./internal/web"}, "3/3", "-p 2 -run " + browserTestPattern, "", ""},
		{"realtime", []string{"./internal/realtime"}, "", "-p 2", "", ""},
	}
	for i := range specs {
		if specs[i].kind == "-skip" {
			specs[i].pattern = argonreviewUnion(specs)
		}
	}
	return specs
}

const rest = "rest"

const ciNeeds = "needs: [checks, test, test-rest, vuln, web-types]"
const ciSuccess = `printf '%s\n' "$NEEDS" | jq -e 'length == 5 and all(.[]; .result == "success")' > /dev/null`

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
		if err := checkArgonreviewTests(root, specs); err != nil {
			return err
		}
		if err := checkBrowserTests(root, specs); err != nil {
			return err
		}
		source, err := os.ReadFile(filepath.Join(root, workflow))
		if err != nil {
			return fmt.Errorf("reading the workflow: %w", err)
		}
		if err := scheduled(string(source), specs); err != nil {
			return err
		}
		source, err = os.ReadFile(filepath.Join(root, "Makefile"))
		if err != nil {
			return fmt.Errorf("reading the Makefile: %w", err)
		}
		return checkMakefile(string(source))
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
		} else if s.browser != "" && runErr == nil {
			return fmt.Errorf("shard %s succeeded without writing a browser scenario count", name)
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

func (s shard) testFlags() string {
	if s.kind == "" {
		return s.flags
	}
	// Shell quoting keeps alternation and grouping intact through make.
	return s.flags + " " + s.kind + " '" + s.pattern + "'"
}

func testCommand(root string, s shard) *exec.Cmd {
	// Make expands command-line variables before the shell sees regex anchors.
	flags := strings.ReplaceAll(s.testFlags(), "$", "$$")
	cmd := exec.Command("make", "check-test", "GO_TEST_PACKAGES="+strings.Join(s.packages, " "), "GO_TEST_FLAGS="+flags)
	cmd.Dir = root
	// Workflow GOFLAGS must not narrow or cache a shard; -check does not read it.
	cmd.Env = append(os.Environ(), "RIBBITTO_BROWSER_SHARD="+s.browser, "GOFLAGS=-count=1")
	return cmd
}

// Read source rather than running package code: a renamed test otherwise makes
// browser-only slices pass with "no tests to run". The totals file also drives
// the web coverage test, so a new workflow total cannot miss that proof.
// Validation ties the non-browser exclusion to the same test name.
func checkBrowserTests(root string, specs []shard) error {
	if err := validateShards(specs); err != nil {
		return err
	}
	for _, s := range specs {
		selection, err := s.selection()
		if err != nil {
			return fmt.Errorf("shard %s: %w", s.name, err)
		}
		if selection != "-run" {
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
				if !ok || fn.Recv != nil || fn.Name.Name != browserTestName || fn.Type.TypeParams != nil || fn.Type.Results != nil || len(fn.Type.Params.List) != 1 {
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

// Only these flag forms preserve the execution ranges checked below. Reject
// extra or reordered tokens rather than interpreting arbitrary go test flags.
func parseFlags(flags string) (string, error) {
	args := strings.Fields(flags)
	if len(args) < 2 || args[0] != "-p" || args[1] != "2" {
		return "", fmt.Errorf("flags %q: must start with exactly -p 2", flags)
	}
	if len(args) == 2 {
		return "", nil
	}
	if len(args) != 4 || (args[2] != "-run" && args[2] != "-skip") {
		return "", fmt.Errorf("flags %q: selection must be exactly -run or -skip %s", flags, browserTestPattern)
	}
	if args[3] != browserTestPattern {
		return "", fmt.Errorf("flags %q: %s pattern must be %s", flags, args[2], browserTestPattern)
	}
	return args[2], nil
}

func (s shard) selection() (string, error) {
	if strings.HasPrefix(s.name, "argonreview-") || slices.Contains(s.packages, argonreviewPackage) || s.kind != "" {
		if !slices.Equal(s.packages, []string{argonreviewPackage}) || s.browser != "" || s.flags != argonreviewFlags {
			return "", fmt.Errorf("argonreview must run only %s with flags %q and no browser selector", argonreviewPackage, argonreviewFlags)
		}
		if (s.kind != "-run" && s.kind != "-skip") || s.pattern == "" || strings.ContainsAny(s.pattern, "/'\n\r") {
			return "", fmt.Errorf("shard %s: argonreview requires a top-level -run or -skip pattern", s.name)
		}
		if _, err := regexp.Compile(s.pattern); err != nil {
			return "", fmt.Errorf("compiling %s: %w", s.name, err)
		}
		return "argonreview", nil
	}
	return parseFlags(s.flags)
}

// Derive the exclusion from the run slices so new tests fall into the remainder.
func validateArgonreviewSlices(specs []shard) error {
	remainders, pinned := 0, 0
	for _, s := range specs {
		if s.kind == "-run" {
			pinned++
			continue
		}
		remainders++
		if s.pattern != argonreviewUnion(specs) {
			return fmt.Errorf("%s: skip must be exactly the run union", s.name)
		}
	}
	if remainders != 1 || pinned == 0 {
		return fmt.Errorf("argonreview needs pinned slices and exactly one remainder")
	}
	return nil
}

func checkArgonreviewTests(root string, specs []shard) error {
	if err := validateShards(specs); err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(root, argonreviewPackage, "*_test.go"))
	if err != nil {
		return fmt.Errorf("listing argonreview tests: %w", err)
	}
	var names []string
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parsing argonreview tests: %w", err)
		}
		if err := platformIndependent(path, file); err != nil {
			return err
		}
		// Like go test, an Example runs only with an Output or Unordered output comment.
		for _, example := range doc.Examples(file) {
			if example.Output != "" || example.EmptyOutput {
				names = append(names, "Example"+example.Name)
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || fn.Type.Results != nil {
				continue
			}
			for prefix, typ := range map[string]string{"Test": "T", "Fuzz": "F"} {
				if !strings.HasPrefix(fn.Name.Name, prefix) {
					continue
				}
				r, _ := utf8.DecodeRuneInString(strings.TrimPrefix(fn.Name.Name, prefix))
				if unicode.IsLower(r) {
					continue
				}
				params := fn.Type.Params.List
				if len(params) != 1 || len(params[0].Names) > 1 {
					continue
				}
				ptr, ok := params[0].Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				// The same name-only test as cmd/go's isTestFunc, which cannot
				// resolve import aliases either; go vet checks the real type.
				selector, ok := ptr.X.(*ast.SelectorExpr)
				ident, local := ptr.X.(*ast.Ident)
				if ok && selector.Sel.Name == typ || local && ident.Name == typ {
					names = append(names, fn.Name.Name)
				}
			}
		}
	}
	owners, counts := map[string]string{}, map[string]int{}
	for _, s := range specs {
		if !slices.Contains(s.packages, argonreviewPackage) {
			continue
		}
		pattern := regexp.MustCompile(s.pattern) // validateShards compiled it above.
		for _, name := range names {
			selected := pattern.MatchString(name)
			if s.kind == "-skip" {
				selected = !selected
			}
			if !selected {
				continue
			}
			if other := owners[name]; other != "" {
				return fmt.Errorf("%s overlaps %s on %s", s.name, other, name)
			}
			owners[name] = s.name
			counts[s.name]++
		}
		if counts[s.name] == 0 {
			return fmt.Errorf("%s selects no tests", s.name)
		}
	}
	return nil
}

// Packages run in full, in test-name slices, or in one non-browser slice plus
// a complete set of browser selectors. Names expose selector drift in the matrix.
func validateShards(specs []shard) error {
	names := map[string]bool{}
	groups := map[string][]shard{}
	for _, s := range specs {
		if s.name == rest || names[s.name] || len(s.packages) == 0 {
			return fmt.Errorf("invalid or repeated shard %q", s.name)
		}
		if _, err := s.selection(); err != nil {
			return fmt.Errorf("shard %s: %w", s.name, err)
		}
		names[s.name] = true
		for _, p := range s.packages {
			groups[p] = append(groups[p], s)
		}
	}
	for p, group := range groups {
		if p == argonreviewPackage {
			if err := validateArgonreviewSlices(group); err != nil {
				return err
			}
			continue
		}
		seen := map[int]bool{}
		nonBrowser, total := 0, 0
		for _, s := range group {
			selection, err := s.selection()
			if err != nil {
				return fmt.Errorf("shard %s: %w", s.name, err)
			}
			switch selection {
			case "":
				if s.browser != "" {
					return fmt.Errorf("shard %s: browser selector requires -run %s", s.name, browserTestPattern)
				}
				if len(group) != 1 {
					return fmt.Errorf("%s: whole-package range overlaps other slices", p)
				}
				continue
			case "-skip":
				if s.browser != "" {
					return fmt.Errorf("shard %s: non-browser slice must have an empty selector", s.name)
				}
				nonBrowser++
				continue
			}
			k, n, ok := strings.Cut(s.browser, "/")
			index, errK := strconv.Atoi(k)
			count, errN := strconv.Atoi(n)
			if !ok || errK != nil || errN != nil || index < 1 || index > count || s.browser != fmt.Sprintf("%d/%d", index, count) || !strings.HasSuffix(s.name, "-"+s.browser) || len(s.packages) != 1 {
				return fmt.Errorf("%s: invalid browser shard %q in %s", p, s.browser, s.name)
			}
			if total != 0 && total != count {
				return fmt.Errorf("%s: browser total %d disagrees with %d", p, count, total)
			}
			if seen[index] {
				return fmt.Errorf("%s: repeated browser selector %s", p, s.browser)
			}
			total = count
			seen[index] = true
		}
		if total == 0 && nonBrowser == 0 {
			continue
		}
		if nonBrowser != 1 {
			return fmt.Errorf("%s: want exactly one non-browser slice, got %d", p, nonBrowser)
		}
		if total == 0 || len(seen) != total {
			return fmt.Errorf("%s: incomplete browser range: got %d slices, want %d (at least one)", p, len(seen), total)
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
// named shard once in the test matrix and rest once in its own job.
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
	for _, line := range []string{ciNeeds, ciSuccess} {
		if runs(trimmed, []string{line}) != 1 {
			return fmt.Errorf("%s: missing or repeated ci gate line %q", workflow, line)
		}
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

// Pin the branch as well as the commands: a timeout applies per test binary,
// so folding the local runs together would raise every package's timeout.
func checkMakefile(source string) error {
	wiring := `check-test: db-health
ifeq ($(origin GO_TEST_PACKAGES),undefined)
	go test -race $(GO_TEST_FLAGS) $(or $(strip $(shell go list -f '{{if ne .ImportPath (print .Module.Path "/cmd/argonreview")}}{{.ImportPath}}{{end}}' ./...)),$(error no packages outside cmd/argonreview))
%selse
	go test -race $(GO_TEST_FLAGS) $(GO_TEST_PACKAGES)
endif`
	var commands string
	for _, s := range shards() {
		if slices.Contains(s.packages, argonreviewPackage) {
			line := "\tgo -C tools run ./testshard -run .. " + s.name
			if n := runs(strings.Split(source, "\n"), []string{line}); n != 1 {
				return fmt.Errorf("Makefile: local check-test wiring appears %d times for %s, want once", n, s.name)
			}
			commands += line + "\n"
		}
	}
	wiring = strings.Replace(wiring, "%s", commands, 1)
	if n := runs(strings.Split(source, "\n"), strings.Split(wiring, "\n")); n != 1 {
		return fmt.Errorf("Makefile: local check-test wiring appears %d times, want once", n)
	}
	// A default package variable would bypass the full-run branch entirely.
	uses := 0
	for _, line := range strings.Split(source, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			uses += strings.Count(line, "GO_TEST_PACKAGES")
		}
	}
	if uses != 2 {
		return fmt.Errorf("Makefile: found %d GO_TEST_PACKAGES uses, want 2 in local check-test wiring", uses)
	}
	// Only the slice lines above and -check may run testshard: an extra command
	// elsewhere could bypass the pinned selection or the timeout.
	const checkLine = "\tgo -C tools run ./testshard -check .."
	if n := runs(strings.Split(source, "\n"), []string{checkLine}); n != 1 {
		return fmt.Errorf("Makefile: testshard -check line appears %d times, want once", n)
	}
	invocations := 0
	for _, line := range strings.Split(source, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			invocations += strings.Count(line, "./testshard")
		}
	}
	if want := strings.Count(wiring, "./testshard") + 1; invocations != want {
		return fmt.Errorf("Makefile: found %d testshard invocations, want %d (the slices and -check)", invocations, want)
	}
	return nil
}

// The source check counts every test in a file, so a file that go test may
// skip on the runner's platform would make a slice look covered when it is
// not. None exists today; refuse any rather than evaluate constraints here.
func platformIndependent(path string, file *ast.File) error {
	// No platform is called "testshard", so a _GOOS or _GOARCH file name suffix
	// or a tag requirement excludes the file; "!tag" lines are caught below.
	ctx := build.Context{GOOS: "testshard", GOARCH: "testshard", Compiler: "gc"}
	if ok, err := ctx.MatchFile(filepath.Dir(path), filepath.Base(path)); err != nil || !ok {
		return fmt.Errorf("%s: cannot classify a platform-specific test file for the slice check", path)
	}
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if constraint.IsGoBuild(comment.Text) || constraint.IsPlusBuild(comment.Text) {
				return fmt.Errorf("%s: cannot classify a test file with a build constraint for the slice check", path)
			}
		}
	}
	return nil
}
