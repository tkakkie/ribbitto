package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, path, content string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, graph))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	if _, err := command(root, "git", args...); err != nil {
		t.Fatal(err)
	}
}

func TestGeneration(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"go.mod":                             "module example.invalid/deps\n\ngo 1.27.2\n",
		"internal/a/a.go":                    "package a\nimport _ \"example.invalid/deps/internal/b\"\n",
		"internal/a/a_test.go":               "package a\nimport _ \"example.invalid/deps/internal/d\"\n",
		"internal/a/platform_linux_amd64.go": "package a\nimport _ \"example.invalid/deps/internal/c\"\n",
		"internal/a/platform_darwin.go":      "package a\nimport _ \"example.invalid/deps/internal/d\"\n",
		"internal/a/cgo.go":                  "package a\nimport \"C\"\nimport _ \"example.invalid/deps/internal/e\"\n",
		"internal/b/b.go":                    "package b\n", "internal/c/c.go": "package c\n",
		"internal/d/d.go": "package d\n", "internal/e/e.go": "package e\n",
		"cmd/app/main.go":      "package main\nimport _ \"example.invalid/deps/internal/a\"\nfunc main() {}\n",
		"docs/dependencies.md": "stale",
	} {
		write(t, root, path, source)
	}
	// Deliberately hostile host settings must not change the generated graph.
	for key, value := range map[string]string{"GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "0", "GOFLAGS": "-invalid-deps-test-flag"} {
		t.Setenv(key, value)
	}
	generate := func(t *testing.T) {
		t.Helper()
		if err := run(root, false, "", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	generate(t)
	before := read(t, root)
	want := header + "cmd/app -> internal/a\ninternal/a -> internal/b\ninternal/a -> internal/c\ninternal/a -> internal/e\n```\n"
	if before != want {
		t.Fatalf("graph = %s, want %s", before, want)
	}
	generate(t)
	if read(t, root) != before {
		t.Fatal("successive generations differ")
	}
	for _, tc := range []struct{ name, path, source, added string }{
		{"new edge", "internal/b/new.go", "package b\nimport _ \"example.invalid/deps/internal/c\"\n", "internal/b -> internal/c\n"},
		{"existing edge", "internal/a/same.go", "package a\nimport _ \"example.invalid/deps/internal/b\"\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, root, tc.path, tc.source)
			err := run(root, true, "", &bytes.Buffer{})
			if (err != nil) != (tc.added != "") {
				t.Fatalf("check = %v, added = %q", err, tc.added)
			}
			generate(t)
			want := strings.TrimSuffix(before, "```\n") + tc.added + "```\n"
			if got := read(t, root); got != want {
				t.Fatalf("regeneration = %s, want %s", got, want)
			}
			if err := run(root, true, "", &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			before = want
		})
	}
	git(t, root, "init", "-q")
	git(t, root, "add", graph)
	git(t, root, "-c", "user.name=Dependency test", "-c", "user.email=deps@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Generated graph")
	generate(t)
	if read(t, root) != before {
		t.Fatal("regeneration after committing differs")
	}
}

func TestDiff(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	commit := func() {
		git(t, root, "-c", "user.name=Dependency test", "-c", "user.email=deps@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Graph", "--allow-empty")
	}
	commit()
	const baseline = "Header\n```text\ncmd/ribbitto -> internal/web\ninternal/web -> internal/kernel\n```\n"
	write(t, root, graph, baseline)
	git(t, root, "add", graph)
	commit()
	const crossing = "## Package-import edge changes\n\n### Crossing units\n\n"
	const changed = "```text\ninternal/web -> internal/kernel\ninternal/web/i18n -> internal/kernel\ninternal/web/i18n -> web/static\ncmd/ribbitto -> cmd/seed\ncmd/ribbitto -> cmd/ribbitto/helper\ninternal/web/i18n -> internal/web/middleware\n```\n"
	for _, tc := range []struct{ name, rev, current, want string }{
		{"unchanged", "HEAD", baseline, "No package-import edges changed.\n"},
		{"header only", "HEAD", "Different " + baseline, "No package-import edges changed.\n"},
		{"prefix targets", "HEAD^", "```text\ncmd/x -> internal/web\ncmd/x -> internal/web/view\n```\n", crossing + "- Added: `cmd/x -> internal/web`\n- Added: `cmd/x -> internal/web/view`\n"},
		{"unknown revision", "refs/heads/unknown-deps-test-revision", baseline, ""},
		{"groups", "HEAD", changed, crossing + "- Added: `cmd/ribbitto -> cmd/seed`\n- Added: `internal/web/i18n -> internal/kernel`\n- Added: `internal/web/i18n -> web/static`\n- Removed: `cmd/ribbitto -> internal/web`\n\n### Within one unit\n\n- Added: `cmd/ribbitto -> cmd/ribbitto/helper`\n- Added: `internal/web/i18n -> internal/web/middleware`\n"},
		{"valid revision without graph", "HEAD^", baseline, crossing + "- Added: `cmd/ribbitto -> internal/web`\n- Added: `internal/web -> internal/kernel`\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, root, graph, tc.current)
			var out bytes.Buffer
			err := run(root, false, tc.rev, &out)
			if tc.want == "" {
				if err == nil {
					t.Fatal("accepted unknown revision")
				}
				return
			}
			if err != nil || out.String() != tc.want {
				t.Fatalf("diff = %q, %v; want %q", out.String(), err, tc.want)
			}
		})
	}
}
