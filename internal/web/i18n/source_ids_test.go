package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func collectSourceIDs(root string) ([]string, error) {
	fset := token.NewFileSet()
	var ids []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "T" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "i18n" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if id, err := strconv.Unquote(lit.Value); err == nil {
				ids = append(ids, id)
			}
			return true
		})
		return nil
	})
	return ids, err
}

func TestSourceIDs(t *testing.T) {
	c, err := New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	catalogues := map[string]int{}
	for _, lang := range []string{"en", "ja"} {
		file, err := c.bundle.LoadMessageFileFS(locales, "locales/"+lang+".toml")
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range file.Messages {
			if strings.TrimSpace(message.Other) != "" {
				catalogues[message.ID]++
			}
		}
	}
	ids, err := collectSourceIDs("..")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
		if catalogues[id] != 2 {
			t.Errorf("%s is not present in both catalogues", id)
		}
	}
	if len(seen) < 5000 {
		t.Fatalf("collected %d literal i18n.T IDs (%d call sites), want at least 5000", len(seen), len(ids))
	}
	for _, tt := range []struct{ name, path, code, want string }{
		{"literal call", "handler.go", `package web; func f() { _ = i18n.T(ctx, "hello.title") }`, "hello.title"},
		{"nested templ file", "view/hello_templ.go", `package view; func f() { _ = i18n.T(ctx, "hello.title") }`, "hello.title"},
		{"test file", "handler_test.go", `package web; func f() { _ = i18n.T(ctx, "skipped.test") }`, ""},
		{"testdata directory", "testdata/fixture.go", `package testdata; func f() { _ = i18n.T(ctx, "skipped.testdata") }`, ""},
		{"non-go file", "view/hello.templ", `package view; func f() { _ = i18n.T(ctx, "skipped.templ") }`, ""},
		{"concatenated id", "setup.go", `package web; func f(k string) { _ = i18n.T(ctx, "setup."+k) }`, ""},
		{"variable id", "setup.go", `package web; func f(k string) { _ = i18n.T(ctx, k) }`, ""},
		{"non-string literal", "setup.go", `package web; func f() { _ = i18n.T(ctx, 1) }`, ""},
		{"other package selector", "setup.go", `package web; func f() { _ = other.T(ctx, "skipped.pkg") }`, ""},
		{"other function selector", "setup.go", `package web; func f() { _ = i18n.Other(ctx, "skipped.func") }`, ""},
		{"wrong arg count", "setup.go", `package web; func f() { _ = i18n.T("skipped.arity") }`, ""},
	} {
		dir := t.TempDir()
		target := filepath.Join(dir, tt.path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(tt.code), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := collectSourceIDs(dir)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != tt.want {
			t.Errorf("%s: collectSourceIDs = %v, want %q", tt.name, got, tt.want)
		}
	}
}
