package static_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestApplicationJavaScript(t *testing.T) {
	files := os.DirFS("..")
	names, err := applicationSources(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			source, err := fs.ReadFile(files, name)
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range checkJavaScript(strings.TrimPrefix(strings.TrimPrefix(name, "static/"), "src/"), string(source)) {
				t.Errorf("%s:%d: %s (see checkJavaScript in web/static/javascript_test.go)", name, finding.line, finding.rule)
			}
		})
	}
}

func applicationSources(files fs.FS) ([]string, error) {
	names, err := fs.Glob(files, "static/*.js")
	if err != nil {
		return nil, err
	}
	if len(names) != 0 {
		return nil, fmt.Errorf("authored JavaScript outside web/src: %v", names)
	}
	typed, err := fs.Glob(files, "src/*.ts")
	if err != nil {
		return nil, err
	}
	if len(typed) == 0 {
		return nil, fmt.Errorf("no application TypeScript found")
	}
	return typed, nil
}

func TestApplicationSources(t *testing.T) {
	files := fstest.MapFS{
		"src/logic.ts": {}, "src/globals.d.ts": {},
		"static/vendor/lib.js": {}, "src/nested/logic.ts": {},
	}
	got, err := applicationSources(files)
	want := []string{"src/globals.d.ts", "src/logic.ts"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("sources = %v, %v; want %v", got, err, want)
	}
	for _, files := range []fstest.MapFS{{}, {"static/vendor/lib.js": {}, "static/dist/bundle.js": {}, "src/nested/logic.ts": {}}} {
		if _, err := applicationSources(files); err == nil || err.Error() != "no application TypeScript found" {
			t.Fatalf("expected no application TypeScript found, got %v", err)
		}
	}
	for _, name := range []string{"static/plain.js", "static/example-v2.js"} {
		files[name] = &fstest.MapFile{}
		if _, err := applicationSources(files); err == nil || !strings.Contains(err.Error(), "authored JavaScript outside web/src") {
			t.Fatalf("expected authored JavaScript rejection for %s, got %v", name, err)
		}
		delete(files, name)
	}
}

