package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const guardBinary = "/opt/argonreview/argonreview"

// fixture returns a fake account home whose .gemini holds shared settings,
// and a snapshot function that fails the test if anything in it changed.
func fixture(t *testing.T) (account string, unchanged func()) {
	t.Helper()
	account, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shared := map[string]string{
		".gemini/antigravity-cli/settings.json": `{"permissions":{"allow":["command(git diff)"]}}`,
		".gemini/config/hooks.json":             `{"tencle":{}}`,
	}
	for path, content := range shared {
		write(t, filepath.Join(account, path), content)
	}
	return account, func() {
		t.Helper()
		for path, want := range shared {
			if got, err := os.ReadFile(filepath.Join(account, path)); err != nil || string(got) != want {
				t.Fatalf("shared %s changed: %q, %v", path, got, err)
			}
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepare(t *testing.T) {
	account, unchanged := fixture(t)
	// A dedicated directory under the account home is allowed.
	home := filepath.Join(account, "dev", "agy-home")
	kept := map[string]string{
		".gemini/antigravity-cli/antigravity-oauth-token":  "token",
		".gemini/antigravity-cli/conversations/1.db":       "conversation",
		".gemini/config/projects/default-cli-project.json": "{}",
	}
	for path, content := range kept {
		write(t, filepath.Join(home, path), content)
	}
	// Antigravity's own rewrite, adding a trusted workspace, is replaced.
	write(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), `{"trustedWorkspaces":["/"]}`)
	got, err := prepare(home, account, guardBinary)
	if err != nil || got != home {
		t.Fatalf("prepare = %q, %v", got, err)
	}
	for _, f := range owned(guardBinary) {
		data, err := os.ReadFile(filepath.Join(home, ".gemini", f.dir, f.name))
		if err != nil || !bytes.Equal(data, f.content) {
			t.Fatalf("%s/%s = %q, %v", f.dir, f.name, data, err)
		}
	}
	for path, want := range kept {
		if data, err := os.ReadFile(filepath.Join(home, path)); err != nil || string(data) != want {
			t.Fatalf("kept %s changed: %q, %v", path, data, err)
		}
	}
	if !strings.Contains(string(owned(guardBinary)[1].content), `"'`+guardBinary+`' guard"`) {
		t.Fatal("the hook does not run this binary's guard")
	}
	unchanged()
}

func TestPrepareRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, account, home string) string // returns RIBBITTO_ARGON_HOME
		guard string
		want  string
	}{
		{"account home", func(_ *testing.T, account, _ string) string { return account }, "", "account home"},
		{"ancestor of the account home", func(_ *testing.T, account, _ string) string { return filepath.Dir(account) }, "", "account home"},
		{"the shared .gemini", func(_ *testing.T, account, _ string) string { return filepath.Join(account, ".gemini") }, "", "overlaps"},
		{"inside the shared .gemini", func(_ *testing.T, account, _ string) string { return filepath.Join(account, ".gemini/config") }, "", "overlaps"},
		{"a symlink to the shared .gemini", func(t *testing.T, account, home string) string {
			link := filepath.Join(home, "alias")
			symlink(t, filepath.Join(account, ".gemini"), link)
			return link
		}, "", "overlaps"},
		{"relative", func(_ *testing.T, _, _ string) string { return "agy-home" }, "", "absolute path"},
		{"unset", func(_ *testing.T, _, _ string) string { return "" }, "", "absolute path"},
		{"symlinked .gemini", func(t *testing.T, account, home string) string {
			symlink(t, filepath.Join(account, ".gemini"), filepath.Join(home, ".gemini"))
			return home
		}, "", "not a plain directory"},
		{"symlinked config", func(t *testing.T, account, home string) string {
			mkdir(t, filepath.Join(home, ".gemini"))
			symlink(t, filepath.Join(account, ".gemini/config"), filepath.Join(home, ".gemini/config"))
			return home
		}, "", "not a plain directory"},
		{"symlinked settings", func(t *testing.T, account, home string) string {
			mkdir(t, filepath.Join(home, ".gemini/antigravity-cli"))
			symlink(t, filepath.Join(account, ".gemini/antigravity-cli/settings.json"), filepath.Join(home, ".gemini/antigravity-cli/settings.json"))
			return home
		}, "", "not a regular file"},
		{"symlinked hooks", func(t *testing.T, account, home string) string {
			mkdir(t, filepath.Join(home, ".gemini/config"))
			symlink(t, filepath.Join(account, ".gemini/config/hooks.json"), filepath.Join(home, ".gemini/config/hooks.json"))
			return home
		}, "", "not a regular file"},
		{"unquotable guard path", func(_ *testing.T, _, home string) string { return home }, "/opt/it's/argonreview", "cannot be quoted"},
		{".agents", func(t *testing.T, _, home string) string { mkdir(t, filepath.Join(home, ".agents")); return home }, "", "customisation"},
		{"hooks in antigravity-cli", func(t *testing.T, _, home string) string {
			write(t, filepath.Join(home, ".gemini/antigravity-cli/hooks.json"), "{}")
			return home
		}, "", "customisation"},
		{"MCP in antigravity-cli", func(t *testing.T, _, home string) string {
			write(t, filepath.Join(home, ".gemini/antigravity-cli/mcp_config.json"), "{}")
			return home
		}, "", "customisation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := filepath.Join(account, "dev", "agy-home")
			mkdir(t, home)
			guard := guardBinary
			if tc.guard != "" {
				guard = tc.guard
			}
			if _, err := prepare(tc.setup(t, account, home), account, guard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			unchanged()
		})
	}
	for _, dir := range []string{"config", "antigravity-cli"} {
		for _, name := range refusedCustomisation {
			t.Run(dir+"/"+name, func(t *testing.T) {
				account, unchanged := fixture(t)
				home := filepath.Join(account, "dev", "agy-home")
				write(t, filepath.Join(home, ".gemini", dir, name), "{}")
				if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "customisation") {
					t.Fatalf("err = %v", err)
				}
				unchanged()
			})
		}
	}
}

