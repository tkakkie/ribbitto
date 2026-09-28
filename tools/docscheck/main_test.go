package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo writes files into a temporary repository root.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSizes(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  string // a substring of the only problem, or "" for none
	}{
		{"AGENTS.md at its limit", map[string]string{"AGENTS.md": strings.Repeat("a", agentsLimit)}, ""},
		{"AGENTS.md one over", map[string]string{"AGENTS.md": strings.Repeat("a", agentsLimit+1)}, "AGENTS.md: 8001 characters"},
		{"doc at its limit", map[string]string{"docs/x.md": strings.Repeat("a", docLimit)}, ""},
		{"doc one over", map[string]string{"docs/x.md": strings.Repeat("a", docLimit+1)}, "docs/x.md: 11001 characters"},
		{"code points, not bytes", map[string]string{"docs/x.md": strings.Repeat("あ", docLimit)}, ""},
		{"newlines count", map[string]string{"docs/x.md": strings.Repeat("\n", docLimit+1)}, "over its limit"},
		{"README.md is checked", map[string]string{"README.md": strings.Repeat("a", docLimit+1)}, "README.md"},
		{"generated and DECISIONS.md are not", map[string]string{
			"docs/schema/t.md": strings.Repeat("a", docLimit+1), "docs/dependencies.md": strings.Repeat("a", docLimit+1),
			"DECISIONS.md": strings.Repeat("a", docLimit+1),
		}, ""},
		{"listed exception passes", map[string]string{"docs/x.md": strings.Repeat("a", docLimit+1), exceptionsFile: "# comment\ndocs/x.md #12\n"}, ""},
		{"stale exception fails", map[string]string{"docs/x.md": "short", exceptionsFile: "docs/x.md #12\n"}, "still listed"},
		{"exception for an unchecked file fails", map[string]string{exceptionsFile: "docs/gone.md #12\n"}, "not a checked document"},
		{"malformed exception fails", map[string]string{exceptionsFile: "docs/x.md 12\n"}, "want \"<path> #<issue>\""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems, err := checkSizes(repo(t, tt.files))
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" && len(problems) != 0 || tt.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tt.want)) {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
		})
	}
}

