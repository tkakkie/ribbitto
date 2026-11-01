package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const config = `{"compilerOptions":{"strict":true,"noEmit":true},"include":["web/src/**/*.ts"]}`

func fixture() map[string]string {
	return map[string]string{listFile: "example\n", "tsconfig.json": config, "web/static/example-v1.js": ""}
}

func TestRules(t *testing.T) {
	for _, tc := range []struct{ name, file, good, bad, want string }{
		{"duplicate", listFile, "example\n", "example\nexample\n", "duplicate"},
		{"stale", "web/static/example-v1.js", "", "", "0 script files"},
		{"exclude", "tsconfig.json", config, strings.Replace(config, `"include"`, `"exclude":[],"include"`, 1), "exact typed include"},
		{"files", "tsconfig.json", config, strings.Replace(config, `"include"`, `"files":[],"include"`, 1), "exact typed include"},
		{"extends", "tsconfig.json", config, strings.Replace(config, `"include"`, `"extends":"./base.json","include"`, 1), "exact typed include"},
		{"case shadow include", "tsconfig.json", config, strings.Replace(config, `"include":["web/src/**/*.ts"]`, `"include":["web/src/one.ts"],"Include":["web/src/**/*.ts"]`, 1), "exact typed include"},
		{"case shadow exact include", "tsconfig.json", config, strings.Replace(config, `"include"`, `"Include":["web/src/**/*.ts"],"include"`, 1), "exact typed include"},
		{"case include", "tsconfig.json", config, strings.Replace(config, `"include"`, `"Include"`, 1), "exact typed include"},
		{"case exclude", "tsconfig.json", config, strings.Replace(config, `"include"`, `"Exclude":[],"include"`, 1), "exact typed include"},
		{"case files", "tsconfig.json", config, strings.Replace(config, `"include"`, `"Files":[],"include"`, 1), "exact typed include"},
		{"case strict", "tsconfig.json", config, strings.Replace(config, `"strict"`, `"Strict"`, 1), "exact typed include"},
		{"case noEmit", "tsconfig.json", config, strings.Replace(config, `"noEmit"`, `"NoEmit"`, 1), "exact typed include"},
		{"case shadow strict", "tsconfig.json", config, strings.Replace(config, `"strict"`, `"Strict":true,"strict"`, 1), "exact typed include"},
		{"case shadow noEmit", "tsconfig.json", config, strings.Replace(config, `"noEmit"`, `"NoEmit":true,"noEmit"`, 1), "exact typed include"},
		{"missing include", "tsconfig.json", config, strings.Replace(config, `,"include":["web/src/**/*.ts"]`, "", 1), "exact typed include"},
		{"narrow include", "tsconfig.json", config, strings.Replace(config, "**/*.ts", "one.ts", 1), "exact typed include"},
		{"wide include", "tsconfig.json", config, strings.Replace(config, "web/src/**/*.ts", "**/*.ts", 1), "exact typed include"},
		{"strict false", "tsconfig.json", config, strings.Replace(config, `"strict":true`, `"strict":false`, 1), "exact typed include"},
		{"strict missing", "tsconfig.json", config, strings.Replace(config, `"strict":true,`, "", 1), "exact typed include"},
		{"noEmit false", "tsconfig.json", config, strings.Replace(config, `"noEmit":true`, `"noEmit":false`, 1), "exact typed include"},
		{"noEmit missing", "tsconfig.json", config, strings.Replace(config, `,"noEmit":true`, "", 1), "exact typed include"},
		{"line directive", "web/src/one.ts", "// checked\n", "// @ts-nocheck\n", "@ts-nocheck"},
		{"generated block directive", "web/src/contracts.gen.ts", "/* checked\n */", "/* @ts-nocheck\n */", "@ts-nocheck"},
		{"outside typed input", "web/src/nested/other.ts", "// checked", "// @ts-nocheck", "@ts-nocheck"},
		{"regex literal directive", "web/src/one.ts", `const q = /"/g; // checked`, `const q = /"/g; // @ts-nocheck`, "@ts-nocheck"},
		{"inline", "other.templ", `<script src="/app.js"></script>`, `<script nonce={ nonce }></script>`, "inline script"},
		{"slash separator", "other.templ", `<script/src="/app.js"></script>`, `<script/nonce={ nonce }></script>`, "inline script"},
		{"slash between attributes", "other.templ", `<script nonce={ nonce }/src="/app.js"></script>`, `<script nonce={ nonce }/title="app"></script>`, "inline script"},
		{"mixed case inline", "other.templ", `<ScRiPt SRC="/app.js"></ScRiPt>`, `<ScRiPt></ScRiPt>`, "inline script"},
		{"data src", "other.templ", `<script src="/app.js">`, `<script data-src="/app.js">`, "inline script"},
		{"src in value", "other.templ", `<script src="/app.js">`, `<script title=" src='/app.js'">`, "inline script"},
		{"component", "other.templ", "templ example() {}", "script example() {}", "templ script component"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fixture()
			files[tc.file] = tc.good
			if got := inspect(files); len(got) != 0 {
				t.Fatalf("passing twin: %v", got)
			}
			files[tc.file] = tc.bad
			if tc.name == "stale" {
				delete(files, tc.file)
			}
			if got := inspect(files); len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("negative twin: %v; want one %q", got, tc.want)
			}
		})
	}
}

