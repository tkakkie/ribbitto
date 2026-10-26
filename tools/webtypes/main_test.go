package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

func fakeNodeRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{".node-version", "package.json"} {
		data, err := os.ReadFile(filepath.Join("../..", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pin, err := os.ReadFile(filepath.Join(root, ".node-version"))
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'v" + strings.TrimSpace(string(pin)) + "\\n'; else printf 'CHECKER RAN\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(root, "node"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ DevDependencies struct{ TypeScript string } }
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	return root, pkg.DevDependencies.TypeScript
}

func TestInstallCommand(t *testing.T) {
	root, _ := fakeNodeRoot(t)
	// A symlinked Node must still select npm from its PATH directory.
	target := filepath.Join(t.TempDir(), "node")
	if err := os.Rename(filepath.Join(root, "node"), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "node")); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	for path, script := range map[string]string{
		filepath.Join(decoy, "npm"): "#!/bin/sh\nprintf decoy > decoy-ran\nexit 99\n",
		filepath.Join(root, "npm"): `#!/bin/sh
printf '%s\n' "$@" > npm-args
printf '%s\n' "$npm_config_cache" > npm-cache
: > npm-configs
for arg in "$@"; do
    case "$arg" in
        --userconfig=*|--globalconfig=*)
            config=${arg#*=}
            if [ -f "$config" ] && [ ! -s "$config" ]; then
                printf '%s=empty\n' "${arg%%=*}" >> npm-configs
            fi
            ;;
    esac
done
`,
	} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", decoy+string(os.PathListSeparator)+root)
	t.Setenv("npm_config_cache", "untrusted")
	if err := run("install", root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "decoy-ran")); !os.IsNotExist(err) {
		t.Fatalf("decoy npm ran or marker unreadable: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	args := strings.Split(strings.TrimSuffix(read("npm-args"), "\n"), "\n")
	want := []string{"ci", "--ignore-scripts", "--include=optional", "--include=dev", "--registry=https://registry.npmjs.org/"}
	if len(args) != len(want)+2 || !slices.Equal(args[:len(want)], want) {
		t.Fatalf("npm arguments = %q, want %q followed by two config overrides", args, want)
	}
	user := strings.TrimPrefix(args[5], "--userconfig=")
	global := strings.TrimPrefix(args[6], "--globalconfig=")
	if args[5] == user || args[6] == global || user == global || filepath.Dir(user) != filepath.Dir(global) || filepath.Dir(filepath.Dir(user)) != filepath.Join(root, "bin") {
		t.Fatalf("expected separate sandbox config files, got %q", args[5:])
	}
	if got := read("npm-configs"); got != "--userconfig=empty\n--globalconfig=empty\n" {
		t.Fatalf("npm configs at execution = %q", got)
	}
	if got := read("npm-cache"); got != filepath.Join(root, "bin/.cache/npm")+"\n" {
		t.Fatalf("npm cache = %q", got)
	}
}

func TestMissingNPM(t *testing.T) {
	root, _ := fakeNodeRoot(t)
	if err := run("install", root); err == nil || !strings.Contains(err.Error(), "locating bundled npm beside Node") || !strings.Contains(err.Error(), filepath.Join(root, "npm")) {
		t.Fatalf("expected missing bundled npm error, got %v", err)
	}
}

func checkOutput(t *testing.T, root string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	err = run("check", root)
	_ = w.Close()
	out, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(out), err
}

func TestMissingChecker(t *testing.T) {
	root, pin := fakeNodeRoot(t)
	for _, required := range []string{"", "1"} {
		t.Run("required="+required, func(t *testing.T) {
			t.Setenv("RIBBITTO_REQUIRE_NODE", required)
			if required == "" {
				if err := os.Unsetenv("RIBBITTO_REQUIRE_NODE"); err != nil {
					t.Fatal(err)
				}
			}
			out, err := checkOutput(t, root)
			if required == "1" {
				if err == nil || !strings.Contains(err.Error(), pin) {
					t.Fatalf("expected missing checker failure with pin %s, got %v: %s", pin, err, out)
				}
			} else if err != nil || !strings.Contains(out, "NOT VERIFIED") || !strings.Contains(out, pin) {
				t.Fatalf("expected NOT VERIFIED with pin %s, got %v: %s", pin, err, out)
			}
		})
	}
}

func TestCheckerVersion(t *testing.T) {
	for _, tc := range []struct{ name, manifest, version string }{
		{"match", "", ""},
		{"mismatch", `{"version":"0.0.0"}`, "0.0.0"},
		{"unreadable", `{"version":`, "unreadable"},
		{"empty", `{}`, "unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, pin := fakeNodeRoot(t)
			manifest := tc.manifest
			if tc.name == "match" {
				manifest = `{"version":"` + pin + `"}`
			}
			dir := filepath.Join(root, "node_modules/typescript")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"", "1"} {
				t.Setenv("RIBBITTO_REQUIRE_NODE", required)
				out, err := checkOutput(t, root)
				if tc.name == "match" {
					if err != nil || out != "CHECKER RAN\n" {
						t.Fatalf("matching checker: %v: %s", err, out)
					}
					continue
				}
				message := out
				if required == "1" {
					if err == nil {
						t.Fatalf("expected version failure, got success: %s", out)
					}
					message = err.Error()
				} else if err != nil || !strings.Contains(out, "NOT VERIFIED") {
					t.Fatalf("expected NOT VERIFIED, got %v: %s", err, out)
				}
				if !strings.Contains(message, pin) || !strings.Contains(message, tc.version) || strings.Contains(out, "CHECKER RAN") {
					t.Fatalf("expected installed %s and pinned %s without checker execution: %v: %s", tc.version, pin, err, out)
				}
			}
		})
	}
}

