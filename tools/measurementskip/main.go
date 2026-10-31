// Command measurementskip rejects new measurement-gated test skips and stale
// allowlist entries across repository tests. It preserves the existing literal
// AST scan; indirect or dynamically constructed skip messages need review.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: measurementskip <repository root>")
		os.Exit(2)
	}
	if err := check(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "measurementskip:", err)
		os.Exit(1)
	}
}

// Convenience for honest code, not a security control: fixtures and runtime
// messages can evade this scan, just as code can fabricate test2json events.
const skipScanExcludedVariables = "RIBBITTO_TEST_DATABASE_URL RIBBITTO_REQUIRE_DB RIBBITTO_REQUIRE_BROWSER RIBBITTO_BROWSER_SHARD RIBBITTO_BROWSER_COUNT_FILE"

func skipLiteral(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			value, _ := strconv.Unquote(e.Value)
			return value
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return skipLiteral(e.X) + skipLiteral(e.Y)
		}
	case *ast.ParenExpr:
		return skipLiteral(e.X)
	}
	return ""
}

// scanMeasurementSkips cannot discover gates passed through helpers such as
// t.Skip(reason). Review must check those indirect gates.
func scanMeasurementSkips(pkg, filename string, source any) ([]measurementSkip, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing skip sites: %w", err)
	}
	gate := regexp.MustCompile(`RIBBITTO_[A-Z0-9_]+=1`)
	var found []measurementSkip
	var scanErr error
	for _, declaration := range f.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Skip" && selector.Sel.Name != "Skipf" && selector.Sel.Name != "SkipNow" {
				return true
			}
			message := ""
			for _, arg := range call.Args {
				message += skipLiteral(arg)
				if selector.Sel.Name == "Skipf" {
					break
				}
			}
			for _, match := range gate.FindAllString(message, -1) {
				if !strings.Contains(" "+skipScanExcludedVariables+" ", " "+strings.TrimSuffix(match, "=1")+" ") {
					if selector.Sel.Name == "Skipf" && (strings.Contains(message, "%") || len(call.Args) > 1) {
						scanErr = fmt.Errorf("scanning %s/%s: gated measurement skips must use a constant message via t.Skip or a verb-free t.Skipf", pkg, fn.Name.Name)
						return false
					}
					found = append(found, measurementSkip{pkg, fn.Name.Name, message})
					break
				}
			}
			return true
		})
		if scanErr != nil {
			return nil, scanErr
		}
	}
	return found, nil
}

func compareMeasurementSkips(found, allowed []measurementSkip) error {
	sites, entries := map[measurementSkip]bool{}, map[measurementSkip]bool{}
	for _, site := range found {
		sites[site] = true
	}
	for _, entry := range allowed {
		entries[entry] = true
	}
	var problems []string
	for site := range sites {
		if !entries[site] {
			problems = append(problems, fmt.Sprintf("new gated skip %s/%s %q: update the measurement skip allowlist in review", site.pkg, site.test, site.message))
		}
	}
	for entry := range entries {
		if !sites[entry] {
			problems = append(problems, fmt.Sprintf("stale allowlist entry %s/%s %q", entry.pkg, entry.test, entry.message))
		}
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return fmt.Errorf("comparing measurement skip sites: %s", strings.Join(problems, "; "))
	}
	return nil
}

func check(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return fmt.Errorf("reading module: %w", err)
	}
	module := strings.Fields(string(data))[1]
	var found []measurementSkip
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walking skip sites: %w", walkErr)
		}
		if d.IsDir() {
			switch d.Name() {
			case "bin", ".git", "node_modules", "vendor", "third_party", "vendored":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("locating skip package: %w", err)
		}
		// Keep walking the separate tools module: its root-derived import path
		// would be wrong, so a gated skip there fails closed as a new site and
		// forces an allowlist review. There are none today.
		pkg := module
		if rel != "." {
			pkg += "/" + filepath.ToSlash(rel)
		}
		sites, err := scanMeasurementSkips(pkg, path, nil)
		found = append(found, sites...)
		return err
	})
	if err != nil {
		return err
	}
	return compareMeasurementSkips(found, measurementSkips())
}
