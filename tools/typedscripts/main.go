// Command typedscripts keeps tracked client scripts typed or in a shrinking
// legacy list. Its regex and JSON checks are not parsers: computed imports,
// untyped re-exports and other semantic bypasses remain for review.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const listFile = "web/legacy-scripts.txt"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: typedscripts <repository root>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Getenv("GITHUB_ACTIONS") != "", os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

func run(root string, ci bool, out io.Writer) error {
	tracked, err := git(root, "ls-files", "-z")
	if err != nil {
		return err
	}
	files := map[string]string{}
	for _, name := range append(strings.Split(strings.TrimSuffix(tracked, "\x00"), "\x00"), listFile, "tsconfig.json") {
		if name == listFile || name == "tsconfig.json" || slices.Contains([]string{".ts", ".templ", ".js", ".mjs", ".cjs", ".jsx"}, path.Ext(name)) {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				return fmt.Errorf("reading %s: %w", name, err)
			}
			files[name] = string(data)
		}
	}
	problems := inspect(files)
	base, err := baseStems(root, ci)
	if err != nil {
		if ci {
			problems = append(problems, err.Error())
		} else if _, err := fmt.Fprintf(out, "NOT VERIFIED: legacy list may not grow: %v\n", err); err != nil {
			return fmt.Errorf("writing base warning: %w", err)
		}
	} else {
		problems = append(problems, additions(base, stems(files[listFile]))...)
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

func stems(text string) []string {
	var result []string
	for _, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func additions(base, current []string) []string {
	var problems []string
	for _, stem := range current {
		if !slices.Contains(base, stem) {
			problems = append(problems, listFile+": added legacy stem: "+stem)
		}
	}
	return problems
}

var legacy = regexp.MustCompile(`^web/static/([^/]+)-v[0-9]+\.js$`)

func baseStems(root string, ci bool) ([]string, error) {
	args := []string{"merge-base", "HEAD", "origin/main"}
	if ci {
		args = []string{"rev-parse", "--verify", "HEAD^1^{commit}"}
	}
	rev, err := git(root, args...)
	if err != nil {
		return nil, err
	}
	rev = strings.TrimSpace(rev)
	tree, err := git(root, "ls-tree", "-r", "-z", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	var base []string
	for _, name := range strings.Split(tree, "\x00") {
		if name == listFile {
			text, err := git(root, "show", rev+":"+listFile)
			return stems(text), err
		}
		if match := legacy.FindStringSubmatch(name); match != nil {
			base = append(base, match[1])
		}
	}
	return base, nil
}

func inspect(files map[string]string) []string {
	var problems []string
	listed, counts := stems(files[listFile]), map[string]int{}
	for name, source := range files {
		switch path.Ext(name) {
		case ".js", ".mjs", ".cjs", ".jsx":
			// Vendor is third-party code, pinned separately by vendor_test.go.
			if strings.HasPrefix(name, "web/static/vendor/") {
				continue
			}
			if match, _ := path.Match("web/test/*.test.mjs", name); match {
				continue
			}
			match := legacy.FindStringSubmatch(name)
			if match == nil || !slices.Contains(listed, match[1]) {
				problems = append(problems, name+": untyped client script")
			} else {
				counts[match[1]]++
			}
		case ".ts":
			for _, token := range regexp.MustCompile("(?s)\"(?:\\\\.|[^\"\\\\])*\"|'(?:\\\\.|[^'\\\\])*'|`(?:\\\\.|[^`\\\\])*`|//[^\n]*|/\\*.*?\\*/").FindAllString(source, -1) {
				if strings.HasPrefix(token, "//") || strings.HasPrefix(token, "/*") {
					if strings.Contains(token, "@ts-nocheck") {
						problems = append(problems, name+": @ts-nocheck comment")
					}
				}
			}
		case ".templ":
			for _, tag := range regexp.MustCompile(`(?is)<script(?:\s(?:[^>"']|"[^"]*"|'[^']*')*)?/?>`).FindAllString(source, -1) {
				hasSrc := false
				for _, attr := range regexp.MustCompile(`\s+([^\s=/>]+)(?:\s*=\s*(?:"[^"]*"|'[^']*'|\{[^}]*\}|[^\s>]+))?`).FindAllStringSubmatch(tag, -1) {
					hasSrc = hasSrc || strings.EqualFold(attr[1], "src")
				}
				if !hasSrc {
					problems = append(problems, name+": inline script")
				}
			}
			if regexp.MustCompile(`\bscript\s+\w+\s*\(`).MatchString(source) {
				problems = append(problems, name+": templ script component")
			}
		}
	}
	for i, stem := range listed {
		if slices.Contains(listed[:i], stem) {
			problems = append(problems, listFile+": duplicate stem: "+stem)
		} else if counts[stem] != 1 {
			problems = append(problems, fmt.Sprintf("%s: %s has %d script files; want exactly one", listFile, stem, counts[stem]))
		}
	}
	var config struct {
		Include         []string
		Exclude         json.RawMessage
		Files           json.RawMessage
		CompilerOptions struct{ Strict, NoEmit bool }
	}
	if err := json.Unmarshal([]byte(files["tsconfig.json"]), &config); err != nil {
		problems = append(problems, "tsconfig.json: "+err.Error())
	} else if config.Exclude != nil || config.Files != nil || !slices.Equal(config.Include, []string{"web/src/**/*.ts"}) || !config.CompilerOptions.Strict || !config.CompilerOptions.NoEmit {
		problems = append(problems, "tsconfig.json: require exact typed include, strict and noEmit; no exclude or files")
	}
	return problems
}