func allowedLockName(name string) bool {
	if !strings.HasPrefix(name, "node_modules/") {
		return false
	}
	for _, pkg := range strings.Split(strings.TrimPrefix(name, "node_modules/"), "/node_modules/") {
		if pkg != "typescript" && !strings.HasPrefix(pkg, "@typescript/") {
			return false
		}
	}
	return true
}

func TestLockfilePackageNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"node_modules/typescript", true},
		{"node_modules/@typescript/typescript-linux-x64", true},
		{"node_modules/@typescript/outer/node_modules/typescript", true},
		{"node_modules/@typescript/outer/node_modules/unrelated", false},
		{"node_modules/@typescript/outer/node_modules/@other/package", false},
		{"node_modules/evil/node_modules/typescript", false},
		{"node_modules/evil/node_modules/@typescript/typescript-linux-x64", false},
		{"node_modules/evil", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowedLockName(tc.name); got != tc.want {
				t.Fatalf("allowedLockName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
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
		if !allowedLockName(name) || !strings.HasPrefix(pkg.Resolved, "https://registry.npmjs.org/") || pkg.Integrity == "" || pkg.HasInstallScript {
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

func TestFilesReadiness(t *testing.T) {
	for _, state := range []string{"wrong-node", "missing-checker", "mismatched-checker"} {
		t.Run(state, func(t *testing.T) {
			root, pin := fakeNodeRoot(t)
			t.Setenv("RIBBITTO_REQUIRE_NODE", "")
			if err := os.Unsetenv("RIBBITTO_REQUIRE_NODE"); err != nil {
				t.Fatal(err)
			}
			want := "TypeScript installed version unreadable"
			switch state {
			case "wrong-node":
				installFakeChecker(t, root, pin)
				if err := os.WriteFile(filepath.Join(root, "node"), []byte("#!/bin/sh\nprintf 'v0.0.0\\n'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				want = `Node version "v0.0.0" does not match`
			case "mismatched-checker":
				installFakeChecker(t, root, "0.0.0")
				want = `TypeScript installed version "0.0.0" does not match pinned version "` + pin + `"`
			}
			if err := run("files", root, "input.ts"); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("files must fail with %q even without opt-in: %v", want, err)
			}
		})
	}
}

func installFakeChecker(t *testing.T, root, version string) {
	t.Helper()
	dir := filepath.Join(root, "node_modules/typescript")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":"`+version+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestFilesArguments(t *testing.T) {
	root, pin := fakeNodeRoot(t)
	installFakeChecker(t, root, pin)
	data, err := os.ReadFile(filepath.Join(root, "node"))
	if err != nil {
		t.Fatal(err)
	}
	// Shell builtins only: PATH contains just our fake, never a real Node.
	script := strings.Replace(string(data), "printf 'CHECKER RAN\\n'", `printf '%s\n' "$@" > node-args`, 1)
	if err := os.WriteFile(filepath.Join(root, "node"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(root, "one input.ts"), filepath.Join(root, "two.ts")}
	if err := run("files", root, files...); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(root, "node-args"))
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{"node_modules/typescript/bin/tsc", "--ignoreConfig"}, fileFlags()...), files...)
	if got := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"); !slices.Equal(got, want) {
		t.Fatalf("node argv = %q, want %q", got, want)
	}
}

func TestFilesFlagsMatchConfig(t *testing.T) {
	data, err := os.ReadFile("../../tsconfig.json") // This repository's config is strict JSON.
	if err != nil {
		t.Fatal(err)
	}
	var config struct{ CompilerOptions map[string]any }
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	var want []string
	for name, value := range config.CompilerOptions {
		flag := "--" + name
		switch value := value.(type) {
		case bool:
			if value {
				want = append(want, flag)
			} else {
				want = append(want, flag, "false")
			}
		case string:
			want = append(want, flag, value)
		case []any:
			var values []string
			for _, item := range value {
				text, ok := item.(string)
				if !ok {
					t.Fatalf("unsupported %s item %v", name, item)
				}
				values = append(values, text)
			}
			want = append(want, flag, strings.Join(values, ","))
		default:
			t.Fatalf("unsupported compiler option %s=%v", name, value)
		}
	}
	// Compare option/value groups, independent of map and command-line ordering.
	options := func(args []string) []string {
		var result []string
		for i := 0; i < len(args); i++ {
			option := args[i]
			if !strings.HasPrefix(option, "--") {
				t.Fatalf("expected flag, got %q", option)
			}
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				option += "=" + args[i]
			}
			result = append(result, option)
		}
		slices.Sort(result)
		return result
	}
	got, expected := options(fileFlags()), options(want)
	if !slices.Equal(got, expected) {
		t.Fatalf("files flags = %q, tsconfig compilerOptions = %q", got, expected)
	}
}