func TestLinks(t *testing.T) {
	target := "# Title\n\n## Posting a message *(planned, M2–M3)*\n\n## Same\n\n## Same\n\n```\n## Not a heading\n```\n"
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"valid links and anchors", map[string]string{
			"docs/b.md":    target,
			"docs/a.md":    "[p](b.md#posting-a-message-planned-m2m3) [s](b.md#same-1) [t](#top)\n\n## Top\n[ext](https://example.com/x.md#y)",
			"AGENTS.md":    "Read `docs/b.md#same` first.",
			"scripts/x.sh": "# see docs/b.md#title\n",
		}, ""},
		{"missing file", map[string]string{"docs/a.md": "[x](missing.md)"}, "docs/missing.md does not exist"},
		{"broken anchor", map[string]string{"docs/b.md": target, "docs/a.md": "[x](b.md#nope)"}, "has no heading #nope"},
		{"broken anchor in the same file", map[string]string{"docs/a.md": "[x](#nope)"}, "has no heading #nope"},
		{"heading inside a code fence is not an anchor", map[string]string{"docs/b.md": target, "docs/a.md": "[x](b.md#not-a-heading)"}, "has no heading"},
		{"link inside a code fence is ignored", map[string]string{"docs/a.md": "```\n[x](missing.md)\n```\n"}, ""},
		{"bare reference in a script", map[string]string{"docs/b.md": target, "scripts/x.sh": "echo 'see docs/b.md#nope'\n"}, "scripts/x.sh:1: docs/b.md has no heading #nope"},
		{"bare reference in a template", map[string]string{".github/t.md": "Follow docs/gone.md."}, "docs/gone.md does not exist"},
		{"relative link from a subdirectory", map[string]string{"DECISIONS.md": "# D", "docs/a.md": "[d](../DECISIONS.md)"}, ""},
		{"template link resolves relative to the template, not as a bare reference", map[string]string{
			"docs/b.md": target, ".github/t.md": "See [review](../docs/b.md#same) and docs/b.md#title.",
		}, ""},
		{"fenced examples are not scanned for bare references", map[string]string{
			".github/e.md": "```\n[x](missing.md) docs/gone.md\n```\n~~~\ndocs/gone.md\n~~~\n",
		}, ""},
		{"titled inline link", map[string]string{"docs/a.md": `[x](missing.md "Title")`}, "docs/missing.md does not exist"},
		{"reference definition to a missing file", map[string]string{"docs/a.md": "[x][t]\n\n[t]: missing.md\n"}, "docs/missing.md does not exist"},
		{"reference definition with a broken anchor", map[string]string{"docs/b.md": target, "docs/a.md": "[t]: b.md#nope \"Title\"\n"}, "has no heading #nope"},
		{"bare Unicode fragment is checked whole", map[string]string{"docs/x.md": "## 見出し\n", "scripts/s.sh": "# see docs/x.md#見出し and docs/x.md#不存在\n"}, "has no heading #不存在"},
		{"bare fragment is not shortened", map[string]string{"docs/b.md": target, "scripts/s.sh": "# see docs/b.md#title-x\n"}, "has no heading #title-x"},
		{"code span inside a heading's link label", map[string]string{
			"docs/b.md": "## [`API`](c.md)\n", "docs/c.md": "# C", "docs/a.md": "[x](b.md#api)",
		}, ""},
		{"parenthesis inside a link title, missing file", map[string]string{"docs/a.md": `[x](missing.md "Use ) here")`}, "docs/missing.md does not exist"},
		{"parenthesis inside a link title, broken anchor", map[string]string{"docs/b.md": target, "docs/a.md": `[x](b.md#nope "Use ( here")`}, "has no heading #nope"},
		{"link label is not read as a bare reference", map[string]string{"docs/b.md": target, ".github/t.md": "[`../docs/b.md`](../docs/b.md)"}, ""},
		{"fence inside a blockquote is an example", map[string]string{"docs/a.md": "> ~~~\n> [x](missing.md)\n> ~~~\n\n[y](missing2.md)\n"}, "docs/missing2.md does not exist"},
		{"fence inside a list item is an example", map[string]string{"docs/a.md": "- item\n\n  ```\n  [x](missing.md)\n  ```\n"}, ""},
		{"used reference link to a missing file", map[string]string{"docs/a.md": "See [x][t].\n\n[t]: missing.md\n"}, "docs/missing.md does not exist"},
		{"inline HTML comment in a template, missing file", map[string]string{".github/t.md": "Risk <!-- see docs/gone.md --> here"}, "docs/gone.md does not exist"},
		{"block HTML comment in a template, broken anchor", map[string]string{"docs/b.md": target, ".github/t.md": "<!--\n  see docs/b.md#nope\n-->\n"}, "has no heading #nope"},
		{"percent-encoded fragment and path resolve", map[string]string{
			"docs/b.md": "## 見出し\n", "docs/my file.md": "# M",
			"docs/a.md": "[x](b.md#%E8%A6%8B%E5%87%BA%E3%81%97) [y](my%20file.md)\n\n[r]: b.md#%E8%A6%8B%E5%87%BA%E3%81%97\n",
		}, ""},
		{"escaped punctuation in destinations", map[string]string{
			"docs/api(v2).md": "## a-b\n",
			"docs/a.md":       "[x](api\\(v2\\).md#a\\-b)\n\n[r]: api\\(v2\\).md#a\\-b\n",
		}, ""},
		{"escaped punctuation in a broken fragment", map[string]string{
			"docs/api(v2).md": "## a-b\n", "docs/a.md": "[x](api\\(v2\\).md#a\\-c)\n",
		}, "has no heading #a-c"},
		{"escaped punctuation in a reference definition's broken fragment", map[string]string{
			"docs/api(v2).md": "## a-b\n", "docs/a.md": "[r]: api\\(v2\\).md#a\\-c\n",
		}, "has no heading #a-c"},
		{"query string on a link", map[string]string{"docs/a.md": "[m](missing.md?plain=1)"}, "docs/missing.md does not exist"},
		{"trailing slash on a link", map[string]string{"docs/a.md": "[m](missing.md/)"}, "docs/missing.md does not exist"},
		{"encoded trailing space on a link", map[string]string{"docs/a.md": "[m](missing.md%20)"}, "does not exist"},
		{"URL-like fragment on a relative link", map[string]string{"docs/a.md": "[m](missing.md#http://example.com)"}, "docs/missing.md does not exist"},
		{"git pathspec in a script", map[string]string{"scripts/s.sh": "git show \"$ref:.github/prompts/gone.md\"\n"}, ".github/prompts/gone.md does not exist"},
		{"shell assignment is a bare reference", map[string]string{"scripts/s.sh": "file=docs/gone.md\n"}, "docs/gone.md does not exist"},
		{"a longer file name is not a reference", map[string]string{"scripts/s.sh": "cp docs/x.md.backup /tmp\n"}, ""},
		{"sentence punctuation after a reference is allowed", map[string]string{"scripts/s.sh": "# See docs/gone.md.\n"}, "docs/gone.md does not exist"},
		{"fence in a template's blockquote is an example", map[string]string{".github/e.md": "> ```\n> docs/gone.md\n> ```\n"}, ""},
		{"Go tests of other scripts are scanned", map[string]string{"scripts/y/y_test.go": "// see docs/gone.md\n"}, "docs/gone.md does not exist"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems, err := checkLinks(repo(t, tt.files))
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" && len(problems) != 0 || tt.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tt.want)) {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
		})
	}
}

