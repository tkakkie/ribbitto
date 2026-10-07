// Command sourcecheck enforces docs/domain/vocabulary.md and docs/ui.md on
// repository sources. It checks mux patterns as authored, without resolving
// aliases or evaluating route expressions, and literal CSS values.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	problems, err := check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

func check(root string) ([]string, error) {
	source, err := os.ReadFile(filepath.Join(root, "docs/domain/vocabulary.md"))
	if err != nil {
		return nil, err
	}
	labels := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(string(source), "\n") {
		cells := strings.Split(line, "|")
		if len(cells) > 3 && strings.TrimSpace(cells[2]) == "UI label" {
			inTable = true
			continue
		}
		if inTable && len(cells) < 4 {
			break
		}
		if inTable && !strings.Contains(cells[2], "---") {
			labels[strings.ToLower(strings.TrimSpace(cells[2]))] = true
		}
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("docs/domain/vocabulary.md: missing UI label table")
	}
	var problems []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".claude" || d.Name() == "bin" || d.Name() == "node_modules" || d.Name() == "vendor" || rel == "docs" || rel == "internal/web/i18n/locales") {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		report := func(rule, value string) { problems = append(problems, fmt.Sprintf("%s: %s (%s)", rel, value, rule)) }
		vocabulary := func(name string) {
			if productWord(name, labels) {
				report("docs/domain/vocabulary.md", "product vocabulary in identifier or file name: "+name)
			}
		}
		vocabulary(d.Name())
		if !d.Type().IsRegular() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".sql" && ext != ".templ" && rel != "web/styles/app.css" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		switch ext {
		case ".go":
			tree, err := parser.ParseFile(token.NewFileSet(), path, data, 0)
			if err != nil {
				return err
			}
			ast.Inspect(tree, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					vocabulary(id.Name)
				}
				route := func(expr ast.Node) {
					ast.Inspect(expr, func(n ast.Node) bool {
						if s, ok := n.(*ast.BasicLit); ok && s.Kind == token.STRING {
							value, _ := strconv.Unquote(s.Value)
							for _, segment := range strings.Split(value, "/")[1:] {
								if !strings.HasPrefix(segment, "{") && productWord(segment, labels) {
									report("docs/domain/vocabulary.md", "product vocabulary in mux route: "+value)
								}
							}
						}
						return true
					})
				}
				if call, ok := n.(*ast.CallExpr); ok && len(call.Args) > 0 {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc" || sel.Sel.Name == "HandleFuncWithoutSession") {
						route(call.Args[0])
					}
				}
				// orgRoute's table supplies suffixes to the mux registration in org.go.
				if literal, ok := n.(*ast.CompositeLit); ok {
					if array, ok := literal.Type.(*ast.ArrayType); ok {
						if id, ok := array.Elt.(*ast.Ident); ok && id.Name == "orgRoute" {
							route(literal)
						}
					}
				}
				return true
			})
		case ".sql":
			sqlVocabulary(string(data), vocabulary)
		case ".templ":
			for _, attr := range attributes.FindAllStringSubmatch(string(data), -1) {
				reportColour := func(value string) { report("docs/ui.md", "raw colour in template: "+value) }
				if attr[1] == "class" {
					classColours(attr[2], reportColour)
				} else {
					cssColours(attr[2], reportColour)
				}
			}
		case ".css":
			cssColours(string(data), func(value string) { report("docs/ui.md", "raw colour outside token definitions: "+value) })
		}
		return nil
	})
	return problems, err
}

func productWord(name string, labels map[string]bool) bool {
	runes := []rune(name)
	var parts strings.Builder
	for i, r := range runes {
		if !unicode.IsLetter(r) {
			parts.WriteByte(' ')
			continue
		}
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
			parts.WriteByte(' ')
		}
		parts.WriteRune(unicode.ToLower(r))
	}
	for _, part := range strings.Fields(parts.String()) {
		for label := range labels {
			if part == label || part == label+"s" || part == label+"es" {
				return true
			}
		}
	}
	return false
}

// SQL comments and values are not identifiers; quoted identifiers still are.
var sqlTokens = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/|'(?:''|\\.|[^'])*'|\$[A-Za-z_0-9]*\$|"(?:""|[^"])*"|[A-Za-z_][A-Za-z_0-9]*`)

