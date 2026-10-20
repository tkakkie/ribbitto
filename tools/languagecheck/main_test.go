package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if _, ok := files[exceptionsFile]; !ok {
		files[exceptionsFile] = "# No exceptions.\n"
	}
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func inspect(t *testing.T, files map[string]string) []string {
	t.Helper()
	root := fixture(t, files)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	problems, err := checkFiles(root, names)
	if err != nil {
		t.Fatal(err)
	}
	return problems
}

func TestRules(t *testing.T) {
	for _, tt := range []struct {
		name, source string
		allowed      bool
	}{
		{"a.go", "package a", true},
		{"nested/a.go", "", true},
		{"db/migrations/a.sql", "", true},
		{"db/queries/org/a.sql", "", true},
		{"internal/web/view/a.templ", "", true},
		{"internal/lintfixture/a.templ", "", true},
		{"web/styles/a.css", "", true},
		{"web/static/css/a.css", "", true},
		{"web/static/a-v1.js", "", true},
		{"web/static/a-v23.js", "", true},
		{"web/static/brand/a.svg", "", true},
		{"web/static/channel-images-v1/a.svg", "", true},
		{"docs/a.md", "", true},
		{"docs/nested/a.md", "", true},
		{".github/ISSUE_TEMPLATE/a.md", "", true},
		{".github/instructions/a.md", "", true},
		{".github/prompts/a.md", "", true},
		{".github/pull_request_template.md", "", true},
		{"AGENTS.md", "", true}, {"CLAUDE.md", "", true},
		{"DECISIONS.md", "", true}, {"README.md", "", true},
		{".github/workflows/a.yml", "", true},
		{".github/ISSUE_TEMPLATE/config.yml", "", true},
		{".github/dependabot.yml", "", true},
		{".golangci.yml", "", true}, {".tbls.yml", "", true},
		{"compose.yml", "", true}, {"sqlc.yaml", "", true},
		{"internal/web/i18n/locales/a.toml", "", true},
		{"cmd/seed/conversations.json", "", true},
		{"go.mod", "", true}, {"go.sum", "", true},
		{"tools/go.mod", "", true}, {"tools/go.sum", "", true},
		{"tools/tbls/go.mod", "", true}, {"tools/tbls/go.sum", "", true},
		{"LICENSE", "", true}, {"Makefile", "", true},
		{".gitignore", "", true}, {".gitattributes", "", true}, {".env.example", "", true},
		{"docs/api/a.txt", "", true}, {"docs/size-exceptions.txt", "", true},
		{exceptionsFile, "", true}, {"tools/scopecheck/allowlist.txt", "", true},
		{"cmd/argonreview/testdata/implement-guard/cases.json", "[]", true},
		{"cmd/argonreview/testdata/implement-guard/hooks-off.json", "{}", true},
		{"cmd/argonreview/testdata/implement-guard/hooks-on.json", "{}", true},
		{"cmd/argonreview/testdata/implement-guard/settings-off.json", "{}", true},
		{"cmd/argonreview/testdata/implement-guard/settings-on.json", "{}", true},
		{"cmd/argonreview/testdata/implement-guard/mcp-config-on.json", "{}", true},
		{"cmd/argonreview/testdata/implement-guard/real-agy-calls.jsonl", "{}\n", true},
		{"internal/web/testdata/browser-shard-totals.txt", "", true},
		{"internal/conversation/testdata/fuzz/FuzzParseChatV1/a", "go test fuzz v1\n[]byte(\"x\")", true},
		{"internal/identity/testdata/fuzz/FuzzParse/a", "go test fuzz v1", true},
		{"web/static/vendor/README.md", "", true},
		{"web/static/vendor/fluent-icons-1.1.344.svg", "", true},
		{"web/static/vendor/htmx-2.0.7.min.js", "", true},
		{"web/static/vendor/htmx-ext-sse-2.2.4.min.js", "", true},
		{"web/static/vendor/idiomorph-ext-0.7.4.min.js", "", true},
		{"web/static/vendor/fluent-icons-LICENSE", "", true},
		{"web/static/vendor/htmx-ext-sse-LICENSE", "", true},
		{"web/static/vendor/htmx.org-LICENSE", "", true},
		{"web/static/vendor/idiomorph-LICENSE", "", true},
		{"a.js", "", false}, {"web/static-evil/a.js", "", false},
		{"web/static/eslint.config.js", "", false}, {"web/static/vite.config.js", "", false},
		{"web/static/example.js", "", false}, {"web/static/app.js", "", false},
		{"web/static/x/foo-v1.js", "", false},
		{"web/static/foo-v1.config.js", "", false},
		{"web/static/a-v1.min.js", "", false},
		{"web/static/x-v1.test.js", "", false},
		{"web/static/eslint-v9.config.js", "", false},
		{"web/static/-v1.js", "", false},
		{"foo/web/static/a.js", "", false}, {"web/static/nested/a.js", "", false},
		{"a.sql", "", false}, {"db/a.sql", "", false}, {"db/queries/a.sql", "", false},
		{"db/migrations/nested/a.sql", "", false}, {"A.SQL", "", false},
		{"db/migrations/A.SQL", "", false}, {"Makefile.JS", "", false},
		{"a.templ", "", false}, {"internal/web/a.templ", "", false},
		{"a.css", "", false}, {"a.svg", "", false}, {"web/static/a.svg", "", false},
		{"a.md", "", false}, {"src/a.md", "", false}, {"a.yml", "", false},
		{".github/unknown.yml", "", false}, {".github/workflows/nested/a.yml", "", false},
		{"a.toml", "", false}, {"a.json", "", false}, {"cmd/seed/nested/a.json", "", false},
		{"other/go.mod", "", false}, {"other/go.sum", "", false}, {"other/LICENSE", "", false},
		{"docs/unknown.txt", "", false}, {"docs/api/nested/a.txt", "", false},
		{"web/static/a.ts", "", false}, {"web/static/a.tsx", "", false},
		{"web/static/a.mjs", "", false}, {"web/static/a.cjs", "", false},
		{"package.json", "", false}, {"a.py", "", false}, {"a.sh", "", false}, {"a.rs", "", false},
		{"cmd/seed/package.json", "", false}, {"cmd/seed/package-lock.json", "", false},
		{"cmd/seed/npm-shrinkwrap.json", "", false}, {".github/workflows/pnpm-lock.yaml", "", false},
		{"package-lock.json", "", false}, {"yarn.lock", "", false}, {"Cargo.lock", "", false},
		{"web/static/vendor/unknown.js", "", false}, {"web/static/vendor/a.go", "", false},
		{"web/static/vendor/unknown-LICENSE", "", false}, {"docs/vendor/a.md", "", false},
		{"internal/web/testdata/a.go", "", false}, {"internal/web/testdata/a.txt", "", false},
		{"cmd/argonreview/testdata/other.json", "[]", false},
		{"cmd/argonreview/cases.json", "[]", false},
		{"cmd/argonreview/testdata/implement-guard/unknown.json", "{}", false},
		{"cmd/argonreview/testdata/implement-guard/other.jsonl", "{}\n", false},
		{"cmd/argonreview/hooks-off.json", "{}", false},
		{"cmd/argonreview/hooks-on.json", "{}", false},
		{"cmd/argonreview/settings-off.json", "{}", false},
		{"cmd/argonreview/settings-on.json", "{}", false},
		{"cmd/argonreview/mcp-config-on.json", "{}", false},
		{"cmd/argonreview/real-agy-calls.jsonl", "{}\n", false},
		{"cmd/other/testdata/implement-guard/hooks-off.json", "{}", false},
		{"cmd/other/testdata/implement-guard/hooks-on.json", "{}", false},
		{"cmd/other/testdata/implement-guard/settings-off.json", "{}", false},
		{"cmd/other/testdata/implement-guard/settings-on.json", "{}", false},
		{"cmd/other/testdata/implement-guard/mcp-config-on.json", "{}", false},
		{"cmd/other/testdata/implement-guard/real-agy-calls.jsonl", "{}\n", false},
		{"elsewhere/events.jsonl", "{}\n", false},
		{"internal/web/testdata/browser-shard-totals.txt", "#!/bin/sh\n", false},
		{"web/static/vendor/htmx-2.0.7.min.js", "#!/bin/sh\n", false},
		{"internal/identity/testdata/fuzz/FuzzParse/a", "", false},
		{"internal/identity/testdata/fuzz/FuzzParse/a", "go test fuzz v2\n", false},
		{"internal/identity/testdata/fuzz/FuzzParse/a", "\ngo test fuzz v1\n", false},
		{"internal/identity/testdata/fuzz/FuzzParse/a", "go test fuzz v1extra\n", false},
		{"internal/identity/testdata/fuzz/FuzzParse/a", "#!/bin/sh\n", false},
		{"internal/identity/testdata/fuzz/FuzzParse/a.txt", "go test fuzz v1\n", false},
		{"internal/identity/testdata/fuzz/FuzzParse/nested/a", "go test fuzz v1\n", false},
		{"internal/identity/testdata/fuzz/Parse/a", "go test fuzz v1\n", false},
		{"internal/identity/testdata/fuzz/a", "go test fuzz v1\n", false},
		{"elsewhere/testdata/fuzz/FuzzParse/a", "go test fuzz v1\n", false},
	} {
		t.Run(tt.name+"/"+tt.source, func(t *testing.T) {
			problems := inspect(t, map[string]string{tt.name: tt.source})
			if (len(problems) == 0) != tt.allowed {
				t.Fatalf("allowed = %v, problems = %v", tt.allowed, problems)
			}
		})
	}
}

