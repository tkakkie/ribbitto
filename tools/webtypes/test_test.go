package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeHarness(t *testing.T, root, name, source string) string {
	t.Helper()
	path := filepath.Join(root, "web/test", name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNodeTestCommand(t *testing.T) {
	if root := os.Getenv("WEBTYPES_TEST_MAIN"); root != "" {
		os.Args = []string{"webtypes", "test", root}
		main()
		return
	}
	root, _ := fakeNodeRoot(t)
	a := writeHarness(t, root, "a.test.mjs", "")
	b := writeHarness(t, root, "b.test.mjs", "")
	writeHarness(t, root, "ignored.mjs", "")
	writeHarness(t, root, "nested/ignored.test.mjs", "")
	writeHarness(t, root, "directory.test.mjs/nested.test.mjs", "")
	script := `#!/bin/sh
if [ "$1" = --version ]; then printf 'v26.8.1\n'; exit; fi
printf '%s\n' "$@" > args
printf '%s\n' "$PWD" > cwd
printf '%s' "${NODE_OPTIONS-unset}" > options
exit "${TEST_EXIT_CODE:-0}"
`
	if err := os.WriteFile(filepath.Join(root, "node"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_OPTIONS", "--invalid-inherited-option")
	t.Setenv("RIBBITTO_REQUIRE_NODE", "1")
	if err := run("test", root); err != nil {
		t.Fatal(err)
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"args": "--test\n" + a + "\n" + b + "\n", "cwd": physicalRoot + "\n", "options": "unset"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	t.Setenv("TEST_EXIT_CODE", "23")
	err = run("test", root)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("expected exit 23, got %v", err)
	}
	t.Setenv("WEBTYPES_TEST_MAIN", root)
	out, err := exec.Command(os.Args[0], "-test.run=^TestNodeTestCommand$").CombinedOutput()
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("CLI expected exit 23, got %v: %s", err, out)
	}
}

func TestNodeTestReadiness(t *testing.T) {
	for _, state := range []string{"wrong", "absent", "no-harness"} {
		t.Run(state, func(t *testing.T) {
			root, _ := fakeNodeRoot(t)
			marker := filepath.Join(root, "ran")
			switch state {
			case "wrong":
				script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf v0.0.0; else : > ran; fi\n"
				if err := os.WriteFile(filepath.Join(root, "node"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "no-harness":
				writeHarness(t, root, "directory.test.mjs/nested.test.mjs", "")
			case "absent":
				if err := os.Remove(filepath.Join(root, "node")); err != nil {
					t.Fatal(err)
				}
			}
			for _, required := range []string{"", "1"} {
				t.Setenv("RIBBITTO_REQUIRE_NODE", required)
				out, err := runOutput(t, "test", root)
				want := map[string]string{"wrong": "does not match", "absent": "unavailable", "no-harness": "no client unit-test harnesses found"}[state]
				if required == "1" || state == "no-harness" {
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("expected %s failure, got %v: %s", want, err, out)
					}
				} else if err != nil || !strings.Contains(out, "NOT VERIFIED") || !strings.Contains(out, want) {
					t.Fatalf("expected NOT VERIFIED %s, got %v: %s", want, err, out)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("tests ran before readiness: %v", err)
				}
			}
		})
	}
}

func TestRealNodeTests(t *testing.T) {
	if os.Getenv("RIBBITTO_REQUIRE_NODE") != "1" {
		t.Skip("real Node runs in web-types")
	}
	root := t.TempDir()
	pin, err := os.ReadFile("../../.node-version")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".node-version"), pin, 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"false", "true"} {
		writeHarness(t, root, "probe.test.mjs", `import test from 'node:test'; import assert from 'node:assert/strict'; test('wrapper probe', () => assert.ok(`+value+`));`)
		out, err := runOutput(t, "test", root)
		if value == "false" {
			if err == nil || !strings.Contains(out, "ERR_ASSERTION") {
				t.Fatalf("expected failing harness assertion, got %v: %s", err, out)
			}
		} else if err != nil || !strings.Contains(out, "wrapper probe") {
			t.Fatalf("expected passing harness, got %v: %s", err, out)
		}
	}
}
