package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const baselineFile = "tools/docscheck/baseline.go"

func readBaseline(source []byte) (map[string]int, error) {
	file, err := parser.ParseFile(token.NewFileSet(), baselineFile, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing baseline: %w", err)
	}
	var text string
	ast.Inspect(file, func(n ast.Node) bool {
		if v, ok := n.(*ast.ValueSpec); ok && len(v.Names) == 1 && v.Names[0].Name == "directoryBaseline" && len(v.Values) == 1 {
			if literal, ok := v.Values[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
				text, _ = strconv.Unquote(literal.Value)
			}
		}
		return true
	})
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) < 2 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(lines[0]) {
		return nil, fmt.Errorf("%s: want a base commit SHA and nonempty directory limits", baselineFile)
	}
	limits := map[string]int{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s: invalid directory limit %q", baselineFile, line)
		}
		dir := fields[0]
		limit, err := strconv.Atoi(fields[1])
		_, duplicate := limits[dir]
		if err != nil || limit < 0 || duplicate || !strings.HasPrefix(dir, "docs/") || !strings.HasSuffix(dir, "/") || filepath.ToSlash(filepath.Clean(dir))+"/" != dir {
			return nil, fmt.Errorf("%s: invalid directory limit %q", baselineFile, line)
		}
		limits[dir] = limit
	}
	return limits, nil
}

func checkPolicy(root, base string, out io.Writer) ([]string, error) {
	// Resolve first so an unreadable revision cannot masquerade as an absent file.
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		result, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("reading comparison base %q: %w: %s; fetch the needed revision from your remote (use `git fetch --unshallow <remote>` if history is too short), or choose an available revision with --base <rev> or DOCSCHECK_BASE=<rev> for make", base, err, strings.TrimSpace(string(result)))
		}
		return result, nil
	}
	if base == "" {
		base = "merge base of origin/main and HEAD"
		rev, err := git("merge-base", "origin/main", "HEAD")
		if err != nil {
			return nil, err
		}
		base = strings.TrimSpace(string(rev))
	}
	rev, err := git("rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return nil, err
	}
	files, err := git("ls-tree", "--name-only", strings.TrimSpace(string(rev)), "--", baselineFile)
	if err != nil {
		return nil, err
	}
	before := map[string]int{}
	if len(files) == 0 {
		if _, err := fmt.Fprintf(out, "docscheck: comparison base %q has no %s; introducing directory limits\n", base, baselineFile); err != nil {
			return nil, fmt.Errorf("writing baseline notice: %w", err)
		}
	} else {
		source, err := git("show", strings.TrimSpace(string(rev))+":"+baselineFile)
		if err != nil {
			return nil, err
		}
		before, err = readBaseline(source)
		if err != nil {
			return nil, fmt.Errorf("comparison base %q: %w", base, err)
		}
	}
	source, err := os.ReadFile(filepath.Join(root, baselineFile))
	if err != nil {
		return nil, fmt.Errorf("reading current baseline: %w", err)
	}
	limits, err := readBaseline(source)
	if err != nil {
		return nil, err
	}
	problems, err := checkDocuments(root, limits, documentAllowlist())
	for dir, previous := range before {
		if limit, ok := limits[dir]; !ok || limit > previous {
			problems = append(problems, fmt.Sprintf("%s: %s limit removed or raised above %d at %s", baselineFile, dir, previous, base))
		}
	}
	sort.Strings(problems)
	return problems, err
}

func policyExempt(path string) bool {
	return path == "docs/dependencies.md" || strings.HasPrefix(path, "docs/schema/") || strings.HasPrefix(path, decisionsDir+"/")
}

func checkDocuments(root string, limits map[string]int, allowed map[string]string) ([]string, error) {
	files, err := markdownFiles(root)
	if err != nil {
		return nil, fmt.Errorf("listing policy documents: %w", err)
	}
	totals, seen := map[string]int{}, map[string]bool{}
	var problems []string
	for _, file := range files {
		seen[file] = true
		if policyExempt(file) {
			continue
		}
		if strings.TrimSpace(allowed[file]) == "" {
			problems = append(problems, file+": needs an allow-list entry with a reason")
		}
		if !strings.HasPrefix(file, "docs/") {
			continue
		}
		// Subdirectories stay in their top-level bucket; docs/ itself is direct-only.
		dir := "docs/"
		if child, _, nested := strings.Cut(strings.TrimPrefix(file, dir), "/"); nested {
			dir += child + "/"
		}
		if _, ok := limits[dir]; !ok {
			problems = append(problems, dir+": Markdown directory is neither capped nor exempt")
			continue
		}
		source, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			return nil, fmt.Errorf("reading policy document %s: %w", file, err)
		}
		totals[dir] += utf8.RuneCount(source)
	}
	for file, reason := range allowed {
		if !seen[file] || strings.TrimSpace(reason) == "" {
			problems = append(problems, file+": stale allow-list entry or empty reason")
		}
	}
	for dir, total := range totals {
		if total > limits[dir] {
			problems = append(problems, fmt.Sprintf("%s: %d characters, over its total limit of %d; delete narration and consolidate", dir, total, limits[dir]))
		}
	}
	sort.Strings(problems)
	return compact(problems), nil
}
