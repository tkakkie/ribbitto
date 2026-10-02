package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisions(t *testing.T) {
	const index = "# Decisions\n\n- [1. First](docs/decisions/01-first.md)\n"
	const heading = "# 1. First\n"
	current, err := os.ReadFile("../../DECISIONS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"decision checks disabled without decisions directory", map[string]string{"DECISIONS.md": string(current)}, ""},
		{"size limit disabled without decisions directory", map[string]string{"DECISIONS.md": strings.Repeat("a", docLimit+1)}, ""},
		{"valid split with gaps", map[string]string{
			"DECISIONS.md":               index + "- [14. Later](docs/decisions/14-later.md)\n",
			"docs/decisions/01-first.md": heading,
			"docs/decisions/14-later.md": "# 14. Later\n",
		}, ""},
		{"unlisted file", map[string]string{
			"DECISIONS.md": index, "docs/decisions/01-first.md": heading,
			"docs/decisions/02-second.md": "# 2. Second\n",
		}, "docs/decisions/02-second.md: not listed"},
		{"missing target", map[string]string{
			"DECISIONS.md": index + "- [2. Missing](docs/decisions/02-missing.md)\n", "docs/decisions/01-first.md": heading,
		}, "docs/decisions/02-missing.md is not an existing decision file"},
		{"duplicate file number", map[string]string{
			"DECISIONS.md":               index + "- [1. Duplicate](docs/decisions/01-duplicate.md)\n",
			"docs/decisions/01-first.md": heading, "docs/decisions/01-duplicate.md": heading,
		}, "duplicate decision number 1"},
		{"duplicate index number", map[string]string{
			"DECISIONS.md": index + "- [1. First](docs/decisions/01-first.md)\n", "docs/decisions/01-first.md": heading,
		}, "duplicate index number 1"},
		{"heading differs", map[string]string{
			"DECISIONS.md": index, "docs/decisions/01-first.md": "# 2. First\n",
		}, "first line must be # 1. Title"},
		{"index differs", map[string]string{
			"DECISIONS.md": "- [2. First](docs/decisions/01-first.md)\n", "docs/decisions/01-first.md": heading,
		}, "index number 2 differs from file number 1"},
		{"filename differs from index and heading", map[string]string{
			"DECISIONS.md": "- [1. First](docs/decisions/02-first.md)\n", "docs/decisions/02-first.md": heading,
		}, "first line must be # 2. Title"},
		{"index at limit", map[string]string{
			"DECISIONS.md": index + strings.Repeat("あ", docLimit-len(index)), "docs/decisions/01-first.md": heading,
		}, ""},
		{"index over limit", map[string]string{
			"DECISIONS.md": index + strings.Repeat("あ", docLimit-len(index)+1), "docs/decisions/01-first.md": heading,
		}, "DECISIONS.md: 11001 characters, over its limit of 11000"},
		{"decision at limit", map[string]string{
			"DECISIONS.md": index, "docs/decisions/01-first.md": heading + strings.Repeat("a", docLimit-len(heading)),
		}, ""},
		{"decision over limit", map[string]string{
			"DECISIONS.md": index, "docs/decisions/01-first.md": heading + strings.Repeat("a", docLimit-len(heading)+1),
		}, "docs/decisions/01-first.md: 11001 characters, over its limit of 11000"},
		{"missing index", map[string]string{"docs/decisions/01-first.md": heading}, "decision index does not exist"},
		{"filename needs two digits", map[string]string{
			"DECISIONS.md": "- [1. First](docs/decisions/1-first.md)\n", "docs/decisions/1-first.md": heading,
		}, "want a regular NN-slug.md file"},
		{"zero is not a decision number", map[string]string{
			"DECISIONS.md": "# Decisions\n", "docs/decisions/00-zero.md": "# 0. Zero\n",
		}, "want a regular NN-slug.md file"},
		{"invalid slug", map[string]string{
			"DECISIONS.md": "# Decisions\n", "docs/decisions/01-First.md": heading,
		}, "want a regular NN-slug.md file"},
		{"nested decision", map[string]string{
			"DECISIONS.md": "# Decisions\n", "docs/decisions/nested/01-first.md": heading,
		}, "docs/decisions/nested: want a regular NN-slug.md file"},
		{"missing heading", map[string]string{
			"DECISIONS.md": index, "docs/decisions/01-first.md": "Some prose\n",
		}, "first line must be # 1. Title"},
		{"wrong heading level", map[string]string{
			"DECISIONS.md": index, "docs/decisions/01-first.md": "## 1. First\n",
		}, "first line must be # 1. Title"},
		{"malformed index line", map[string]string{
			"DECISIONS.md": "- [First](docs/decisions/01-first.md)\n", "docs/decisions/01-first.md": heading,
		}, "want - [N. Title]"},
		{"entry outside a list", map[string]string{
			"DECISIONS.md": "[1. First](docs/decisions/01-first.md)\n", "docs/decisions/01-first.md": heading,
		}, "want - [N. Title]"},
		{"nested entry is checked", map[string]string{
			"DECISIONS.md": index + "  - [2. Missing](docs/decisions/02-missing.md)\n", "docs/decisions/01-first.md": heading,
		}, "docs/decisions/02-missing.md is not an existing decision file"},
		{"fenced example does not list a file", map[string]string{
			"DECISIONS.md": "```markdown\n" + index + "```\n", "docs/decisions/01-first.md": heading,
		}, "docs/decisions/01-first.md: not listed"},
		{"fenced example is ignored", map[string]string{
			"DECISIONS.md":               index + "\n```markdown\n- [2. Example](docs/decisions/02-example.md)\n```\n",
			"docs/decisions/01-first.md": heading,
		}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := repo(t, tt.files)
			var problems []string
			for _, check := range []func(string) ([]string, error){checkSizes, checkDecisions} {
				got, err := check(root)
				if err != nil {
					t.Fatal(err)
				}
				problems = append(problems, got...)
			}
			if tt.want == "" && len(problems) != 0 || tt.want != "" && !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
		})
	}
}

func TestEmptyDecisionsDirectoryEnablesIndexLimit(t *testing.T) {
	root := repo(t, map[string]string{"DECISIONS.md": strings.Repeat("a", docLimit+1)})
	if err := os.MkdirAll(filepath.Join(root, decisionsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	problems, err := checkSizes(root)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "DECISIONS.md: 11001 characters") {
		t.Fatalf("problems %q, error %v", problems, err)
	}
}
