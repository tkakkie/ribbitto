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

// Moving the introduction path requires a maintainer decision.
const introducedBaselineFile = "tools/docscheck/baseline.go"

func gitEnvironment() []string {
	// Match tools/languagecheck/main.go: hooks export repository/index selectors.
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

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
	return checkPolicyBaseline(root, base, baselineFile, out)
}

func checkPolicyBaseline(root, base, baselinePath string, out io.Writer) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving repository root: %w", err)
	}
	// Resolve first so an unreadable revision cannot masquerade as an absent file.
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(gitEnvironment(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))
		result, err := cmd.CombinedOutput()
		if err != nil {
			detail := strings.TrimSpace(string(result))
			if failure, ok := err.(*exec.ExitError); ok && failure.ExitCode() == 1 && len(result) == 0 && args[0] == "merge-base" {
				err = fmt.Errorf("no common ancestor with origin/main")
			}
			if detail != "" {
				detail = ": " + detail
			}
			return nil, fmt.Errorf("reading comparison base %q: %w%s; fetch the needed revision from your remote (use `git fetch --unshallow <remote>` if history is too short), or choose an available revision with --base <rev> or DOCSCHECK_BASE=<rev> for make", base, err, detail)
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
	files, err := git("ls-tree", "--name-only", strings.TrimSpace(string(rev)), "--", introducedBaselineFile)
	if err != nil {
		return nil, err
	}
	if len(files) != 0 && baselinePath != introducedBaselineFile {
		return nil, fmt.Errorf("baseline moved or removed: comparison base %q has %s; the limits at the base are the reference", base, introducedBaselineFile)
	}
	if baselinePath != introducedBaselineFile {
		files, err = git("ls-tree", "--name-only", strings.TrimSpace(string(rev)), "--", baselinePath)
		if err != nil {
			return nil, err
		}
	}
	before := map[string]int{}
	if len(files) == 0 {
		if _, err := fmt.Fprintf(out, "docscheck: comparison base %q has no %s; introducing directory limits\n", base, baselinePath); err != nil {
			return nil, fmt.Errorf("writing baseline notice: %w", err)
		}
	} else {
		source, err := git("show", strings.TrimSpace(string(rev))+":"+baselinePath)
		if err != nil {
			return nil, err
		}
		before, err = readBaseline(source)
		if err != nil {
			return nil, fmt.Errorf("comparison base %q: %w", base, err)
		}
	}
	source, err := os.ReadFile(filepath.Join(root, baselinePath))
	if err != nil {
		return nil, fmt.Errorf("reading current baseline (baseline moved or removed; the limits at the base are the reference): %w", err)
	}
	limits, err := readBaseline(source)
	if err != nil {
		return nil, err
	}
	problems, err := checkDocuments(root, limits, before, documentAllowlist())
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

func checkDocuments(root string, limits, before map[string]int, allowed map[string]string) ([]string, error) {
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
	for dir, limit := range limits {
		total := totals[dir]
		if _, exists := before[dir]; !exists && limit > total {
			problems = append(problems, fmt.Sprintf("%s: new limit %d must equal its measured total of %d", dir, limit, total))
		}
		if total > limit {
			problems = append(problems, fmt.Sprintf("%s: %d characters, over its total limit of %d; delete narration and consolidate", dir, total, limits[dir]))
		}
	}
	sort.Strings(problems)
	return compact(problems), nil
}
