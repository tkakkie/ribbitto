package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestVerifyLogReadImportBoundary(t *testing.T) {
	// Closed import set for every new production file, including capture/store
	// dependencies. It forbids os, exec, syscall, network and container adapters,
	// including aliased/dot imports; qualified calls cannot hide behind aliases.
	allowed := map[string]bool{"bytes": true, "crypto/rand": true, "encoding/hex": true, "encoding/json": true,
		"errors": true, "fmt": true, "net/url": true, "sort": true, "strconv": true, "strings": true, "sync": true, "unicode/utf8": true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "verifylog_") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		n++
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !allowed[path] || imp.Name != nil {
				t.Fatalf("unexpected read-path import in %s: %s", name, imp.Path.Value)
			}
		}
		// Existing package functions are accessible without imports. Check calls
		// too, permitting only the small shared pure API and verifyLog helpers.
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			pure := map[string]bool{"len": true, "cap": true, "min": true, "max": true, "make": true, "append": true, "copy": true, "string": true, "rune": true,
				"summaryText": true, "textCharge": true, "verifyObject": true, "parseVerifyRequest": true, "validVerifyID": true, "boolVerifyLog": true, "visit": true, "frame": true}
			if !pure[id.Name] && !strings.HasPrefix(id.Name, "verifyLog") && !strings.HasPrefix(id.Name, "newVerifyLog") && id.Name != "parseVerifyLogArguments" {
				t.Errorf("unexpected direct call in %s: %s", name, id.Name)
			}
			return true
		})
	}
	if n == 0 {
		t.Fatal("did not inspect reader files")
	}
}
