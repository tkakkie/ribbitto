package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentPolicy(t *testing.T) {
	for _, tt := range []struct {
		name, file, content, reason, want string
		limit                             int
	}{
		{"under", "docs/a.md", "あ\n", "why", "", 3},
		{"at", "docs/a.md", "あ\n", "why", "", 2},
		{"over", "docs/a.md", "あ\n", "why", "2 characters, over its total limit of 1", 1},
		{"unlisted capped", "docs/a.md", "", "", "allow-list entry", 2},
		{"unlisted new directory", "docs/new/a.md", "", "", "allow-list entry", 2},
		{"unlisted root", "NEW.md", "", "", "allow-list entry", 2},
		{"unlisted github", ".github/a.md", "", "", "allow-list entry", 2},
		{"undeclared directory", "docs/new/a.md", "", "why", "neither capped nor exempt", 2},
		{"nested capped directory", "docs/domain/nested/a.md", "あ\n", "why", "over its total limit", 1},
		{"decisions exempt", "docs/decisions/a.md", "", "", "", 0},
		{"schema exempt", "docs/schema/x.md", "", "", "", 0},
		{"schema lookalike", "docs/schemata/x.md", "", "", "allow-list entry", 0},
		{"dependencies exempt", "docs/dependencies.md", "", "", "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allowed := map[string]string{}
			if tt.reason != "" {
				allowed[tt.file] = tt.reason
			}
			problems, err := checkDocuments(repo(t, map[string]string{tt.file: tt.content}), map[string]int{"docs/": tt.limit, "docs/domain/": tt.limit}, allowed)
			if err != nil || tt.want == "" && len(problems) != 0 || tt.want != "" && !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Fatalf("problems %q, error %v, want %q", problems, err, tt.want)
			}
		})
	}
	for _, allowed := range []map[string]string{{"gone.md": "why"}, {"README.md": " "}} {
		problems, err := checkDocuments(repo(t, map[string]string{"README.md": ""}), nil, allowed)
		if err != nil || !strings.Contains(strings.Join(problems, "\n"), "stale allow-list entry or empty reason") {
			t.Fatalf("problems %q, error %v", problems, err)
		}
	}
	root := repo(t, map[string]string{"docs/domain/a.md": "あ", "docs/domain/sub/b.md": "\n"})
	problems, err := checkDocuments(root, map[string]int{"docs/domain/": 1}, map[string]string{"docs/domain/a.md": "why", "docs/domain/sub/b.md": "why"})
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "2 characters, over its total limit of 1") {
		t.Fatalf("aggregate total: problems %q, error %v", problems, err)
	}
}

func TestPolicyComparison(t *testing.T) {
	baseline := func(limit int) string {
		return fmt.Sprintf("package main\nconst directoryBaseline = `%s\ndocs/ %d\ndocs/architecture/ 0\ndocs/domain/ 0\ndocs/measurements/ 0\ndocs/workflow/ 0\n`\n", strings.Repeat("a", 40), limit)
	}
	for _, tt := range []struct{ name, before, head, base, want string }{
		{"equal", baseline(2), baseline(2), "HEAD", ""},
		{"raised", baseline(2), baseline(3), "HEAD", "limit removed or raised"},
		{"lowered", baseline(2), baseline(1), "HEAD", ""},
		{"default behind main", baseline(2), baseline(2), "", ""},
		{"default raised", baseline(2), baseline(3), "", "limit removed or raised"},
		{"default missing origin/main", baseline(2), baseline(2), "", "comparison base \"merge base of origin/main and HEAD\""},
		{"unreadable", baseline(2), baseline(2), "absent", "comparison base \"absent\""},
		{"missing parent", baseline(2), baseline(2), "HEAD^1", "comparison base \"HEAD^1\""},
		{"introducing", "", baseline(2), "HEAD", "introducing directory limits"},
		{"removed", baseline(2), "", "HEAD", "reading current baseline"},
		{"emptied", baseline(2), "package main\n", "HEAD", "nonempty directory limits"},
		{"blank contents", baseline(2), "\n", "HEAD", "parsing baseline"},
		{"empty base", "package main\n", baseline(2), "HEAD", "comparison base \"HEAD\""},
		{"removed cap", baseline(2), strings.Replace(baseline(2), "docs/ 2\n", "", 1), "HEAD", "limit removed or raised"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{}
			for file := range documentAllowlist() {
				files[file] = ""
			}
			root := repo(t, files)
			write := func(content string) {
				t.Helper()
				path := filepath.Join(root, baselineFile)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if content == "" {
					_ = os.Remove(path)
					return
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write(tt.before)
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = root
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %v: %s", err, output)
				}
			}
			commit := func() {
				t.Helper()
				git("add", ".")
				git("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "baseline")
			}
			git("init", "-q")
			commit()
			if tt.base == "" && tt.name != "default missing origin/main" {
				write(baseline(1))
				commit()
				git("update-ref", "refs/remotes/origin/main", "HEAD")
				git("checkout", "-qb", "topic", "HEAD^1")
			}
			write(tt.head)
			if tt.base == "" {
				commit()
			}
			var notice bytes.Buffer
			problems, err := checkPolicy(root, tt.base, &notice)
			got := fmt.Sprint(err, problems, notice.String())
			if tt.want == "" && (err != nil || len(problems) != 0 || notice.Len() != 0) || tt.want != "" && !strings.Contains(got, tt.want) {
				t.Fatalf("got %s, want %q", got, tt.want)
			}
			if tt.name == "unreadable" || tt.name == "default missing origin/main" {
				for _, want := range []string{"fatal:", "git fetch --unshallow <remote>", "--base <rev>", "DOCSCHECK_BASE=<rev>"} {
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("missing git stderr or hint %q: %v", want, err)
					}
				}
			}
		})
	}
	_, err := checkPolicy(t.TempDir(), "HEAD^1", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "HEAD^1") || !strings.Contains(err.Error(), "git fetch") {
		t.Fatalf("no history: %v", err)
	}
}

func TestExistingDocumentPolicy(t *testing.T) {
	limits, err := readBaseline([]byte("package main\nconst directoryBaseline = `" + directoryBaseline + "`"))
	if err != nil {
		t.Fatal(err)
	}
	problems, err := checkDocuments("../..", limits, documentAllowlist())
	if err != nil || len(problems) != 0 {
		t.Fatalf("problems %q, error %v", problems, err)
	}
}
