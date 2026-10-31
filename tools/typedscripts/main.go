// Command typedscripts keeps tracked client scripts typed or in a shrinking
// legacy list. Its regex and JSON checks are not parsers: computed imports,
// untyped re-exports and other semantic bypasses remain for review.
package main

import (
	"encoding/json"
	"errors"
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
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, exit.Stderr)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
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
		if name == listFile || name == "tsconfig.json" || slices.Contains([]string{".ts", ".tsx", ".mts", ".cts", ".templ", ".js", ".mjs", ".cjs", ".jsx"}, path.Ext(name)) {
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

var vendorScripts = map[string]bool{
	"web/static/vendor/htmx-2.0.7.min.js":          true,
	"web/static/vendor/htmx-ext-sse-2.2.4.min.js":  true,
	"web/static/vendor/idiomorph-ext-0.7.4.min.js": true,
}

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
			if vendorScripts[name] {
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
		case ".ts", ".tsx", ".mts", ".cts":
			if path.Ext(name) != ".ts" || !strings.HasPrefix(name, "web/src/") {
				problems = append(problems, name+": not type-checked")
			}
			if strings.Contains(source, "@ts-nocheck") {
				problems = append(problems, name+": @ts-nocheck")
			}
		case ".templ":
			for _, tag := range regexp.MustCompile(`(?is)<script(?:[\s/](?:[^>"']|"[^"]*"|'[^']*')*)?/?>`).FindAllString(source, -1) {
				hasSrc := false
				for _, attr := range regexp.MustCompile(`[\s/]+([^\s=/>]+)(?:\s*=\s*(?:"[^"]*"|'[^']*'|\{[^}]*\}|[^\s>]+))?`).FindAllStringSubmatch(tag, -1) {
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
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(files["tsconfig.json"]), &config); err != nil {
		problems = append(problems, "tsconfig.json: "+err.Error())
	} else if !validConfig(config) {
		problems = append(problems, "tsconfig.json: require exact typed include, strict and noEmit; strict-family overrides must be true; no exclude, files or extends; keys are case-sensitive")
	}
	return problems
}

func validConfig(config map[string]json.RawMessage) bool {
	var options map[string]json.RawMessage
	var include []string
	var strict, noEmit bool
	if json.Unmarshal(config["compilerOptions"], &options) != nil ||
		json.Unmarshal(config["include"], &include) != nil ||
		json.Unmarshal(options["strict"], &strict) != nil ||
		json.Unmarshal(options["noEmit"], &noEmit) != nil {
		return false
	}
	strictFamily := []string{"noImplicitAny", "strictNullChecks", "strictFunctionTypes", "strictBindCallApply", "strictPropertyInitialization", "noImplicitThis", "useUnknownInCatchVariables", "alwaysStrict", "strictBuiltinIteratorReturn"}
	if hasCaseVariant(options, strictFamily...) {
		return false
	}
	for _, name := range strictFamily {
		if value, ok := options[name]; ok {
			var enabled bool
			if json.Unmarshal(value, &enabled) != nil || !enabled {
				return false
			}
		}
	}
	return !hasCaseVariant(config, "include", "exclude", "files", "extends", "compilerOptions") &&
		!hasCaseVariant(options, "strict", "noEmit") &&
		config["exclude"] == nil && config["files"] == nil && config["extends"] == nil &&
		slices.Equal(include, []string{"web/src/**/*.ts"}) && strict && noEmit
}

func hasCaseVariant(fields map[string]json.RawMessage, names ...string) bool {
	for key := range fields {
		for _, name := range names {
			if key != name && strings.EqualFold(key, name) {
				return true
			}
		}
	}
	return false
}
