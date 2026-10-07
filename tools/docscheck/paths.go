package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
)

// checkPaths checks code spans in current documentation, including link labels.
// Decision records describe old layouts, so only this check exempts them.
func checkPaths(root string) ([]string, error) {
	files, err := markdownFiles(root)
	if err != nil {
		return nil, fmt.Errorf("listing Markdown files: %w", err)
	}
	repo, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("opening repository root: %w", err)
	}
	defer func() { _ = repo.Close() }()
	shape := regexp.MustCompile(`^(internal|cmd|db|docs|web|tools|scripts|\.github)/\S+$`)
	lineSuffix := regexp.MustCompile(`:[0-9]+$`)
	braceSet := regexp.MustCompile(`\{([^{}]+,[^{}]+)\}`)
	identifier := regexp.MustCompile(`^\p{Lu}[\pL\pN_]*$`)
	var problems []string
	for _, file := range files {
		if file != "AGENTS.md" && file != "README.md" && file != "DECISIONS.md" && !strings.HasPrefix(file, "docs/") {
			continue
		}
		if strings.HasPrefix(file, decisionsDir+"/") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			return nil, fmt.Errorf("reading document %s: %w", file, err)
		}
		d := parse(source)
		_ = ast.Walk(d.root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if _, ok := n.(*ast.CodeSpan); !ok || !entering {
				return ast.WalkContinue, nil
			}
			path := strings.TrimSpace(renderedText(n, source))
			if !shape.MatchString(path) || strings.ContainsAny(path, "<>*?[") || strings.Contains(path, "…") || strings.Contains(path, "...") {
				return ast.WalkSkipChildren, nil
			}
			path = strings.TrimSuffix(path, "/")
			path = lineSuffix.ReplaceAllString(path, "")
			path = strings.TrimSuffix(path, "/")
			for _, expanded := range expandPaths(path, braceSet) {
				if repositoryPathExists(repo, expanded, identifier) {
					continue
				}
				problems = append(problems, fmt.Sprintf("%s:%d: %s does not exist", file, d.lineOfNode(n), expanded))
			}
			return ast.WalkSkipChildren, nil
		})
	}
	sort.Strings(problems)
	return compact(problems), nil
}

func expandPaths(path string, braceSet *regexp.Regexp) []string {
	m := braceSet.FindStringSubmatchIndex(path)
	if m == nil {
		return []string{path}
	}
	var paths []string
	for _, choice := range strings.Split(path[m[2]:m[3]], ",") {
		paths = append(paths, expandPaths(path[:m[0]]+choice+path[m[1]:], braceSet)...)
	}
	return paths
}

func repositoryPathExists(repo *os.Root, path string, identifier *regexp.Regexp) bool {
	if !filepath.IsLocal(path) {
		return false
	}
	if _, err := repo.Stat(path); err == nil {
		return true
	}
	// Files with extensions take precedence over package selectors. Requiring
	// exported identifiers and Go files keeps missing files from passing as selectors.
	start := strings.LastIndexByte(path, '/') + 1
	dot := strings.IndexByte(path[start:], '.')
	if dot < 0 {
		return false
	}
	dot += start
	for _, name := range strings.Split(path[dot+1:], ".") {
		if !identifier.MatchString(name) {
			return false
		}
	}
	info, err := repo.Stat(path[:dot])
	if err != nil || !info.IsDir() {
		return false
	}
	matches, err := fs.Glob(repo.FS(), path[:dot]+"/*.go")
	return err == nil && len(matches) > 0
}
