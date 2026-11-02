package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
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
	t.Run("moduleScopeWarning", func(t *testing.T) {
		control := t.TempDir()
		for name, content := range map[string]string{
			".node-version": string(pin),
			"package.json":  `{"private":true}` + "\n",
			"web/src/probe.ts": `export const value: number = 1;
`,
		} {
			full := filepath.Join(control, name)
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		writeHarness(t, control, "probe.test.mjs", `import test from 'node:test'; import assert from 'node:assert/strict'; import { value } from '../src/probe.ts'; test('module scope probe', () => assert.equal(value, 1));`)
		out, err := runOutput(t, "test", control)
		if err != nil || !strings.Contains(out, "MODULE_TYPELESS_PACKAGE_JSON") {
			t.Fatalf("expected MODULE_TYPELESS_PACKAGE_JSON without web/src/package.json, got %v: %s", err, out)
		}
		if err := os.WriteFile(filepath.Join(control, "web/src/package.json"), []byte(`{"type": "module"}`+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		out, err = runOutput(t, "test", control)
		if err != nil || strings.Contains(out, "MODULE_TYPELESS_PACKAGE_JSON") {
			t.Fatalf("unexpected MODULE_TYPELESS_PACKAGE_JSON with web/src/package.json, got %v: %s", err, out)
		}
		out, err = runOutput(t, "test", "../..")
		if err != nil || strings.Contains(out, "MODULE_TYPELESS_PACKAGE_JSON") {
			t.Fatalf("repository unit tests failed or warned: %v\n%s", err, out)
		}
	})
}

func TestModuleScopeManifest(t *testing.T) {
	validScope := func(data []byte) bool {
		return strings.TrimSpace(string(data)) == `{"type": "module"}`
	}
	data, err := os.ReadFile("../../web/src/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if !validScope(data) {
		t.Fatalf("web/src/package.json = %q; want exact {\"type\": \"module\"}", data)
	}
	for _, invalid := range []struct {
		name string
		raw  string
	}{
		{"escapedKey", `{"\u0074ype": "module"}`},
		{"duplicateKey", `{"type": "commonjs", "type": "module"}`},
		{"duplicateEscapedKey", `{"type": "commonjs", "\u0074ype": "module"}`},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if validScope([]byte(invalid.raw)) {
				t.Fatalf("accepted invalid module scope manifest %s", invalid.raw)
			}
		})
	}
	data, err = os.ReadFile("../../package.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["type"]; ok {
		t.Fatal("root package.json must not set type")
	}
	out, err := exec.Command("git", "-C", "../..", "ls-files", "-z", "--", "web").Output()
	if err != nil {
		t.Fatal(err)
	}
	var manifests []string
	for _, name := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if path.Base(name) == "package.json" {
			manifests = append(manifests, name)
		}
	}
	if !slices.Equal(manifests, []string{"web/src/package.json"}) {
		t.Fatalf("tracked web package.json files = %q, want [\"web/src/package.json\"]", manifests)
	}
}
