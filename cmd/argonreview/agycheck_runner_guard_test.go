package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guard every agy-check process creation site: only agy may launch hooks in a
// real run. Synthetic fake-agent subprocesses live in test files and are excluded.
func TestAgyCheckRunnerNeverExecutesHook(t *testing.T) {
	paths, err := filepath.Glob("agycheck*.go")
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		if path == "agycheck_execute.go" && !strings.Contains(source, `args := []string{"-p", prompt, "--model", o.model, "--sandbox", "--print-timeout", printLimit.String(), "--output-format", "stream-json"}`) {
			t.Fatal("runner argv changed; hook dispatch is forbidden")
		}
		fs := token.NewFileSet()
		tree, err := parser.ParseFile(fs, path, data, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name strings.Builder
			if err := format.Node(&name, fs, call.Fun); err != nil {
				t.Fatal(err)
			}
			fn := name.String()
			if fn == "agyCheckHook" || fn == "run" || fn == "os.StartProcess" || fn == "exec.Command" || fn == "exec.CommandContext" {
				t.Errorf("runner may not launch hooks: %s: %s", path, fn)
			}
			if strings.HasSuffix(fn, ".runResult") || strings.HasSuffix(fn, ".run") {
				var expression strings.Builder
				if err := format.Node(&expression, fs, call); err != nil {
					t.Fatal(err)
				}
				want := map[string]string{
					"agycheck_execute.go": `sup.runResult(o.agy, args, agyEnvironment([]string{"PATH=" + agyCheckRunPath}, m.Home, r.Tmp), r.Workspace, logs[0], logs[1])`,
					"agycheck_pin.go":     `sup.runResult(o.agy, []string{"--version"}, agyEnvironment(os.Environ(), o.home, tmp), tmp, out, out)`,
				}[path]
				if want == "" || expression.String() != want {
					t.Errorf("unapproved process dispatch (hook forbidden): %s: %s", path, expression.String())
				}
				sites++
			}
			return true
		})
	}
	if sites != 2 {
		t.Fatalf("runner process sites=%d want 2", sites)
	}
}
