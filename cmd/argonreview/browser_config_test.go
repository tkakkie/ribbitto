package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// browserMCPGolden is independent of the generator and leaves the old golden intact.
const browserMCPGolden = `{
  "mcpServers": {
    "ribbitto-verify": {
      "args": [
        "-i",
        "PATH=/usr/bin:/bin",
        "/trusted/bin/argonreview",
        "verify-mcp",
        "--trusted-source",
        "/trusted/source",
        "--worktree",
        "/trusted/worktree",
        "--browser-mode",
        "/trusted/browser-mode"
      ],
      "command": "/usr/bin/env",
      "disabled": false
    }
  }
}
`

// TestImplementConfigBrowserMode covers all five host interfaces and the full
// generated-file comparison, including an otherwise-correct extra switch path.
func TestImplementConfigBrowserMode(t *testing.T) {
	t.Run("goldens", func(t *testing.T) {
		for _, path := range []string{"", "/trusted/browser-mode"} {
			data, err := implementerMCPConfig(true, "/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree", path)
			want := implementVerifyGolden(t, "mcp-config-on.json")
			if path != "" {
				want = []byte(browserMCPGolden)
			}
			if err != nil || !bytes.Equal(data, want) {
				t.Fatalf("MCP golden path=%q differs: %s %v", path, data, err)
			}
			h, s, m, err := implementConfigFiles(true, "/trusted/bin/argonreview", "/trusted/guard", "/trusted/source", "/trusted/worktree", path)
			if err != nil || !bytes.Equal(m, want) || !bytes.Equal(h, implementVerifyGolden(t, "hooks-on.json")) || !bytes.Equal(s, implementVerifyGolden(t, "settings-on.json")) {
				t.Fatalf("config golden differs: %v", err)
			}
		}
		if data, err := implementerMCPConfig(false, "", "", "", ""); err != nil || len(data) != 0 {
			t.Fatalf("off bytes=%q err=%v", data, err)
		}
	})
	for _, kind := range []string{"valid", "missing", "different", "extra"} {
		t.Run("comparison/"+kind, func(t *testing.T) {
			expected, observed := "/trusted/browser-mode", "/trusted/browser-mode"
			switch kind {
			case "missing":
				observed = ""
			case "different":
				observed += "-other"
			case "extra":
				expected = ""
			}
			h, s, m, err := implementConfigFiles(true, "/trusted/bin/argonreview", "/trusted/guard", "/trusted/source", "/trusted/worktree", observed)
			if err != nil {
				t.Fatal(err)
			}
			err = checkImplementerConfig(true, h, s, m, "/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree", "/trusted/guard", expected)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("browser path comparison %s: %v", kind, err)
			}
		})
	}
	// Each parser must refuse each bad value before filesystem or trust checks
	// can accidentally mask missing character validation.
	for _, kind := range []string{"valid", "repeated", "relative", "unclean", "root", "UTF8", "control", "quote", "empty", "missing-value", "off"} {
		t.Run("parsing/"+kind, func(t *testing.T) {
			f := newImplementConfigFixture(t, true)
			path := "/trusted/browser-mode"
			switch kind {
			case "relative":
				path = "relative"
			case "unclean":
				path = "/a/../b"
			case "root":
				path = "/"
			case "UTF8":
				path = "/a\xffb"
			case "control":
				path = "/a\tb"
			case "quote":
				path = "/a'b"
			case "empty":
				path = ""
			}
			suffix := []string{"--browser-mode", path}
			if kind == "repeated" {
				suffix = append(suffix, suffix...)
			}
			if kind == "missing-value" {
				suffix = suffix[:1]
			}
			args := f.args()
			if kind == "off" {
				f = newImplementConfigFixture(t, false)
				args = f.args()
			}
			c, err := parseImplementConfig(append(args, suffix...))
			if (err == nil) != (kind == "valid") {
				t.Fatalf("config parser %s: %v", kind, err)
			}
			if kind == "valid" && c.browserMode != path {
				t.Fatal("config parser lost browser path")
			}
			x := newImplementRun(t)
			if kind != "off" {
				featureOn(t, x)
			}
			runArgs := append(append([]string{}, x.args...), suffix...)
			r, err := implementRunArguments(runArgs, io.Discard, x.deps)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("run parser %s: %v", kind, err)
			}
			if kind == "valid" && r.browserMode != path {
				t.Fatal("run parser lost browser path")
			}
			checkArgs := x.f.args()
			if kind != "off" {
				checkArgs = featureFlag(checkArgs, "verify", "on")
				checkArgs = append(checkArgs, "--trusted-source", f.c.source)
			}
			// Deliberate missing required argument makes valid browser parsing return
			// its later required-argument diagnostic, without invoking process checks.
			checkArgs = featureFlag(checkArgs, "home", "<absent>")
			var log bytes.Buffer
			code := implementCheckCommand(append(checkArgs, suffix...), io.Discard, &log, x.f.deps)
			if code != 2 || (strings.Contains(log.String(), "--home, --worktree") != (kind == "valid")) {
				t.Fatalf("check parser %s: exit=%d log=%s", kind, code, &log)
			}
			if kind == "off" {
				return
			}
			source, tree := resolved(t, t.TempDir()), resolved(t, t.TempDir())
			args = append([]string{"--trusted-source", source, "--worktree", tree}, suffix...)
			log.Reset()
			trusted := false
			code = startVerifyMCP(context.Background(), args, nil, func(string) error { trusted = true; return nil }, noSweep, noVerify(t), strings.NewReader(""), io.Discard, &log)
			if (code == 0) != (kind == "valid") || trusted != (kind == "valid") {
				t.Fatalf("MCP parser %s: exit=%d trust=%t log=%s", kind, code, trusted, &log)
			}
			// The real command fails its existing trusted-build gate only after parsing.
			log.Reset()
			code = containerCheckCommand(append([]string{"/tree", "test-browser", "./internal/web", browserTestRun}, suffix...), io.Discard, &log)
			want := 2
			if kind == "valid" {
				want = 1
			}
			if code != want {
				t.Fatalf("container parser %s: exit=%d want=%d log=%s", kind, code, want, &log)
			}
		})
	}
	for _, kind := range []string{"valid", "missing", "different", "extra"} {
		t.Run("implement-check/"+kind, func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			path := filepath.Join(x.f.root, "browser-mode")
			x.set("browser-mode", path)
			r, err := implementRunArguments(x.args, io.Discard, x.deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.validate(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(r.mcp, []byte(path)) {
				t.Fatal("implement-run generated MCP lost browser path")
			}
			observed, expected := path, path
			switch kind {
			case "missing":
				observed = ""
			case "different":
				observed += "-other"
			case "extra":
				expected = ""
			}
			m, err := implementerMCPConfig(true, "/trusted/argonreview", r.source, r.tree, observed)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range r.files()[:3] {
				data := file.data
				if file.name == "mcp" {
					data = m
				}
				write(t, file.path, string(data))
			}
			args := featureFlag(x.f.args(), "verify", "on")
			args = append(args, "--trusted-source", r.source)
			if expected != "" {
				args = append(args, "--browser-mode", expected)
			}
			var out bytes.Buffer
			code := implementCheckCommand(args, &out, &out, x.f.deps)
			if (code == 0) != (kind == "valid") {
				t.Fatalf("implement-check browser path %s: code=%d out=%s", kind, code, &out)
			}
		})
	}
	t.Run("run-forwards-on-only", func(t *testing.T) {
		x := newImplementRun(t)
		featureOn(t, x)
		x.set("browser-mode", "/trusted/browser-mode")
		r, err := implementRunArguments(x.args, io.Discard, x.deps)
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"on", "off"} {
			r.deps.check = func(args []string, _, _ io.Writer) int {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "--browser-mode /trusted/browser-mode") != (state == "on") {
					t.Fatalf("forwarded args for %s: %v", state, args)
				}
				return 0
			}
			if code := r.checkState(state, io.Discard); code != 0 {
				t.Fatal(code)
			}
		}
	})
	t.Run("config-command", func(t *testing.T) {
		f := newImplementConfigFixture(t, true)
		path := filepath.Join(f.root, "browser-mode")
		var log bytes.Buffer
		if code := implementConfigCommand(append(f.args(), "--browser-mode", path), io.Discard, &log); code != 0 {
			t.Fatalf("config exit=%d log=%s", code, &log)
		}
		data, err := os.ReadFile(filepath.Join(f.c.out, "mcp_config.json"))
		if err != nil || !bytes.Contains(data, []byte(path)) {
			t.Fatalf("published MCP missing switch: %s %v", data, err)
		}
	})
	t.Run("usage-and-admission", func(t *testing.T) {
		var log bytes.Buffer
		if code := run(nil, nil, io.Discard, &log); code != 2 {
			t.Fatal(code)
		}
		if strings.Count(log.String(), "--browser-mode") != 6 || !strings.Contains(log.String(), browserRecordUsage) || !strings.Contains(verifyMCPUsage, "--browser-mode") || !strings.Contains(implementConfigUsage, "--browser-mode") || !strings.Contains(implementRunUsage, "--browser-mode") {
			t.Fatalf("usage missing browser flags: %s", &log)
		}
		if err := validateContainerCheck("/tree", "test-browser", "./internal/web", "TestBrowser/a"); err != nil {
			t.Fatal("valid test-browser runner refused")
		}
		if _, err := parseVerifyArguments([]byte(`{"check":"test-browser","package":"./internal/web","run":"TestBrowser/a"}`)); err != nil {
			t.Fatal("valid browser MCP refused")
		}
		v := newContainerVerifier("unused", "")
		if got := v.browserAdmission(); got != browserPaused {
			t.Fatalf("absent flag admitted: %q", got)
		}
	})
}
