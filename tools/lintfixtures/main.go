// Command lintfixtures proves the lint configuration rejects what it must.
// The fixtures in internal/lintfixture, built only with the lintfixture tag,
// show that depguard rejects pgxbridge in a module root, accepts it in a
// store, and rejects web in compose sub-packages; staticcheck rejects ignored
// results even in generated templates; bodyclose rejects unclosed HTTP
// response bodies; sqlclosecheck rejects unused, unclosed pgx rows; nilerr
// rejects returning nil after checking a non-nil error. Plain lint never
// sees them.
// sqlclosecheck only catches rows that are never used: once rows are read,
// a missing Close passes lint, so keep defer rows.Close() after the query's
// error check. make check runs it with the pinned golangci-lint and the
// repository root.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Each expectation is the line the recipe it replaced grepped for: a file
// prefix and a pattern that must match on one line of golangci-lint's output.
var expectations = []struct {
	file, pattern, message string
}{
	{"lintfixture/internal/compose/sub/bad.go:", `internal/web.*is not allowed from list 'composition'.*\(depguard\)`, "depguard must reject web in a compose sub-package"},
	{"lintfixture/bad.go:", `SA4017:.*TrimSpace`, "staticcheck must reject an ignored result in bad.go"},
	{"lintfixture/bad_templ.go:", `SA4017:.*TrimSpace`, "staticcheck must reject an ignored result in bad_templ.go"},
	{"lintfixture/bodyclose.go:", `response body must be closed.*\(bodyclose\)`, "bodyclose must reject an unclosed HTTP response body"},
	{"lintfixture/sqlclosecheck.go:", `Rows/Stmt/NamedStmt was not closed \(sqlclosecheck\)`, "sqlclosecheck must reject unused, unclosed pgx rows"},
	{"lintfixture/nilerr.go:", `error is not nil \(line [0-9]*\) but it returns nil \(nilerr\)`, "nilerr must reject returning nil after a non-nil error"},
}

var rejectedRoot = regexp.MustCompile(`lintfixture/badroot/root\.go.*pgxbridge`)

func main() {
	root, lint, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: lintfixtures -lint <golangci-lint binary> <repository root>")
		os.Exit(2)
	}
	out, failed, err := run(root, lint)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lintfixtures:", err)
		os.Exit(1)
	}
	if err := evaluate(out, failed); err != nil {
		fmt.Println("lint-fixtures: " + err.Error())
		fmt.Println(out)
		os.Exit(1)
	}
}

func parse(args []string) (string, string, error) {
	flags := flag.NewFlagSet("lintfixtures", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	lint := flags.String("lint", "", "path to the pinned golangci-lint binary")
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if flags.NArg() != 1 || flags.Arg(0) == "" || *lint == "" {
		return "", "", errors.New("invalid arguments")
	}
	return flags.Arg(0), *lint, nil
}

// run returns golangci-lint's combined output and whether it reported
// issues. A binary that cannot start is an error, not a lint failure.
func run(root, lint string) (string, bool, error) {
	lint, err := filepath.Abs(lint)
	if err != nil {
		return "", false, fmt.Errorf("resolving golangci-lint: %w", err)
	}
	cmd := exec.Command(lint, "run", "--build-tags", "lintfixture", "./internal/lintfixture/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return "", false, fmt.Errorf("running golangci-lint: %w", err)
	}
	return strings.TrimSuffix(string(out), "\n"), err != nil, nil
}

// evaluate checks the output in the recipe's order and names the first
// expectation that does not hold.
func evaluate(out string, failed bool) error {
	lines := strings.Split(out, "\n")
	if !failed || !matches(lines, func(line string) bool { return rejectedRoot.MatchString(line) }) {
		return errors.New("depguard must reject pgxbridge in a module root")
	}
	if matches(lines, func(line string) bool { return strings.Contains(line, "lintfixture/internal/postgres/") }) {
		return errors.New("depguard must accept pgxbridge in a store")
	}
	for _, e := range expectations {
		pattern := regexp.MustCompile(e.pattern)
		if !matches(lines, func(line string) bool { return strings.Contains(line, e.file) && pattern.MatchString(line) }) {
			return errors.New(e.message)
		}
	}
	return nil
}

func matches(lines []string, match func(string) bool) bool {
	for _, line := range lines {
		if match(line) {
			return true
		}
	}
	return false
}