func TestNoCheck(t *testing.T) {
	for _, tc := range []struct{ name, good, bad string }{
		{"false vs true", `"noCheck":false,`, `"noCheck":true,`},
		{"absent vs true", "", `"noCheck":true,`},
		{"case variant", `"noCheck":false,`, `"NoCheck":false,`},
		{"uppercase variant", `"noCheck":false,`, `"NOCHECK":false,`},
		{"case shadow", `"noCheck":false,`, `"noCheck":false,"NoCheck":false,`},
		{"uppercase shadow", `"noCheck":false,`, `"noCheck":false,"NOCHECK":false,`},
		{"string", `"noCheck":false,`, `"noCheck":"true",`},
		{"number", `"noCheck":false,`, `"noCheck":1,`},
		{"null", `"noCheck":false,`, `"noCheck":null,`},
		{"object", `"noCheck":false,`, `"noCheck":{},`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, twin := range []struct {
				name, options string
				negative      bool
			}{
				{"passing twin", tc.good, false},
				{"negative twin", tc.bad, true},
			} {
				t.Run(twin.name, func(t *testing.T) {
					files := fixture()
					files["tsconfig.json"] = strings.Replace(config, `"strict":true`, twin.options+`"strict":true`, 1)
					got := inspect(files)
					if twin.negative {
						if len(got) != 1 || !strings.Contains(got[0], "exact typed include") {
							t.Fatalf("negative twin: %v; want one exact typed include", got)
						}
					} else if len(got) != 0 {
						t.Fatalf("passing twin: %v", got)
					}
				})
			}
		})
	}
}

func TestStrictFamily(t *testing.T) {
	for _, name := range []string{"noImplicitAny", "strictNullChecks", "strictFunctionTypes", "strictBindCallApply", "strictPropertyInitialization", "noImplicitThis", "useUnknownInCatchVariables", "alwaysStrict", "strictBuiltinIteratorReturn"} {
		t.Run(name, func(t *testing.T) {
			files := fixture()
			files["tsconfig.json"] = strings.Replace(config, `"strict":true`, `"strict":true,"`+name+`":true`, 1)
			if got := inspect(files); len(got) != 0 {
				t.Fatalf("passing twin: %v", got)
			}
			files["tsconfig.json"] = strings.Replace(files["tsconfig.json"], `"`+name+`":true`, `"`+name+`":false`, 1)
			if got := inspect(files); len(got) != 1 || !strings.Contains(got[0], "exact typed include") {
				t.Fatalf("negative twin: %v; want one exact typed include", got)
			}
		})
	}
	for _, tc := range []struct{ name, good, bad string }{
		{"case variant", config, strings.Replace(config, `"strict":true`, `"strict":true,"NoImplicitAny":true`, 1)},
		{"non-boolean", strings.Replace(config, `"strict":true`, `"strict":true,"noImplicitAny":true`, 1), strings.Replace(config, `"strict":true`, `"strict":true,"noImplicitAny":"true"`, 1)},
		{"null", strings.Replace(config, `"strict":true`, `"strict":true,"noImplicitAny":true`, 1), strings.Replace(config, `"strict":true`, `"strict":true,"noImplicitAny":null`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fixture()
			files["tsconfig.json"] = tc.good
			if got := inspect(files); len(got) != 0 {
				t.Fatalf("passing twin: %v", got)
			}
			files["tsconfig.json"] = tc.bad
			if got := inspect(files); len(got) != 1 || !strings.Contains(got[0], "exact typed include") {
				t.Fatalf("negative twin: %v; want one exact typed include", got)
			}
		})
	}
}