const approval = " #1437 https://github.com/tkakkie/ribbitto/issues/1437#issuecomment-123"

func TestExceptions(t *testing.T) {
	for _, tt := range []struct{ name, line, file, source, want string }{
		{"exact", "helper.py" + approval, "helper.py", "", ""},
		{"spaces", "a space.py" + approval, "a space.py", "", ""},
		{"reason separator in path", "a -- b.py" + approval, "a -- b.py", "", ""},
		{"separator in reason", "a.py" + approval + " -- a -- b", "a.py", "", ""},
		{"only issue", "#1437", "", "", "want <exact path>"},
		{"no URL", "helper.py #1437", "helper.py", "", "want <exact path>"},
		{"wildcard", "*.py" + approval, "a.py", "", "needs -- <reason>"},
		{"wildcard reason", "*.py" + approval + " -- necessary", "a.py", "", "not tracked"},
		{"literal wildcard reason", "*.py" + approval + " -- literal filename", "*.py", "", ""},
		{"braces", "{a,b}.py" + approval, "{a,b}.py", "", "needs -- <reason>"},
		{"directory", "scripts" + approval, "scripts/a.py", "", "needs -- <reason>"},
		{"directory slash", "scripts/" + approval, "scripts/a.py", "", "needs -- <reason>"},
		{"directory reason", "scripts" + approval + " -- necessary", "scripts/a.py", "", "not tracked"},
		{"bad URL", "a.py #1437 http://github.com/tkakkie/ribbitto/issues/1437#issuecomment-123", "a.py", "", "want <exact path>"},
		{"different issue", "a.py #12 https://github.com/tkakkie/ribbitto/issues/1437#issuecomment-123", "a.py", "", "same issue"},
		{"wrong repo", "a.py #1437 https://github.com/other/ribbitto/issues/1437#issuecomment-123", "a.py", "", "want <exact path>"},
		{"URL suffix", "a.py" + approval + "?x=y", "a.py", "", "want <exact path>"},
		{"URL nondigits", "a.py #1437 https://github.com/tkakkie/ribbitto/issues/1437#issuecomment-a", "a.py", "", "want <exact path>"},
		{"duplicate", "a.py" + approval + "\na.py" + approval, "a.py", "", "duplicate"},
		{"stale", "gone.py" + approval, "", "", "not tracked"},
		{"already allowed", "a.go" + approval, "a.go", "", "already allowed"},
		{"unknown tracked", "a.unknown" + approval, "a.unknown", "", ""},
		{"unknown unapproved", "", "a.unknown", "", "not in the language table"},
		{"no shebang exception", "a.py" + approval, "a.py", "#!anything", "shebang"},
		{"empty reason", "a.py" + approval + " -- ", "a.py", "", "empty exception reason"},
		{"blank and comment", "\t \n# Comment\n", "", "", ""},
		{"indented comment", " # Comment", "", "", "want <exact path>"},
		{"bare hash", "#", "", "", "want <exact path>"},
		{"invalid path", "../a.py" + approval, "", "", "invalid exception path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{exceptionsFile: tt.line + "\n"}
			if tt.file != "" {
				files[tt.file] = tt.source
			}
			problems := inspect(t, files)
			if tt.want == "" && len(problems) != 0 || tt.want != "" && !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Fatalf("want %q; got %v", tt.want, problems)
			}
		})
	}
}

