package web

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func feedBudgetReferences(name string, source []byte) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), name, source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing budget source: %w", err)
	}
	issue := regexp.MustCompile(`#[0-9]+`)
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.GenDecl)
		if !ok || (decl.Tok != token.CONST && decl.Tok != token.VAR) {
			return true
		}
		for _, spec := range decl.Specs {
			value := spec.(*ast.ValueSpec)
			comment := value.Doc.Text() + value.Comment.Text()
			if len(decl.Specs) == 1 {
				comment += decl.Doc.Text()
			}
			for _, ident := range value.Names {
				if strings.HasPrefix(ident.Name, "feedBudget") && !issue.MatchString(comment) {
					violations = append(violations, ident.Name+": budget comment needs an issue reference (#<digits>)")
				}
			}
		}
		return true
	})
	return violations, nil
}

func TestFeedGuardBudgetReferences(t *testing.T) {
	// Scan all Go files so moving a budget or changing const to var cannot evade it.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		violations, err := feedBudgetReferences(file, source)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range violations {
			t.Errorf("%s: %s", file, violation)
		}
	}
}

func TestFeedGuardBudgetReferenceChecker(t *testing.T) {
	for _, tt := range []struct {
		source string
		want   int
	}{
		{"package web\nconst feedBudgetMissing = 1", 1},
		{"package web\nvar feedBudgetMissing = 1", 1},
		{"package web\n// #1478: justified\nconst feedBudgetGood = 1", 0},
		{"package web\nconst (\n// #1478: justified\nfeedBudgetGood = 1\nfeedBudgetMissing = 2\n)", 1},
		{"package web\n// #1478: justified\nconst (\nfeedBudgetMissing = 1\nfeedBudgetAlsoMissing = 2\n)", 2},
		{"package web\nconst (\n// #1478: justified\nfeedBudgetGood = 1\nfeedBudgetAlsoGood = 2 // #1478: justified\n)", 0},
		{"package web\nvar feedBudgetGood = 1 // #1478: justified", 0},
	} {
		violations, err := feedBudgetReferences("memory.go", []byte(tt.source))
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) != tt.want {
			t.Errorf("%s: violations %v, want %d", tt.source, violations, tt.want)
		}
	}
}