func TestTypeScriptDirectives(t *testing.T) {
	for _, name := range []string{"web/src/one.tsx", "web/src/one.mts", "web/src/one.cts", "other/one.ts", "other/one.d.ts"} {
		t.Run(name, func(t *testing.T) {
			files := fixture()
			files[name] = ""
			if got := inspect(files); len(got) != 1 || got[0] != name+": not type-checked" {
				t.Fatalf("path finding: %v", got)
			}
			files[name] = "// @ts-nocheck"
			got := inspect(files)
			if len(got) != 2 || !slices.Contains(got, name+": not type-checked") || !slices.Contains(got, name+": @ts-nocheck") {
				t.Fatalf("path and directive findings: %v", got)
			}
		})
	}
}

func TestTrackedTypeScriptPaths(t *testing.T) {
	for _, tc := range []struct{ good, bad string }{
		{"web/src/one.ts", "web/src/one.tsx"},
		{"web/src/one.ts", "web/src/one.mts"},
		{"web/src/one.ts", "web/src/one.cts"},
		{"web/src/one.ts", "other/one.ts"},
		{"web/src/one.d.ts", "other/one.d.ts"},
		{"web/src/nested/one.ts", "other/nested/one.ts"},
	} {
		t.Run(tc.bad, func(t *testing.T) {
			root := t.TempDir()
			g := func(args ...string) {
				t.Helper()
				if _, err := git(root, args...); err != nil {
					t.Fatal(err)
				}
			}
			g("init", "-q")
			for name, text := range fixture() {
				write(t, root, name, text)
			}
			g("add", ".")
			write(t, root, tc.good, "")
			write(t, root, tc.bad, "")
			g("add", "--", tc.good)
			if err := run(root, false, &bytes.Buffer{}); err != nil {
				t.Fatalf("passing twin: %v", err)
			}
			g("rm", "--cached", "--", tc.good)
			g("add", "--", tc.bad)
			if err := run(root, false, &bytes.Buffer{}); err == nil || err.Error() != tc.bad+": not type-checked" {
				t.Fatalf("negative twin: %v; want one not type-checked", err)
			}
		})
	}
}

func TestScriptPaths(t *testing.T) {
	for _, name := range []string{"web/static/new-v1.js", "elsewhere.js", "web/src/one.mjs", "elsewhere.cjs", "elsewhere.jsx", "web/test/nested/one.test.mjs", "web/test/one.mjs", "web/static/nested/example-v1.js", "web/static/example-v2.js", "web/static/vendor/x.js"} {
		t.Run(name, func(t *testing.T) {
			files := fixture()
			if got := inspect(files); len(got) != 0 {
				t.Fatal(got)
			}
			files[name] = ""
			want := "untyped client script"
			if name == "web/static/example-v2.js" {
				want = "2 script files"
			}
			if got := inspect(files); len(got) != 1 || !strings.Contains(got[0], want) {
				t.Fatalf("extra script: %v", got)
			}
		})
	}
	files := fixture()
	delete(files, "web/static/example-v1.js")
	files[listFile] = "# all converted\n"
	files["web/static/vendor/htmx-2.0.7.min.js"] = ""
	files["web/static/vendor/htmx-ext-sse-2.2.4.min.js"] = ""
	files["web/static/vendor/idiomorph-ext-0.7.4.min.js"] = ""
	files["web/test/example.test.mjs"] = ""
	files["web/src/one.ts"] = `const text = "// @ts-nocheck";`
	files["other.templ"] = `<script-widget></script-widget>`
	if got := inspect(files); len(got) != 1 || !strings.Contains(got[0], "@ts-nocheck") {
		t.Fatal(got)
	}
	files["web/src/one.ts"] = `const text = "// checked";`
	if got := inspect(files); len(got) != 0 {
		t.Fatal(got)
	}
	if got := additions([]string{"example"}, stems(files[listFile])); len(got) != 0 {
		t.Fatal(got)
	}
	if got := stems("# comment\n example # awaiting conversion\n\n"); !slices.Equal(got, []string{"example"}) {
		t.Fatal(got)
	}
	files[listFile], files["web/static/example-v2.js"] = "example", ""
	if got := inspect(files); len(got) != 0 {
		t.Fatal(got)
	}
}