func sqlVocabulary(source string, visit func(string)) {
	for len(source) > 0 {
		loc := sqlTokens.FindStringIndex(source)
		if loc == nil {
			return
		}
		word := source[loc[0]:loc[1]]
		source = source[loc[1]:]
		switch {
		case strings.HasPrefix(word, "$"):
			if end := strings.Index(source, word); end >= 0 {
				source = source[end+len(word):]
			}
		case strings.HasPrefix(word, "--"), strings.HasPrefix(word, "/*"), strings.HasPrefix(word, "'"):
		default:
			visit(strings.Trim(word, `"`))
		}
	}
}

var attributes = regexp.MustCompile(`(?s)(?:^|\s)(class|style)\s*=\s*("[^"]*"|'[^']*'|\{.*?\})`)
var arbitrary = regexp.MustCompile(`\[[^\]]*\]`)
var comments = regexp.MustCompile(`(?s)/\*.*?\*/|"(?:\\.|[^"])*"|'(?:\\.|[^'])*'`)
var urls = regexp.MustCompile(`(?i)url\([^)]*\)`)
var references = regexp.MustCompile(`(?i)var\(\s*--[\w-]+\s*\)`)
var literalColour = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\s*\(`)
var words = regexp.MustCompile(`[A-Za-z]+`)

func rawColour(value string) bool {
	value = urls.ReplaceAllString(value, "")
	value = references.ReplaceAllString(value, "")
	value = comments.ReplaceAllString(value, "")
	if literalColour.MatchString(value) {
		return true
	}
	// CSS named colours; transparent and currentColor are deliberately absent.
	named := " aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue " +
		"chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki " +
		"darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue " +
		"dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow " +
		"grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan " +
		"lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen " +
		"linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream " +
		"mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred " +
		"papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown " +
		"seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle " +
		"tomato turquoise violet wheat white whitesmoke yellow yellowgreen "
	for _, loc := range words.FindAllStringIndex(value, -1) {
		word := value[loc[0]:loc[1]]
		if !strings.HasPrefix(strings.TrimSpace(value[loc[1]:]), "(") && strings.Contains(named, " "+strings.ToLower(word)+" ") {
			return true
		}
	}
	return false
}

func classColours(source string, report func(string)) {
	for _, value := range arbitrary.FindAllString(source, -1) {
		value = value[1 : len(value)-1]
		if _, suffix, ok := strings.Cut(value, ":"); ok {
			value = suffix
		}
		if rawColour(value) {
			report(value)
		}
	}
}

var apply = regexp.MustCompile(`@apply\s+([^;{}]+)`)
var declarations = regexp.MustCompile(`([{}])|([\w-]+)\s*:\s*([^;{}]+);?`)

func cssColours(source string, report func(string)) {
	source = comments.ReplaceAllStringFunc(source, func(value string) string {
		if strings.HasPrefix(value, "/*") {
			return ""
		}
		return value
	})
	for _, parameters := range apply.FindAllStringSubmatch(source, -1) {
		classColours(parameters[1], report)
	}
	var blocks []string
	last := 0
	for _, loc := range declarations.FindAllStringSubmatchIndex(source, -1) {
		if loc[2] >= 0 {
			if source[loc[2]:loc[3]] == "{" {
				header := source[last:loc[0]]
				header = header[strings.LastIndex(header, ";")+1:]
				blocks = append(blocks, strings.TrimSpace(header))
			} else if len(blocks) > 0 {
				blocks = blocks[:len(blocks)-1]
			}
		} else {
			property, value := source[loc[4]:loc[5]], source[loc[6]:loc[7]]
			block := ""
			if len(blocks) > 0 {
				block = blocks[len(blocks)-1]
			}
			tokenDefinition := strings.HasPrefix(property, "--") && (strings.HasPrefix(block, "@theme") || block == ":root" && strings.HasPrefix(property, "--rb-"))
			if !tokenDefinition && rawColour(value) {
				report(property + ": " + value)
			}
		}
		last = loc[1]
	}
}
