// Command contractliterals rejects literal DOM hooks in authored TypeScript and
// templ, excluding web/src/contracts.gen.ts and legacy web/static scripts.
// Dynamic ids recognize prefixes and the current families' 32-lowercase-hex
// suffixes. This text check is not a security boundary: aliasing, concatenation
// and names computed at runtime cannot be proved and remain for review.
// A templ-text apostrophe or TypeScript regex apostrophe can open a quoted token
// across lines and hide later hooks. Optional dataset access (el.dataset?.unreadCount),
// new window.CustomEvent("...") and nested template literals inside ${} are not
// understood; these lexical gaps also need review.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/tkakkie/ribbitto/tools/internal/domcontract"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: contractliterals <repository root>")
		os.Exit(2)
	}
	problems, err := check(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

func check(root string) ([]string, error) {
	source, err := os.ReadFile(filepath.Join(root, "internal/web/view/contracts.go"))
	if err != nil {
		return nil, fmt.Errorf("reading DOM definitions: %w", err)
	}
	_, definitions, err := domcontract.Parse(source)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, dir := range []string{"web/src", "internal/web/view"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if dir == "internal/web/view" && path != filepath.Join(root, dir) {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "web/src/contracts.gen.ts" || !(dir == "web/src" && filepath.Ext(path) == ".ts" || dir == "internal/web/view" && filepath.Ext(path) == ".templ") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, offset := range findings(string(data), filepath.Ext(path) == ".templ", definitions) {
				problems = append(problems, fmt.Sprintf("%s:%d: literal DOM contract; use its definition/accessor", rel, 1+strings.Count(string(data[:offset]), "\n")))
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", dir, err)
		}
	}
	return problems, nil
}

// Tokens retain offsets for diagnostics and keep comments and quoted prose from
// looking like calls. This is deliberately lexical, not a TypeScript/templ AST.
var tokens = regexp.MustCompile("(?s)<!--.*?-->|//[^\n]*|/\\*.*?\\*/|\"(?:\\\\.|[^\"\\\\])*\"|'(?:\\\\.|[^'\\\\])*'|`[^`]*`|[\\pL\\pN_$-]+|[^\\s]")
var hooks = regexp.MustCompile(`#[\pL\pN_-]+|\[\s*((?:data|aria)-[\w-]+)(?:\s|[~|^$*]?=|\])|\[\s*name\s*[~|^$*]?=\s*["']?([\w-]+)`)
var hexSuffix = regexp.MustCompile(`^[0-9a-f]{32}$`)

func findings(source string, template bool, definitions []domcontract.Definition) []int {
	var words []string
	var offsets []int
	for _, loc := range tokens.FindAllStringIndex(source, -1) {
		word := source[loc[0]:loc[1]]
		if strings.HasPrefix(word, "//") || strings.HasPrefix(word, "/*") || strings.HasPrefix(word, "<!--") {
			continue
		}
		words, offsets = append(words, word), append(offsets, loc[0])
	}
	var found []int
	inTag, braces := false, 0
	for i, word := range words {
		if template {
			switch word {
			case "<":
				inTag = inTag || braces == 0
			case ">":
				inTag = inTag && braces != 0
			case "{":
				if inTag {
					braces++
				}
			case "}":
				if inTag {
					braces--
				}
			}
			if !inTag {
				continue
			}
		}
		at := func(delta int) string {
			if j := i + delta; j >= 0 && j < len(words) {
				return words[j]
			}
			return ""
		}
		quoted := len(word) >= 2 && strings.ContainsAny(word[:1], "\"'`") && !strings.Contains(word, "${")
		value := word
		if quoted {
			value = word[1 : len(word)-1]
		}
		interpolated := strings.HasPrefix(word, "`") && strings.Contains(word, "${")
		selectorValue := value
		if interpolated {
			selectorValue = staticTemplateText(word[1 : len(word)-1])
		}
		for _, definition := range definitions {
			kind, name := definition.Parts[0], definition.Value
			id := func(value string) bool {
				// Current dynamic families use UUID hex; channel-create, for
				// example, shares channel-'s prefix but is a different id.
				return (kind == "id" || kind == "suffix") && value == name || kind == "suffix" && strings.HasPrefix(value, name) && hexSuffix.MatchString(strings.TrimPrefix(value, name))
			}
			selector := false
			for _, hook := range hooks.FindAllStringSubmatch(selectorValue, -1) {
				selector = selector || strings.HasPrefix(hook[0], "#") && id(hook[0][1:]) || kind == "attribute" && hook[1] == name || kind == "field" && hook[2] == name
			}
			bad := false
			if template {
				attribute := at(-1) == "=" && quoted
				key := at(-2)
				if at(-1) == "{" && at(-2) == "=" && at(1) == "}" {
					attribute, key = quoted, at(-3)
				}
				end := offsets[i] + len(word)
				boundary := end < len(source) && strings.ContainsAny(source[end:end+1], " \t\r\n>/=")
				bad = kind == "attribute" && word == name && (boundary || at(1) == "?")
				bad = bad || kind == "attribute" && quoted && value == name && at(-1) == "{" && at(1) == "}" && at(2) == "="
				if attribute && key == "id" {
					bad = id(value)
				}
				if attribute && key == "name" {
					bad = kind == "field" && value == name
				}
				if attribute && oneOf(key, "hx-target hx-select hx-select-oob hx-sync hx-trigger") {
					bad = selector || kind == "event" && eventToken(value, name)
				}
			} else {
				bad = (quoted || interpolated) && selector
				if quoted && at(-1) == "(" {
					switch at(-2) {
					case "getElementById":
						bad = bad || (kind == "suffix" || at(1) == ")" || at(1) == ",") && id(value)
					case "namedItem":
						bad = bad || kind == "field" && value == name
					case "getAttribute", "setAttribute", "hasAttribute", "removeAttribute", "toggleAttribute":
						bad = bad || kind == "attribute" && value == name
					case "addEventListener", "removeEventListener":
						bad = bad || kind == "event" && value == name
					case "Event", "CustomEvent":
						bad = bad || at(-3) == "new" && kind == "event" && value == name
					}
				}
				if kind == "attribute" && strings.HasPrefix(name, "data-") {
					key := datasetKey(name)
					bad = bad || at(-1) == "." && at(-2) == "dataset" && word == key
					bad = bad || quoted && at(-1) == "[" && at(-2) == "dataset" && at(1) == "]" && value == key
				}
				if kind == "field" {
					bad = bad || at(-1) == "." && oneOf(at(-2), "elements parameters") && word == name
					bad = bad || quoted && at(-1) == "[" && oneOf(at(-2), "elements parameters") && at(1) == "]" && value == name
					bad = bad || quoted && at(-1) == "<" && at(-2) == "Record" && value == name
				}
				bad = bad || quoted && kind == "event" && value == name && triggerArgument(words[:i])
			}
			if bad {
				found = append(found, offsets[i])
				break
			}
		}
	}
	return found
}

func staticTemplateText(value string) string {
	var text strings.Builder
	depth := 0
	for i := 0; i < len(value); i++ {
		if depth == 0 && strings.HasPrefix(value[i:], "${") {
			depth = 1
			// Keep static fragments apart so interpolation cannot create a hook.
			text.WriteByte(' ')
			i++
		} else if depth == 0 {
			text.WriteByte(value[i])
		} else if value[i] == '{' {
			depth++
		} else if value[i] == '}' {
			depth--
		}
	}
	return text.String()
}

func oneOf(word, choices string) bool {
	return slices.Contains(strings.Fields(choices), word)
}

func datasetKey(attribute string) string {
	parts := strings.Split(strings.TrimPrefix(attribute, "data-"), "-")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func eventToken(value, name string) bool {
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",[]", r)
	}) {
		if token == name {
			return true
		}
	}
	return false
}

func triggerArgument(words []string) bool {
	depth, commas := 0, 0
	for i := len(words) - 1; i >= 0; i-- {
		switch words[i] {
		case ")", "]", "}":
			depth++
		case "(", "[", "{":
			if depth == 0 {
				return words[i] == "(" && commas == 1 && i >= 3 && words[i-1] == "trigger" && words[i-2] == "." && words[i-3] == "htmx"
			}
			depth--
		case ",":
			if depth == 0 {
				commas++
			}
		}
	}
	return false
}
