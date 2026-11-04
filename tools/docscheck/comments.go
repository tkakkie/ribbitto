package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func walkSourceComments(root string, visit func(string, line)) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// Only tracked files reach CI; local build leftovers must not change results.
	// Add new files with git add before checking them.
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	cmd.Env = append(gitEnvironment(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(root))
	files, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("listing source files: %w", err)
	}
	generated := regexp.MustCompile(`^(?s)(?:\s|//[^\n]*\n|/\*.*?\*/)*?// Code generated [^\n]* DO NOT EDIT\.\r?(?:\n|$)`)
files:
	for _, rel := range strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00") {
		// These tools contain intentionally unresolved fixture/example paths.
		for _, dir := range []string{"tools/docscheck", "tools/languagecheck", "tools/sourcecheck", "bin", "node_modules", ".claude", "web/static/vendor", "web/static/dist", "web/static/css"} {
			if strings.HasPrefix(rel, dir+"/") {
				continue files
			}
		}
		lineMarker, blockStart, blockEnd := commentMarkers(rel)
		if lineMarker == "" && blockStart == "" {
			continue
		}
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading source comments: %w", err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading source comments: %w", err)
		}
		if lineMarker == "//" && generated.Match(source) {
			continue
		}
		for _, l := range sourceComments(string(source), lineMarker, blockStart, blockEnd) {
			visit(rel, l)
		}
	}
	return nil
}

func commentMarkers(path string) (lineMarker, blockStart, blockEnd string) {
	if path == "Makefile" || filepath.Base(path) == ".gitattributes" {
		return "#", "", ""
	}
	switch filepath.Ext(path) {
	case ".go", ".templ", ".ts", ".js", ".mjs":
		return "//", "/*", "*/"
	case ".css":
		return "", "/*", "*/"
	case ".sql":
		return "--", "/*", "*/"
	case ".yml", ".yaml":
		return "#", "", ""
	}
	return "", "", ""
}

// Marker recognition is deliberately lexical: quoted strings (including raw
// backtick strings) are skipped, and block comments retain their source lines.
func sourceComments(source, lineMarker, blockStart, blockEnd string) []line {
	var out []line
	var quote byte
	block := false
	for n, text := range strings.Split(source, "\n") {
		for i := 0; i < len(text); {
			switch {
			case block:
				end := strings.Index(text[i:], blockEnd)
				if end < 0 {
					out = append(out, line{n + 1, text[i:]})
					i = len(text)
				} else {
					out = append(out, line{n + 1, text[i : i+end]})
					i += end + len(blockEnd)
					block = false
				}
			case quote != 0:
				if text[i] == '\\' && quote != '`' {
					i += 2
				} else {
					if text[i] == quote {
						quote = 0
					}
					i++
				}
			case lineMarker != "" && strings.HasPrefix(text[i:], lineMarker):
				out = append(out, line{n + 1, text[i+len(lineMarker):]})
				i = len(text)
			case blockStart != "" && strings.HasPrefix(text[i:], blockStart):
				i += len(blockStart)
				block = true
			case text[i] == '"' || text[i] == '\'' || text[i] == '`' && lineMarker == "//":
				quote = text[i]
				i++
			default:
				i++
			}
		}
		if quote != '`' {
			quote = 0
		}
	}
	return out
}

var commentPath = regexp.MustCompile("(?:^|[\\s`(\"'=:<\\[{*])(docs/[A-Za-z0-9_./-]+)(#[^\\s`\"')\\]}>*\\\\]+)?")
var commentAbbreviation = regexp.MustCompile(`(?:^|/)(?:[A-Za-z]\.){2,}$`)

// Markdown references use bareReferences and the shared heading resolver.
// The remaining paths use the same repositoryPathExists check as code spans.
func commentPaths(text string) []string {
	var paths []string
	for _, m := range commentPath.FindAllStringSubmatchIndex(text, -1) {
		written, rest := text[m[2]:m[3]], text[m[1]:]
		if m[2] > 0 && text[m[2]-1] == '*' && strings.HasPrefix(rest, "*") &&
			(len(rest) == 1 || strings.ContainsAny(rest[1:2], " \t)]}>") || len(rest) == 2 && strings.ContainsAny(rest[1:], ".,;:!?")) {
			rest = rest[1:]
		}
		// Patterns, placeholders, ellipses and abbreviations are prose, not paths.
		if strings.Contains(written, "...") || strings.HasPrefix(rest, "…") ||
			rest != "" && strings.ContainsAny(rest[:1], "*?[<{%") || commentAbbreviation.MatchString(written) {
			continue
		}
		paths = append(paths, strings.TrimSuffix(strings.TrimRight(text[m[2]:m[1]], ".,;:!?"), "/"))
	}
	return paths
}
