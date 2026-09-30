package static_test

import (
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestApplicationJavaScript(t *testing.T) {
	files := os.DirFS(".")
	names, err := fs.Glob(files, "*.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no application JavaScript found")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			source, err := fs.ReadFile(files, name)
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range checkJavaScript(name, string(source)) {
				t.Errorf("%s:%d: %s (see docs/architecture/web-layers.md#javascript-pattern-check)", name, finding.line, finding.rule)
			}
		})
	}
}

type javascriptFinding struct {
	line int
	rule string
}

// checkJavaScript checks recognizable source patterns, not JavaScript semantics.
// Keep its bounds and examples in sync with the web-layers documentation.
func checkJavaScript(name, source string) []javascriptFinding {
	var findings []javascriptFinding
	if !regexp.MustCompile(`^.+-v[0-9]+\.js$`).MatchString(name) {
		findings = append(findings, javascriptFinding{1, "versioned filename"})
	}
	assignment := `(?:=(?:[^=]|$)|(?:\+|-|\*\*?|/|%|<<|>>|>>>|&|\||\^|&&|\|\||\?\?)=)`
	rules := []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{"HTML generation", regexp.MustCompile(`\b(?:innerHTML|outerHTML)\s*` + assignment + `|\b(?:insertAdjacentHTML|createContextualFragment)\s*\(|\bdocument\s*\.\s*(?:write|writeln)\s*\(`)},
		{"requests", regexp.MustCompile(`\b(?:fetch|XMLHttpRequest)\b`)},
		{"IndexedDB", regexp.MustCompile(`\bindexedDB\b`)},
		{"code evaluation", regexp.MustCompile("\\beval\\s*\\(|\\bnew\\s+Function\\b|\\b(?:setTimeout|setInterval)\\s*\\(\\s*[\"'`]")},
		{"global write", regexp.MustCompile(`\b(?:window|globalThis)\s*\.\s*[A-Za-z_$][\w$]*\s*(?:` + assignment + `|\+\+|--)|(?:\+\+|--)\s*(?:window|globalThis)\s*\.\s*[A-Za-z_$][\w$]*`)},
	}
	// Delimiter depth bounds the declaration check without requiring an IIFE.
	tokens := regexp.MustCompile(`\b(?:var|let|const)\b|\bfunction\b\s*\*?\s*[A-Za-z_$][\w$]*|[{}()]`)
	braces, parentheses := 0, 0
	for i, line := range strings.Split(maskJavaScript(source), "\n") {
		for _, rule := range rules {
			if rule.pattern.MatchString(line) {
				findings = append(findings, javascriptFinding{i + 1, rule.name})
			}
		}
		for _, loc := range tokens.FindAllStringIndex(line, -1) {
			token := line[loc[0]:loc[1]]
			// A keyword after "." is a property name (options.var), not a
			// declaration. Delimiters still count: callback?.() opens one.
			keyword := token != "{" && token != "}" && token != "(" && token != ")"
			if before := strings.TrimRight(line[:loc[0]], " \t"); keyword && strings.HasSuffix(before, ".") {
				continue
			}
			switch token {
			case "{":
				braces++
			case "}":
				braces--
			case "(":
				parentheses++
			case ")":
				parentheses--
			default:
				if braces == 0 && parentheses == 0 {
					findings = append(findings, javascriptFinding{i + 1, "top-level declaration"})
				}
			}
		}
	}
	return findings
}

// Preserve positions and quote delimiters so diagnostics keep source line
// numbers and string timer arguments remain recognizable after masking.
func maskJavaScript(source string) string {
	masked := []byte(source)
	var state byte
	for i := 0; i < len(source); i++ {
		c := source[i]
		switch state {
		case '/':
			if c == '\n' {
				state = 0
			} else {
				masked[i] = ' '
			}
		case '*':
			if c != '\n' {
				masked[i] = ' '
			}
			if c == '*' && i+1 < len(source) && source[i+1] == '/' {
				i++
				masked[i] = ' '
				state = 0
			}
		case '\'', '"', '`':
			switch c {
			case state:
				state = 0
			case '\\':
				masked[i] = ' '
				if i+1 < len(source) {
					i++
					if source[i] != '\n' {
						masked[i] = ' '
					}
				}
			default:
				if c != '\n' {
					masked[i] = ' '
				}
			}
		default:
			switch c {
			case '\'', '"', '`':
				state = c
			case '/':
				if i+1 < len(source) && (source[i+1] == '/' || source[i+1] == '*') {
					i++
					state = source[i]
					masked[i-1], masked[i] = ' ', ' '
				}
			}
		}
	}
	return string(masked)
}

