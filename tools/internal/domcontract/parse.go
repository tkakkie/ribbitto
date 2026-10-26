// Package domcontract parses attached DOM directives without importing the application.
package domcontract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// Definition is a constant and the fields of its attached dom directive.
type Definition struct {
	Name, Value string
	Parts       []string
}

// Parse reads the directive syntax shared by generation and literal checking.
func Parse(source []byte) (*ast.File, []Definition, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "contracts.go", source, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing DOM definitions: %w", err)
	}
	var definitions []Definition
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		if group.Lparen.IsValid() && group.Doc != nil {
			for _, comment := range group.Doc.List {
				if strings.HasPrefix(comment.Text, "//dom:") {
					return nil, nil, fmt.Errorf("DOM directive on const group: attach it to one constant")
				}
			}
		}
		for _, spec := range group.Specs {
			value := spec.(*ast.ValueSpec)
			doc := value.Doc
			if !group.Lparen.IsValid() {
				doc = group.Doc // A standalone const's own doc lives on its declaration.
			}
			var comments []*ast.Comment
			for _, attached := range []*ast.CommentGroup{doc, value.Comment} {
				if attached != nil {
					comments = append(comments, attached.List...)
				}
			}
			for _, comment := range comments {
				directive, ok := strings.CutPrefix(comment.Text, "//dom:")
				if !ok {
					continue
				}
				parts := strings.Fields(directive)
				if len(parts) == 0 || len(value.Names) != 1 || len(value.Values) != 1 {
					return nil, nil, fmt.Errorf("invalid DOM definition")
				}
				name := value.Names[0].Name
				literal, ok := value.Values[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return nil, nil, fmt.Errorf("%s must be a string literal", name)
				}
				text, err := strconv.Unquote(literal.Value)
				if err != nil {
					return nil, nil, fmt.Errorf("reading %s: %w", name, err)
				}
				definitions = append(definitions, Definition{Name: name, Value: text, Parts: parts})
			}
		}
	}
	return file, definitions, nil
}