// Probe the actual language checker, including hypothetical nested sources:
// checking only today's tracked files would miss a future rule widening.
func TestApplicationSourcesLanguagecheckConsistency(t *testing.T) {
	checker := filepath.Join(t.TempDir(), "languagecheck")
	build := exec.Command("go", "build", "-o", checker, "./languagecheck")
	build.Dir = "../../tools"
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build languagecheck: %v: %s", err, out)
	}
	candidates := []string{"logic.ts", "globals.d.ts", "nested/logic.ts", "nested/deeper/logic.ts"}
	if err := fs.WalkDir(os.DirFS("../src"), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(name, ".ts") {
			candidates = append(candidates, name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range candidates {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, path := range []string{"docs/language-exceptions.txt", "web/src/logic.ts", "web/src/" + name} {
				full := filepath.Join(root, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{{"init", "--quiet"}, {"add", "."}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = root
				// Hook/index environment must not redirect the fixture to the worktree.
				for _, entry := range os.Environ() {
					if !strings.HasPrefix(entry, "GIT_") {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git fixture: %v: %s", err, out)
				}
			}
			out, err := exec.Command(checker, root).CombinedOutput()
			if err != nil {
				if !strings.Contains(string(out), `"web/src/`+name+`": path and kind are not in the language table`) {
					t.Fatalf("unexpected languagecheck failure: %v: %s", err, out)
				}
				return
			}
			names, err := applicationSources(os.DirFS(filepath.Join(root, "web")))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(names, "src/"+name) {
				t.Fatalf("languagecheck admits web/src/%s but applicationSources skips it", name)
			}
		})
	}
}

type javascriptFinding struct {
	line int
	rule string
}

// checkJavaScript checks recognizable source patterns, not JavaScript semantics.
// It is no parser or security boundary: aliases, computed properties and interpolations
// escape it; regex literals and delimiter depth can mislead it about scope. Reviewers must check HTML and indirect global
// writes, including block-level var and implicit globals, by hand.
func checkJavaScript(name, source string) []javascriptFinding {
	var findings []javascriptFinding
	typed := strings.HasSuffix(name, ".ts")
	if !typed && !regexp.MustCompile(`^.+-v[0-9]+\.js$`).MatchString(name) {
		findings = append(findings, javascriptFinding{1, "versioned filename"})
	}
	assignment := `(?:=(?:[^=]|$)|(?:\+|-|\*\*?|/|%|<<|>>|>>>|&|\||\^|&&|\|\||\?\?)=)`
	type rule struct {
		name    string
		pattern *regexp.Regexp
	}
	rules := []rule{
		{"HTML generation", regexp.MustCompile(`\b(?:innerHTML|outerHTML)\s*` + assignment + `|\b(?:insertAdjacentHTML|createContextualFragment)\s*\(|\bdocument\s*\.\s*(?:write|writeln)\s*\(`)},
		{"requests", regexp.MustCompile(`\b(?:fetch|XMLHttpRequest|sendBeacon|WebSocket|EventSource)\b`)},
		{"IndexedDB", regexp.MustCompile(`\bindexedDB\b`)},
		{"code evaluation", regexp.MustCompile("\\beval\\s*\\(|\\bnew\\s+Function\\b|\\b(?:setTimeout|setInterval)\\s*\\(\\s*[\"'`]")},
		{"global write", regexp.MustCompile(`\b(?:window|globalThis)\s*\.\s*[A-Za-z_$][\w$]*\s*(?:` + assignment + `|\+\+|--)|(?:\+\+|--)\s*(?:window|globalThis)\s*\.\s*[A-Za-z_$][\w$]*`)},
	}
	if typed {
		rules = append(rules, rule{"global cast", regexp.MustCompile(`\(\s*(?:window|globalThis)\s+as\b`)})
	}
	// Stage 1 pins delegate native submits to htmx using server-rendered URLs.
	if name != "pins-sync.ts" {
		rules = append(rules, rule{"requests", regexp.MustCompile(`\bhtmx\s*\.\s*ajax\b`)})
	}
	// Only the drafts module may keep data that outlives the tab (decision 37 (d)).
	if name != "message-drafts.ts" {
		rules = append(rules, rule{"localStorage", regexp.MustCompile(`\blocalStorage\b`)})
	}
	// Delimiter depth bounds the declaration check without requiring an IIFE.
	tokens := regexp.MustCompile(`\b(?:var|let|const)\b|\bfunction\b\s*\*?\s*[A-Za-z_$][\w$]*|[{}()]`)
	braces, parentheses := 0, 0
	masked := maskJavaScript(source)
	if name != "stream-progress-adapter.ts" {
		for _, position := range cursorWrites(source, masked) {
			findings = append(findings, javascriptFinding{strings.Count(source[:position], "\n") + 1, "Progress adapter owns cursor writes"})
		}
	}
	for i, line := range strings.Split(masked, "\n") {
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
				if !typed && braces == 0 && parentheses == 0 {
					findings = append(findings, javascriptFinding{i + 1, "top-level declaration"})
				}
			}
		}
	}
	return findings
}

// Like the other checks, this covers recognizable writes; aliases remain a review concern.
func cursorWrites(raw, masked string) []int {
	var positions []int
	contracts := regexp.MustCompile(`\b(?:writeAttribute|removeAttribute)\s*\([^;)]*?\b(?:AppliedCursorAttribute|EventCursorAttribute)\b`)
	for _, loc := range contracts.FindAllStringIndex(masked, -1) {
		positions = append(positions, loc[0])
	}
	quote := "[\"'`]"
	cursor := `(?:appliedCursor|eventCursor)`
	property := `\bdataset\s*(?:\.\s*` + cursor + `\b|\[\s*` + quote + cursor + quote + `\s*\])`
	assignment := `(?:=(?:[^=]|$)|(?:\+|-|\*|/|%|&&|\|\||\?\?)=|\+\+|--)`
	pattern := regexp.MustCompile(`\b(?:setAttribute|removeAttribute)\s*\(\s*` + quote +
		`(?:data-applied-cursor|data-event-cursor|sse-connect)` + quote +
		`|\bsearchParams\s*\.\s*(?:set|append|delete)\s*\(\s*` + quote + `after` + quote +
		`|` + property + `\s*` + assignment + `|\bdelete\s+\w+\s*\.\s*` + property)
	for _, loc := range pattern.FindAllStringIndex(raw, -1) {
		// Masking preserves offsets, so quoted examples and comments are not writes.
		if masked[loc[0]] == raw[loc[0]] {
			positions = append(positions, loc[0])
		}
	}
	return positions
}

func TestCursorWriter(t *testing.T) {
	for _, source := range []string{
		"dom.writeAttribute(\n el,\n dom.AppliedCursorAttribute, id);",
		"el.setAttribute(\n \"sse-connect\", u);",
		"el.dataset.appliedCursor\n = id;",
		"el.setAttribute(`data-applied-cursor`, id);",
		"el.removeAttribute(`sse-connect`);",
		`el.dataset["appliedCursor"] = id;`,
		"u.searchParams.set(\n 'after', id);",
		`dom.writeAttribute(node, dom.AppliedCursorAttribute, id);`,
		`writeAttribute(node, EventCursorAttribute, id);`,
		`node.dataset.appliedCursor = id;`,
		`node.dataset.eventCursor = id;`,
		`node.dataset.eventCursor += id;`,
		`delete node.dataset.appliedCursor;`,
		`node.setAttribute("data-applied-cursor", id);`,
		`node.removeAttribute('data-event-cursor');`,
		`node.setAttribute("sse-connect", url);`,
		`url.searchParams.set("after", id);`,
		`url.searchParams.delete('after');`,
	} {
		t.Run(source, func(t *testing.T) {
			for _, name := range []string{"message-stream.ts", "other.ts", "stream-progress-adapter.ts.bak.ts"} {
				got := checkJavaScript(name, source)
				if len(got) != 1 || got[0].rule != "Progress adapter owns cursor writes" {
					t.Fatalf("%s: got %v, want cursor ownership finding", name, got)
				}
			}
			if got := checkJavaScript("other.ts", "\n"+source); len(got) != 1 || got[0].line != 2 {
				t.Fatalf("multiline diagnostic: got %v, want line 2", got)
			}
			for _, text := range []string{"// " + strings.ReplaceAll(source, "\n", "\n// "), "void " + strconv.Quote(source), source} {
				name := "other.ts"
				if text == source {
					name = "stream-progress-adapter.ts"
				}
				if got := checkJavaScript(name, text); len(got) != 0 {
					t.Fatalf("%s: unexpected findings %v", name, got)
				}
			}
		})
	}
	if got := checkJavaScript("other.ts", `dom.readAttribute(node, dom.AppliedCursorAttribute); node.getAttribute("sse-connect"); url.searchParams.get("after"); node.dataset["appliedCursor"] === id; node.dataset.eventCursor == id; dom.readAttribute(
 node, dom.EventCursorAttribute); node.getAttribute("data-applied-cursor"); url.searchParams.get(
 "after");`); len(got) != 0 {
		t.Fatalf("cursor readers: %v", got)
	}
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
		{"htmx ajax", `htmx.ajax("POST", url, target);`, "requests"},
		{"beacon", `navigator.sendBeacon(url, data);`, "requests"},
		{"websocket", `new WebSocket(url);`, "requests"},
		{"event source", `new EventSource(url);`, "requests"},
		{"indexed db", `indexedDB.open("state");`, "IndexedDB"},
		{"local storage", `localStorage.setItem("panel-open", "yes");`, "localStorage"},
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
			if tt.rule != "top-level declaration" {
				got := checkJavaScript("example.ts", "/* heading\n */\nconst value: number = 1;\n"+tt.source)
				if len(got) != 1 || got[0] != (javascriptFinding{4, tt.rule}) {
					t.Fatalf("TypeScript got %v, want line 4: %s", got, tt.rule)
				}
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
				if got := checkJavaScript("example.ts", text); len(got) != 0 {
					t.Errorf("TypeScript text %q: %v", text, got)
				}
			}
		})
	}
}