func write(t *testing.T, root, name, text string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestGitOutput(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	root := t.TempDir()
	if _, err := git(root, "rev-parse", "--git-dir"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("git failure lost stderr: %v", err)
	}
	// A successful alias emits a warning without relying on local git settings.
	out, err := git(root, "-c", `alias.typedscripts-probe=!printf 'tracked\0'; printf 'git warning\n' >&2`, "typedscripts-probe")
	if err != nil || out != "tracked\x00" {
		t.Fatalf("git stdout includes stderr: %q, %v", out, err)
	}
}

func TestGitAndBase(t *testing.T) {
	root := t.TempDir()
	g := func(args ...string) string {
		t.Helper()
		out, err := git(root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	g("init", "-q")
	g("config", "user.email", "test@example.invalid")
	g("config", "user.name", "Test")
	write(t, root, "web/static/example-v1.js", "")
	g("add", ".")
	g("commit", "-qm", "base without list")
	g("update-ref", "refs/remotes/origin/main", "HEAD")
	for name, text := range fixture() {
		write(t, root, name, text)
	}
	g("add", ".")
	g("commit", "-qm", "bootstrap")
	for _, ci := range []bool{false, true} {
		base, err := baseStems(root, ci)
		if err != nil || len(additions(base, []string{"example"})) != 0 || len(additions(base, []string{"example", "extra"})) != 1 {
			t.Fatalf("bootstrap twins (CI=%v): %v, %v", ci, base, err)
		}
		if err := run(root, ci, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	g("update-ref", "refs/remotes/origin/main", "HEAD")
	g("commit", "--allow-empty", "-qm", "after list")
	t.Run("legacy growth twins", func(t *testing.T) {
		write(t, root, listFile, "example\nextra\n")
		write(t, root, "web/static/extra-v1.js", "")
		g("add", "web/static/extra-v1.js")
		for _, ci := range []bool{true, false} {
			if err := run(root, ci, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "added legacy stem: extra") {
				t.Errorf("growth twin (CI=%v): %v; want added legacy stem: extra", ci, err)
			}
		}
		write(t, root, listFile, "example\n")
		g("rm", "-qf", "--", "web/static/extra-v1.js")
		for _, ci := range []bool{true, false} {
			if err := run(root, ci, &bytes.Buffer{}); err != nil {
				t.Errorf("restored twin (CI=%v): %v", ci, err)
			}
		}
	})
	if base, err := baseStems(root, true); err != nil || !slices.Equal(base, []string{"example"}) {
		t.Fatalf("base list: %v, %v", base, err)
	}
	write(t, root, "untracked.js", "")
	if err := run(root, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("untracked output: %v", err)
	}
	g("add", "untracked.js")
	if err := run(root, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "untyped client script") {
		t.Fatalf("tracked script: %v", err)
	}
	g("reset", "-q", "--", "untracked.js")
	g("update-ref", "-d", "refs/remotes/origin/main")
	var out bytes.Buffer
	if err := run(root, false, &out); err != nil || !strings.HasPrefix(out.String(), "NOT VERIFIED:") {
		t.Fatalf("local missing base: %v, %s", err, &out)
	}
	write(t, root, "tsconfig.json", "{}")
	if err := run(root, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "tsconfig.json") {
		t.Fatalf("local skip hid another rule: %v", err)
	}
	if err := run(t.TempDir(), true, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted git read failure")
	}
	write(t, root, "tsconfig.json", config)
	g("checkout", "--orphan", "shallow")
	g("commit", "-qm", "no parent")
	if err := run(root, true, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "HEAD^1") {
		t.Fatalf("CI missing base: %v", err)
	}
}
