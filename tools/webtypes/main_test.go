package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWrongNode(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{".node-version": "26.8.1\n", "node": "#!/bin/sh\nprintf 'v0.0.0\\n'\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root)
	for _, mode := range []string{"install", "check"} {
		t.Setenv("RIBBITTO_REQUIRE_NODE", "1")
		if err := run(mode, root); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	t.Setenv("RIBBITTO_REQUIRE_NODE", "")
	if err := run("check", root); err != nil {
		t.Fatal(err)
	}
}

func TestInstallEnvironment(t *testing.T) {
	for _, key := range []string{"npm_config_omit", "NPM_CONFIG_OS", "npm_config_cpu", "npm_config_libc", "npm_config_ignore-scripts", "NODE_OPTIONS"} {
		t.Setenv(key, "untrusted")
	}
	t.Setenv("npm_config_omit", "optional")
	t.Setenv("npm_config_http-proxy", "http://localhost:1234")
	env := strings.Join(environment("/repo"), "\n")
	if strings.Contains(env, "untrusted") || strings.Contains(env, "npm_config_omit=") || !strings.Contains(env, "npm_config_http-proxy=http://localhost:1234") || !strings.Contains(env, "npm_config_cache=/repo/bin/.cache/npm") {
		t.Fatalf("unsafe install environment")
	}
}

func TestLockfile(t *testing.T) {
	data, err := os.ReadFile("../../package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages map[string]struct {
			Resolved, Integrity string
			HasInstallScript    bool
		}
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux-x64", "darwin-arm64"} {
		if _, ok := lock.Packages["node_modules/@typescript/typescript-"+platform]; !ok {
			t.Fatalf("missing %s", platform)
		}
	}
	if _, ok := lock.Packages["node_modules/typescript"]; !ok {
		t.Fatal("missing portable checker")
	}
	for name, pkg := range lock.Packages {
		if name == "" {
			continue // The root is not a downloaded package.
		}
		if (name != "node_modules/typescript" && !strings.HasPrefix(name, "node_modules/@typescript/")) || !strings.HasPrefix(pkg.Resolved, "https://registry.npmjs.org/") || pkg.Integrity == "" || pkg.HasInstallScript {
			t.Errorf("unsafe lock entry %s", name)
		}
	}
}

func TestRealTypeScript(t *testing.T) {
	if os.Getenv("RIBBITTO_REQUIRE_NODE") != "1" {
		t.Skip("real checker runs in web-types after explicit install")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := ready(root); err != nil {
		t.Fatal(err)
	}
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if _, err := os.Stat(filepath.Join(root, "node_modules/@typescript/typescript-"+runtime.GOOS+"-"+arch)); err != nil {
		t.Fatalf("optional platform package missing after install with npm_config_omit=optional: %v", err)
	}
	input := filepath.Join(t.TempDir(), "invalid.ts")
	if err := os.WriteFile(input, []byte("const value: string = 123;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := command(root, "node", "node_modules/typescript/bin/tsc", "--ignoreConfig", "--strict", "--noEmit", input).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "TS2322") {
		t.Fatalf("expected TS2322, got %v: %s", err, out)
	}
}