func TestJavaScriptAllowedPatterns(t *testing.T) {
	if got := checkJavaScript("pins-sync.ts", `window.htmx.ajax("POST", button.getAttribute("formaction"), {source: button, target: form, swap: "outerHTML"});`); len(got) != 0 {
		t.Errorf("delegated pins request: %v", got)
	}
	for _, name := range []string{"pins-sync-v0.js", "pins-sync-v2.js", "other-pins-sync-v2.js", "pins-sync-v2.js.bak-v1.js", "pins-sync-v3.js", "other-pins-sync.ts", "pins-sync.ts.bak.ts", "pins-sync-v2.ts", "pins-seq.ts"} {
		if got := checkJavaScript(name, `htmx.ajax("POST", url);`); len(got) != 1 || got[0].rule != "requests" {
			t.Errorf("%s: got %v, want one requests finding", name, got)
		}
	}
	for _, source := range []string{
		`use(node.innerHTML); use(node.outerHTML);`,
		`if (node.innerHTML === node.outerHTML) use(node);`,
		`if (node.innerHTML == value || node.outerHTML !== value) use(node);`,
		`node.textContent = text; node.append(child);`,
		`form.requestSubmit();`,
		`sessionStorage.setItem("panel-open", "yes"); sessionStorage.getItem("panel-open");`,
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
	// localStorage is allowed only in the drafts module, by its exact name.
	drafts := `window.localStorage.setItem(key, text); localStorage.removeItem(key);`
	if got := checkJavaScript("message-drafts.ts", drafts); len(got) != 0 {
		t.Errorf("message-drafts.ts: unexpected findings: %v", got)
	}
	for _, name := range []string{"message-drafts-v12.js", "old-message-drafts-v1.js", "message-drafts-v1.js.bak-v1.js"} {
		if got := checkJavaScript(name, drafts); len(got) != 1 || got[0].rule != "localStorage" {
			t.Errorf("%s: got %v, want one localStorage finding", name, got)
		}
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

func TestTypeScriptPatterns(t *testing.T) {
	for _, source := range []string{
		`export const value: number = 1; let next: number = value; export function read(): number { return next; }`,
		`function identity<T>(value: T): T { return value; } const value = identity<number>(1);`,
		`const value = { mode: "ready" as const } satisfies { mode: string };`,
		`import type { Item } from "./item.ts"; const value: Item | undefined = undefined;`,
		`interface Item { value: number } const value: Item = { value: 1 };`,
		`declare const external: number; const value: number = external;`,
		`type State = "fetch" | "innerHTML =" | "window.x++"; const value: State = "fetch";`,
	} {
		t.Run(source, func(t *testing.T) {
			if got := checkJavaScript("logic.ts", source); len(got) != 0 {
				t.Fatalf("allowed TypeScript: %v", got)
			}
			if got := checkJavaScript("logic.ts", source+"\nwindow.x = value;"); len(got) != 1 || got[0] != (javascriptFinding{2, "global write"}) {
				t.Fatalf("TypeScript global write: %v", got)
			}
		})
	}
	for _, source := range []string{`(window as Window).x = 1;`, `(globalThis as any).x++;`} {
		if got := checkJavaScript("logic.ts", source); len(got) != 1 || got[0].rule != "global cast" {
			t.Errorf("cast %s: %v", source, got)
		}
	}
	for _, name := range []string{"logic.ts", "globals.d.ts", "drafts.ts", "message-drafts-v12.ts"} {
		if got := checkJavaScript(name, `localStorage.setItem("draft", "text");`); len(got) != 1 || got[0].rule != "localStorage" {
			t.Errorf("storage %s: %v", name, got)
		}
	}
}