func TestHeadingAnchors(t *testing.T) {
	content := strings.Join([]string{
		"## _Setup_",
		"## `[x](y)`",
		"## [API](https://example.com/a(b)c)",
		"## The `snake_case` field",
		"## Same",
		"## Same",
		"## Same-1",
		"## [`Code`](x.md) label",
		"~~~",
		"## Fake tilde",
		"~~~",
		"````",
		"```",
		"## Fake long",
		"````",
		"## Posting a message *(planned, M2–M3)*",
		"## A &amp; B",
		"## <https://example.com>",
		"## का",
		"## Ⅳ",
		"## `a &amp; b`",
	}, "\n")
	got := headingAnchors(parse([]byte(content)))
	for _, want := range []string{"setup", "xy", "api", "the-snake_case-field", "same", "same-1", "same-1-1", "code-label", "posting-a-message-planned-m2m3", "a--b", "httpsexamplecom", "का", "ⅳ", "a-amp-b"} {
		if !got[want] {
			t.Errorf("missing anchor %q in %v", want, got)
		}
	}
	for _, unwanted := range []string{"_setup_", "fake-tilde", "fake-long", "apic", "", "क"} {
		if got[unwanted] {
			t.Errorf("unexpected anchor %q", unwanted)
		}
	}
	if len(got) != 14 {
		t.Errorf("%d anchors, want 14: %v", len(got), got)
	}
}

// A link must stay inside the repository and point at a regular file:
// GitHub cannot follow one out of it and shows a symlink, not its target.
func TestLinksStayInsideTheRepository(t *testing.T) {
	root := repo(t, map[string]string{
		"docs/architecture.md": "## Reverse proxies\n",
		"docs/a.md":            "[out](../../README.md) [alias](alias.md#reverse-proxies) [ok](architecture.md#reverse-proxies)",
	})
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "README.md"), []byte("# Outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("architecture.md", filepath.Join(root, "docs", "alias.md")); err != nil {
		t.Fatal(err)
	}
	problems, err := checkLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || !strings.Contains(problems[0], "../README.md does not exist") || !strings.Contains(problems[1], "docs/alias.md does not exist") {
		t.Fatalf("problems %q", problems)
	}
}