func TestListing(t *testing.T) {
	for _, tt := range []struct {
		name, list string
		valid      bool
	}{
		{"NUL", "a space.go\x00a\nnewline.go\x00", true},
		{"empty", "", false}, {"unterminated", "a.go", false},
		{"empty name", "\x00", false}, {"absolute", "/a.go\x00", false},
		{"parent", "a/../a.go\x00", false}, {"dot", "a/./a.go\x00", false},
		{"empty component", "a//a.go\x00", false}, {"invalid UTF8", "\xff.go\x00", false},
		{"duplicate", "a.go\x00a.go\x00", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseListing([]byte(tt.list))
			if (err == nil) != tt.valid {
				t.Fatalf("valid = %v; got %v", tt.valid, err)
			}
		})
	}
}

func initRepo(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "--", "."}} {
		if _, err := git(root, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitListing(t *testing.T) {
	files := map[string]string{"a space.go": "", "a\nnewline.go": "", "z\nunknown.py": "", "b unknown.sh": ""}
	root := fixture(t, files)
	initRepo(t, root)
	names, err := tracked(root)
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		found := false
		for _, candidate := range names {
			found = found || candidate == name
		}
		if !found {
			t.Fatalf("git listing lost %q: %q", name, names)
		}
	}
	problems, err := check(root)
	if err != nil || len(problems) != 2 {
		t.Fatalf("check = %v, %v", problems, err)
	}
	if !strings.Contains(problems[0], "b unknown.sh") || !strings.Contains(problems[1], `z\nunknown.py`) {
		t.Fatalf("not sorted/quoted: %v", problems)
	}
}

func TestExceptionsFileTracked(t *testing.T) {
	for _, state := range []string{"tracked", "untracked", "ignored", "absent"} {
		t.Run(state, func(t *testing.T) {
			root := fixture(t, map[string]string{
				"helper.py":    "",
				exceptionsFile: "helper.py" + approval + "\n",
				".gitignore":   exceptionsFile + "\n",
			})
			initRepo(t, root)
			if state == "tracked" {
				if _, err := git(root, "add", "-f", "--", exceptionsFile); err != nil {
					t.Fatal(err)
				}
			} else if state == "untracked" {
				if err := os.WriteFile(filepath.Join(root, ".gitignore"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if state == "absent" {
				if err := os.Remove(filepath.Join(root, exceptionsFile)); err != nil {
					t.Fatal(err)
				}
			}
			problems, err := check(root)
			if err != nil {
				t.Fatal(err)
			}
			if state == "tracked" {
				if len(problems) != 0 {
					t.Fatalf("tracked approval refused: %v", problems)
				}
				return
			}
			joined := strings.Join(problems, "\n")
			if len(problems) != 2 || !strings.Contains(joined, exceptionsFile+": exceptions file is not tracked; an untracked file cannot approve anything") || !strings.Contains(joined, `"helper.py": path and kind are not in the language table`) {
				t.Fatalf("want exceptions-file refusal and helper refusal; got %v", problems)
			}
		})
	}
}

func TestGitEnvironment(t *testing.T) {
	other := fixture(t, map[string]string{"other.go": ""})
	initRepo(t, other)
	root := fixture(t, map[string]string{"target.go": ""})
	initRepo(t, root)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	names, err := tracked(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, "\x00"); got != exceptionsFile+"\x00target.go" {
		t.Fatalf("poisoned environment changed listing: %q", names)
	}
	if problems, err := check(root); err != nil || len(problems) != 0 {
		t.Fatalf("check with poisoned environment: %v, %v", problems, err)
	}
}

func TestUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "FIFO", "missing", "parent symlink", "exception symlink", "exception FIFO"} {
		t.Run(kind, func(t *testing.T) {
			root := fixture(t, map[string]string{"a.go": "", "dir/b.go": ""})
			initRepo(t, root)
			target := filepath.Join(root, "a.go")
			if strings.HasPrefix(kind, "exception") {
				target = filepath.Join(root, exceptionsFile)
			}
			if kind == "parent symlink" {
				if err := os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "other")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other", filepath.Join(root, "dir")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink", "exception symlink":
					if err := os.Symlink("dir/b.go", target); err != nil {
						t.Fatal(err)
					}
				case "FIFO", "exception FIFO":
					if err := syscall.Mkfifo(target, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			// The index still tracks the path even when the working-tree file becomes
			// a FIFO; git add itself correctly refuses to index special files.
			done := make(chan error, 1)
			go func() {
				problems, err := check(root)
				if err == nil && len(problems) == 0 {
					err = errors.New("unsafe or missing file passed")
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("check blocked on a special file")
			}
		})
	}
}

func TestFailures(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*testing.T) string
	}{
		{"git failure", func(t *testing.T) string { return t.TempDir() }},
		{"missing exceptions", func(t *testing.T) string {
			root := fixture(t, map[string]string{"a.go": ""})
			initRepo(t, root)
			if err := os.Remove(filepath.Join(root, exceptionsFile)); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"exception scanner error", func(t *testing.T) string {
			root := fixture(t, map[string]string{exceptionsFile: strings.Repeat("x", 100_000)})
			initRepo(t, root)
			return root
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems, err := check(tt.setup(t))
			if err == nil && len(problems) == 0 {
				t.Fatal("failure passed")
			}
		})
	}
}

func TestBaseline(t *testing.T) {
	// This reads the working tree's tracked files, which is what CI checks out,
	// rather than a historical blob set that could miss newly introduced files.
	out, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSuffix(string(out), "\n")
	t.Run("working tree", func(t *testing.T) { assertBaseline(t, root) })
	t.Run("approved exception", func(t *testing.T) {
		root := fixture(t, map[string]string{"helper.py": ""})
		// Amend a fixture copy; never add an exception to the live repository.
		name := filepath.Join(root, exceptionsFile)
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, append(content, []byte("helper.py"+approval+"\n")...), 0600); err != nil {
			t.Fatal(err)
		}
		initRepo(t, root)
		assertBaseline(t, root)
	})
}

func assertBaseline(t *testing.T, root string) {
	t.Helper()
	problems, err := check(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("baseline: %v", problems)
	}
	repo, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	names, err := tracked(root)
	if err != nil {
		t.Fatal(err)
	}
	trackedNames := map[string]bool{}
	for _, name := range names {
		trackedNames[name] = true
	}
	// Today's zero-exception baseline is evidence in the PR body and the empty
	// exceptions file, not an assertion: approved entries must keep passing.
	_, problems = readExceptions(repo, trackedNames)
	if len(problems) != 0 {
		t.Fatalf("baseline exceptions: %v", problems)
	}
}

func TestProblemOrder(t *testing.T) {
	problems := inspect(t, map[string]string{"a\nz.py": "", "a b.py": ""})
	if len(problems) != 2 || !strings.HasPrefix(problems[0], `"a\nz.py"`) || !strings.HasPrefix(problems[1], `"a b.py"`) {
		t.Fatalf("not sorted by git path: %v", problems)
	}
}