// checkOwned refuses any content but the launcher's own.
func TestCheckOwned(t *testing.T) {
	settings := strings.Replace(settingsJSON, `"permissions": {`, `"permissions": {"allow": [], `, 1)
	for _, tc := range []struct{ name, dir, file, content string }{
		{"an allow rule", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, `"deny": [`, `"allow": ["command(git)"], "deny": [`, 1)},
		{"a read_file grant", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, `"deny": [`, `"allow": ["read_file(*)"], "deny": [`, 1)},
		{"an empty allow list", "antigravity-cli", "settings.json", settings},
		{"a trusted workspace", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, "{\n", "{\n  \"trustedWorkspaces\": [\"/\"],\n", 1)},
		{"a wrong deny list", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, `      "mcp(*)"`, `      "unsandboxed(*)"`, 1)},
		{"another hook", "config", "hooks.json", `{"other":{"PreToolUse":[]}}`},
		{"an MCP server", "config", "mcp_config.json", `{"mcpServers":{"x":{"command":"sh"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(fd) }()
			write(t, filepath.Join(dir, tc.file), tc.content)
			for _, f := range owned(guardBinary) {
				if f.dir == tc.dir && f.name == tc.file {
					if err := checkOwned(fd, tc.file, f.content); err == nil {
						t.Fatal("accepted")
					}
					return
				}
			}
			t.Fatal("not an owned file")
		})
	}
}

// prepare runs only after the trusted-build check: an unstamped test binary
// is refused before anything in HOME is created.
func TestPrepareChecksTheBuildFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIBBITTO_ARGON_HOME", home)
	if code := run([]string{"prepare"}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("HOME holds %v, %v", entries, err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	mkdir(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
