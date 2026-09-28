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
	}, "\n")
	got := headingAnchors(parse([]byte(content)))
	for _, want := range []string{"setup", "xy", "api", "the-snake_case-field", "same", "same-1", "same-1-1", "code-label", "posting-a-message-planned-m2m3"} {
		if !got[want] {
			t.Errorf("missing anchor %q in %v", want, got)
		}
	}
	for _, unwanted := range []string{"_setup_", "fake-tilde", "fake-long", "apic"} {
		if got[unwanted] {
			t.Errorf("unexpected anchor %q", unwanted)
		}
	}
	if len(got) != 9 {
		t.Errorf("%d anchors, want 9: %v", len(got), got)
	}
}