func TestJavaScriptPatterns(t *testing.T) {
	for _, tt := range []struct {
		name, source, rule string
	}{
		{"inner assignment", `node.innerHTML = markup;`, "HTML generation"},
		{"inner append", `node.innerHTML += markup;`, "HTML generation"},
		{"outer assignment", `node.outerHTML = markup;`, "HTML generation"},
		{"outer append", `node.outerHTML += markup;`, "HTML generation"},
		{"adjacent", `node.insertAdjacentHTML("beforeend", markup);`, "HTML generation"},
		{"write", `document.write(markup);`, "HTML generation"},
		{"writeln", `document.writeln(markup);`, "HTML generation"},
		{"fragment", `range.createContextualFragment(markup);`, "HTML generation"},
		{"fetch", `fetch(url);`, "requests"},
		{"qualified fetch", `window.fetch(url);`, "requests"},
		{"xhr", `new XMLHttpRequest();`, "requests"},
		{"indexed db", `indexedDB.open("state");`, "IndexedDB"},
		{"eval", `eval(code);`, "code evaluation"},
		{"function constructor", `new Function(code);`, "code evaluation"},
		{"timeout string", `setTimeout("run()", 1);`, "code evaluation"},
		{"interval string", `setInterval('run()', 1);`, "code evaluation"},
		{"timer template", "setTimeout(`run()`, 1);", "code evaluation"},
		{"window assignment", `window.example = value;`, "global write"},
		{"globalThis assignment", `globalThis.example = value;`, "global write"},
		{"global compound", `window.example += 1;`, "global write"},
		{"global logical", `globalThis.example ||= value;`, "global write"},
		{"global increment", `window.example++;`, "global write"},
		{"global prefix", `++globalThis.example;`, "global write"},
		{"var", `var example;`, "top-level declaration"},
		{"let", `let example;`, "top-level declaration"},
		{"const", `const example = 1;`, "top-level declaration"},
		{"function", `function example() {}`, "top-level declaration"},
		{"after iife", `(() => {})(); const example = 1;`, "top-level declaration"},
		{"after optional call", `callback?.(); const example = 1;`, "top-level declaration"},
		{"spaced write", `node /* comment */ . innerHTML /* comment */ += markup;`, "HTML generation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := checkJavaScript("example-v1.js", "/* heading\n */\n"+tt.source)
			if len(got) != 1 || got[0] != (javascriptFinding{3, tt.rule}) {
				t.Fatalf("got %v, want line 3: %s", got, tt.rule)
			}
			// Every prohibited pattern is harmless when mentioned as text.
			for _, text := range []string{
				"// " + tt.source,
				"/*\n" + strings.ReplaceAll(tt.source, "*/", "* /") + "\n*/",
				"void " + strconv.Quote(tt.source) + ";",
			} {
				if got := checkJavaScript("example-v1.js", text); len(got) != 0 {
					t.Errorf("text %q: %v", text, got)
				}
			}
		})
	}
}

func TestJavaScriptAllowedPatterns(t *testing.T) {
	for _, source := range []string{
		`use(node.innerHTML); use(node.outerHTML);`,
		`if (node.innerHTML === node.outerHTML) use(node);`,
		`if (node.innerHTML == value || node.outerHTML !== value) use(node);`,
		`node.textContent = text; node.append(child);`,
		`form.requestSubmit();`,
		`localStorage.setItem("panel-open", "yes"); sessionStorage.getItem("panel-open");`,
		`setTimeout(run, 1); setInterval(() => run(), 1);`,
		`setTimeout(function () { run(); }, 1);`,
		`use(window.example); if (globalThis.example === value) run();`,
		`(() => { const local = 1; function run() {} })(); (() => { let local; })();`,
		"document.addEventListener('click', function handler() {\nvar local;\n});",
		`{ let local; const other = 1; }`,
		`options.var = 1; use(x.let, obj.const); api . function (name);`,
		`void 'fetch("/"); indexedDB; eval(code);';`,
		"void `innerHTML = markup;\nwindow.example = 1;`;",
		`void "escaped \" quote; fetch(url);"; void 'escaped \' quote; eval(code);';`,
		"// { const fake\n/* }\nfunction fake() { */\n(() => { let local; })();",
	} {
		t.Run(source, func(t *testing.T) {
			if got := checkJavaScript("example-v23.js", source); len(got) != 0 {
				t.Fatalf("unexpected findings: %v", got)
			}
		})
	}
}

func TestJavaScriptVersionedNames(t *testing.T) {
	for _, name := range []string{"message-composer-v2.js", "new-feature-v123.js"} {
		if got := checkJavaScript(name, ""); len(got) != 0 {
			t.Errorf("%s: unexpected findings: %v", name, got)
		}
	}
	for _, name := range []string{"example.js", "example-v.js", "example-vx.js", "example-v1-extra.js"} {
		got := checkJavaScript(name, "")
		if len(got) != 1 || got[0].rule != "versioned filename" {
			t.Errorf("%s: got %v, want versioned filename finding", name, got)
		}
	}
}
