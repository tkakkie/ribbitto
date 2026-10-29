package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestAgyCheckRunnerNeverExecutesHook scans non-test agycheck*.go. It forbids
// os/exec imports with any alias; syscall Exec/ForkExec/StartProcess selectors
// with import aliases and syscall dot imports; direct agyCheckHook, run,
// os.StartProcess, exec.Command and exec.CommandContext calls; and direct
// agyCheckHookRecord/Claim calls outside agycheck_hook.go and agycheck_stub.go.
// It pins the runner argv and exactly two approved supervisor run/runResult
// calls. This syntactic guard excludes synthetic test agents and code outside
// that glob; it does not resolve indirect calls, local function aliases or
// interprocedural dispatch, and is not a security boundary. Only agy may launch
// hooks in a real run.
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
		violations, count := agyCheckRunnerGuard(t, path, source)
		for _, violation := range violations {
			t.Errorf("%s: %s", path, violation)
		}
		sites += count
	}
	if sites != 2 {
		t.Fatalf("runner process sites=%d want 2", sites)
	}
}

func agyCheckRunnerGuard(t *testing.T, path, source string) ([]string, int) {
	t.Helper()
	var violations []string
	if path == "agycheck_execute.go" && !strings.Contains(source, `args := []string{"-p", prompt, "--model", o.model, "--sandbox", "--print-timeout", printLimit.String(), "--output-format", "stream-json"}`) {
		violations = append(violations, "runner argv changed; hook dispatch is forbidden")
	}
	fs := token.NewFileSet()
	tree, err := parser.ParseFile(fs, path, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	syscalls := map[string]bool{"syscall": true}
	for _, imp := range tree.Imports {
		pkg, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if pkg == "os/exec" {
			violations = append(violations, "runner may not import os/exec")
		}
		if pkg == "syscall" && imp.Name != nil {
			syscalls[imp.Name.Name] = true
			if imp.Name.Name == "." {
				violations = append(violations, "runner may not dot-import syscall")
			}
		}
	}
	sites := 0
	ast.Inspect(tree, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && syscalls[pkg.Name] &&
				(sel.Sel.Name == "Exec" || sel.Sel.Name == "ForkExec" || sel.Sel.Name == "StartProcess") {
				violations = append(violations, "runner may not use syscall."+sel.Sel.Name)
			}
		}
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
			violations = append(violations, "runner may not launch hooks: "+fn)
		}
		if path != "agycheck_hook.go" && path != "agycheck_stub.go" && (fn == "agyCheckHookRecord" || fn == "agyCheckHookClaim") {
			violations = append(violations, "runner may not write hook records: "+fn)
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
				violations = append(violations, "unapproved process dispatch (hook forbidden): "+expression.String())
			}
			sites++
		}
		return true
	})
	return violations, sites
}

func TestAgyCheckRunnerGuardDetectors(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
	}{
		{"exec plain", `import "os/exec"`, "runner may not import os/exec"},
		{"exec alias", `import launch "os/exec"`, "runner may not import os/exec"},
		{"exec dot", `import . "os/exec"`, "runner may not import os/exec"},
		{"exec blank", `import _ "os/exec"`, "runner may not import os/exec"},
		{"syscall Exec", `import "syscall"; var f = syscall.Exec`, "runner may not use syscall.Exec"},
		{"syscall ForkExec", `import "syscall"; func f() { syscall.ForkExec() }`, "runner may not use syscall.ForkExec"},
		{"syscall StartProcess", `import "syscall"; func f() { syscall.StartProcess() }`, "runner may not use syscall.StartProcess"},
		{"syscall alias", `import launch "syscall"; var f = launch.Exec`, "runner may not use syscall.Exec"},
		{"syscall dot", `import . "syscall"; func f() { Exec() }`, "runner may not dot-import syscall"},
		{"record", `func f() { agyCheckHookRecord() }`, "runner may not write hook records: agyCheckHookRecord"},
		{"claim", `func f() { agyCheckHookClaim() }`, "runner may not write hook records: agyCheckHookClaim"},
		{"allowed", `import "syscall"; var flag = syscall.O_NOFOLLOW`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			violations, sites := agyCheckRunnerGuard(t, "agycheck_fixture.go", "package main\n"+tc.source)
			if sites != 0 || tc.want == "" && len(violations) != 0 || tc.want != "" && (len(violations) != 1 || violations[0] != tc.want) {
				t.Fatalf("guard: violations=%q sites=%d; want %q, 0", violations, sites, tc.want)
			}
		})
	}
}
