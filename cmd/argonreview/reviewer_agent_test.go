package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReviewerAgentBytes(t *testing.T) {
	const want = `---
name: argon-reviewer
description: Read-only reviewer for ribbitto; reads and searches the checkout, changes nothing.
mainAgent: true
tools:
  - view_file
  - list_dir
  - grep_search
  - find_by_name
---

# Argon reviewer

You review one pull request or issue. You can only read and search files in the current checkout.
`
	if reviewerAgent != want {
		t.Fatal("agent constant differs from the reviewed bytes")
	}
	account, unchanged := fixture(t)
	home := resolved(t, t.TempDir())
	path := filepath.Join(home, ".gemini/config/agents/argon-reviewer.md")
	write(t, filepath.Join(home, "kept"), "untouched")
	for _, modified := range []bool{false, true} {
		if modified {
			write(t, path, "untrusted agent")
		}
		if _, err := prepare(home, account, guardBinary); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Fatalf("agent = %q, %v", data, err)
		}
		if data, _ := os.ReadFile(filepath.Join(home, "kept")); string(data) != "untouched" {
			t.Fatal("other home file changed")
		}
	}
	change := func() { write(t, path, "changed after writing") }
	if _, err := prepareWith(home, account, guardBinary, hooks{afterWrite: change}); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("changed agent accepted: %v", err)
	}
	unchanged()
}

func TestReviewerAgentTools(t *testing.T) {
	// Parse this deliberately restricted front matter without a YAML dependency.
	parts := strings.Split(reviewerAgent, "---\n")
	if len(parts) != 3 || parts[0] != "" {
		t.Fatal("invalid front matter")
	}
	keys, tools := map[string]string{}, map[string]bool{}
	key := ""
	for _, line := range strings.Split(strings.TrimSuffix(parts[1], "\n"), "\n") {
		if strings.HasPrefix(line, "  - ") && key == "tools" {
			tool := strings.TrimPrefix(line, "  - ")
			if tools[tool] {
				t.Fatal("duplicate tool")
			}
			tools[tool] = true
			continue
		}
		var value string
		var ok bool
		key, value, ok = strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != key {
			t.Fatalf("unexpected front matter: %q", line)
		}
		if _, duplicate := keys[key]; duplicate {
			t.Fatal("duplicate key")
		}
		keys[key] = strings.TrimSpace(value)
	}
	wantKeys := map[string]string{"name": reviewerAgentName, "description": "Read-only reviewer for ribbitto; reads and searches the checkout, changes nothing.", "mainAgent": "true", "tools": ""}
	wantTools := map[string]bool{"view_file": true, "list_dir": true, "grep_search": true, "find_by_name": true}
	if !reflect.DeepEqual(keys, wantKeys) || !reflect.DeepEqual(tools, allowedTools) || !reflect.DeepEqual(allowedTools, wantTools) {
		t.Fatalf("keys=%v tools=%v guard=%v", keys, tools, allowedTools)
	}
	for _, name := range []string{"read_file", "write_file", "run_command", "search_web", "invoke_subagent"} {
		if allow, _ := decide([]byte(`{"toolCall":{"name":"` + name + `"}}`)); allow {
			t.Errorf("guard allowed %s", name)
		}
	}
}

func TestPrepareRefusesReviewerAgentTypes(t *testing.T) {
	for _, kind := range []string{"symlink", "FIFO", "directory", "agents symlink", "agents file"} {
		t.Run(kind, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := resolved(t, t.TempDir())
			path := filepath.Join(home, ".gemini/config/agents")
			if !strings.HasPrefix(kind, "agents ") {
				path = filepath.Join(path, "argon-reviewer.md")
			}
			mkdir(t, filepath.Dir(path))
			switch kind {
			case "symlink", "agents symlink":
				symlink(t, resolved(t, t.TempDir()), path)
			case "FIFO":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				mkdir(t, path)
			default:
				write(t, path, "untrusted")
			}
			write(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), "mine")
			_, err := prepare(home, account, guardBinary)
			want := "not a regular file"
			if strings.HasPrefix(kind, "agents ") {
				want = "customisation"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %s", err, want)
			}
			if data, _ := os.ReadFile(filepath.Join(home, ".gemini/antigravity-cli/settings.json")); string(data) != "mine" {
				t.Fatal("settings rewritten before refusal")
			}
			unchanged()
		})
	}
}

func TestPrepareRefusesOtherAgents(t *testing.T) {
	for _, name := range []string{"other.md", "argon-implementer.md", "subdirectory", ".hidden", "Argon-Reviewer.md"} {
		t.Run(name, func(t *testing.T) {
			account, _ := fixture(t)
			home := resolved(t, t.TempDir())
			path := filepath.Join(home, ".gemini/config/agents", name)
			if name == "subdirectory" {
				mkdir(t, path)
			} else {
				write(t, path, "untrusted")
			}
			write(t, filepath.Join(home, prepareLockName), "")
			before := snapshot(t, home)
			if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "customisation") || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "remove it by hand") {
				t.Fatalf("err = %v", err)
			}
			if !equal(before, snapshot(t, home)) {
				t.Fatal("refusal changed HOME")
			}
		})
	}
}

func TestPrepareRechecksAgents(t *testing.T) {
	home := resolved(t, t.TempDir())
	write(t, filepath.Join(home, ".gemini/config/agents/argon-reviewer.md"), reviewerAgent)
	root, err := walk(home)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFD(root)
	gemini, dirs, err := openOwned(root)
	defer closeAll(gemini, dirs)
	if err != nil {
		t.Fatal(err)
	}
	files := ownedFiles(t, guardBinary)
	if err := checkBeforeWrite(root, dirs, files); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".gemini/config/agents/other.md"), "untrusted")
	if err := checkBeforeWrite(root, dirs, files); err == nil || !strings.Contains(err.Error(), "customisation") {
		t.Fatalf("recheck = %v", err)
	}
}

func TestPrepareRefusesSharedAgentsOverlap(t *testing.T) {
	for _, relation := range []string{"same", "inside", "contains"} {
		t.Run(relation, func(t *testing.T) {
			account := resolved(t, t.TempDir())
			shared := resolved(t, t.TempDir())
			home := shared
			switch relation {
			case "inside":
				home = filepath.Join(shared, "home")
			case "contains":
				shared = filepath.Join(home, "agents")
			}
			mkdir(t, shared)
			mkdir(t, home)
			// Only agents aliases this tree; the shared config itself is separate.
			symlink(t, shared, filepath.Join(account, ".gemini/config/agents"))
			write(t, filepath.Join(home, prepareLockName), "")
			before := snapshot(t, home)
			if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "overlaps") {
				t.Fatalf("err = %v", err)
			}
			if !equal(before, snapshot(t, home)) {
				t.Fatal("overlap refusal changed HOME")
			}
		})
	}
}
