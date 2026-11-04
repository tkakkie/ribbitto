package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func walkSourceComments(root string, visit func(string, line)) error {
	generated := regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// These tools contain intentionally unresolved fixture/example paths.
			switch rel {
			case "tools/docscheck", "tools/languagecheck", "tools/sourcecheck", "web/static/dist", "web/static/css", "docs/schema":
				return filepath.SkipDir
			}
			switch d.Name() {
			case ".git", ".claude", "bin", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		lineMarker, blockStart, blockEnd := commentMarkers(rel)
		if !d.Type().IsRegular() || lineMarker == "" && blockStart == "" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if generated.Match(source) {
			return nil
		}
		for _, l := range sourceComments(string(source), lineMarker, blockStart, blockEnd) {
			visit(rel, l)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading source comments: %w", err)
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
			case text[i] == '"' || text[i] == '\'' || text[i] == '`':
				quote = text[i]
				i++
			default:
				i++
			}
		}
	}
	return out
}

var commentPath = regexp.MustCompile("(?:^|[\\s`(\"'=:])(docs/[A-Za-z0-9_./-]+)")

// Markdown references use bareReferences and the shared heading resolver.
// The remaining paths use the same repositoryPathExists check as code spans.
func commentPaths(text string) []string {
	var paths []string
	for _, m := range commentPath.FindAllStringSubmatch(text, -1) {
		path := strings.TrimRight(m[1], ".")
		if !strings.HasSuffix(path, ".md") {
			paths = append(paths, strings.TrimSuffix(path, "/"))
		}
	}
	return paths
}
