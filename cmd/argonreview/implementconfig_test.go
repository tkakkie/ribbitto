package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type implementConfigFixture struct {
	root string
	c    implementConfig
}

func newImplementConfigFixture(t *testing.T, on bool) implementConfigFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := implementConfigFixture{root, implementConfig{on, filepath.Join(root, "bin", "argonreview"), filepath.Join(root, "guard"), "", "", filepath.Join(root, "output", "home")}}
	for _, name := range []string{"bin", "guard", "source", "worktree", "output"} {
		configMkdir(t, filepath.Join(root, name))
	}
	configWrite(t, f.c.binary, []byte("never execute this file"), 0755)
	if on {
		f.c.source, f.c.worktree = filepath.Join(root, "source"), filepath.Join(root, "worktree")
	}
	return f
}

func configMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func configWrite(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func configSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func (f implementConfigFixture) args() []string {
	mode := "off"
	if f.c.verify {
		mode = "on"
	}
	args := []string{"--mode", mode, "--binary", f.c.binary, "--guard-dir", f.c.guardDir, "--out", f.c.out}
	if f.c.verify {
		args = append(args, "--trusted-source", f.c.source, "--worktree", f.c.worktree)
	}
	return args
}

func configSnapshot(t *testing.T, root string) string {
	t.Helper()
	var result strings.Builder
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(&result, "%s %s\n", path, info.Mode())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(&result, "%x\n", sha256.Sum256(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.String()
}

func TestImplementConfigGoldens(t *testing.T) {
	for _, on := range []bool{false, true} {
		mode := "off"
		if on {
			mode = "on"
		}
		t.Run(mode, func(t *testing.T) {
			h, s, m, err := implementConfigFiles(on, "/trusted/bin/argonreview", "/trusted/guard", "/trusted/source", "/trusted/worktree")
			if err != nil {
				t.Fatal(err)
			}
			wantMCP := []byte(nil)
			if on {
				wantMCP = implementVerifyGolden(t, "mcp-config-on.json")
			}
			for i, pair := range [][2][]byte{{h, implementVerifyGolden(t, "hooks-"+mode+".json")}, {s, implementVerifyGolden(t, "settings-"+mode+".json")}, {m, wantMCP}} {
				if !bytes.Equal(pair[0], pair[1]) {
					t.Fatalf("file %d differs from golden", i)
				}
			}
		})
	}
}

func TestImplementConfigEndToEnd(t *testing.T) {
	for _, on := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("on=%t/existing=%t", on, existing), func(t *testing.T) {
				f := newImplementConfigFixture(t, on)
				if existing {
					configMkdir(t, f.c.out)
				}
				var out, stderr bytes.Buffer
				if code := run(append([]string{"implement-config"}, f.args()...), nil, &out, &stderr); code != 0 || stderr.Len() != 0 {
					t.Fatalf("exit=%d stderr=%s", code, &stderr)
				}
				h, err := implementerHooks(on, f.c.binary, f.c.guardDir)
				if err != nil {
					t.Fatal(err)
				}
				m, err := implementerMCPConfig(on, f.c.binary, f.c.source, f.c.worktree)
				if err != nil {
					t.Fatal(err)
				}
				var expected strings.Builder
				for i, name := range []string{"hooks.json", "settings.json", "mcp_config.json"} {
					want := [][]byte{h, implementerSettings(on), m}[i]
					data, err := os.ReadFile(filepath.Join(f.c.out, name))
					if err != nil || !bytes.Equal(data, want) {
						t.Fatalf("%s differs: %v", name, err)
					}
					info, err := os.Lstat(filepath.Join(f.c.out, name))
					if err != nil || info.Mode() != 0600 {
						t.Fatalf("%s mode: %v %v", name, info, err)
					}
					if !on && (i == 1 && len(data) != 171 || i == 2 && len(data) != 0) {
						t.Fatalf("off %s length=%d", name, len(data))
					}
					_, _ = fmt.Fprintf(&expected, "%x  %s\n", sha256.Sum256(data), name)
				}
				mode := "off"
				if on {
					mode = "on"
				}
				_, _ = fmt.Fprintf(&expected, "implement-config: wrote mode=%s files=3 out=%s\n", mode, f.c.out)
				if out.String() != expected.String() {
					t.Fatalf("stdout=%q want=%q", &out, &expected)
				}
				info, err := os.Stat(f.c.out)
				if err != nil || info.Mode() != os.ModeDir|0700 {
					t.Fatalf("output mode: %v %v", info, err)
				}
				entries, err := os.ReadDir(filepath.Dir(f.c.out))
				if err != nil || len(entries) != 1 {
					t.Fatalf("output parent entries=%v err=%v", entries, err)
				}
			})
		}
	}
}

func TestImplementConfigArguments(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newImplementConfigFixture(t, on)
		valid := f.args()
		cases := []struct {
			name string
			args []string
		}{
			{"unknown", append(append([]string{}, valid...), "--unknown", "x")},
			{"repeated", append(append([]string{}, valid...), "--mode", "off")},
			{"missing-value", append(append([]string{}, valid...), "--out")},
			{"positional", append(append([]string{}, valid...), "position")},
			{"equals", append([]string{"--mode=on"}, valid[2:]...)},
			{"bad-mode", append([]string{"--mode", "ON"}, valid[2:]...)},
			{"value-is-flag", append([]string{"--mode", "--out"}, valid[2:]...)},
		}
		for i := 0; i < len(valid); i += 2 {
			cases = append(cases, struct {
				name string
				args []string
			}{"repeated-" + valid[i], append(append([]string{}, valid...), valid[i], valid[i+1])})
			cases = append(cases, struct {
				name string
				args []string
			}{"missing-value-" + valid[i], append(append([]string{}, valid[:i]...), valid[i])})
			missing := append(append([]string{}, valid[:i]...), valid[i+2:]...)
			cases = append(cases, struct {
				name string
				args []string
			}{"missing-" + valid[i], missing})
			empty := append([]string{}, valid...)
			empty[i+1] = ""
			cases = append(cases, struct {
				name string
				args []string
			}{"empty-" + valid[i], empty})
			if i == 0 {
				continue
			}
			for _, bad := range []struct{ name, path string }{
				{"relative", "relative"}, {"dotdot", "/a/../b"}, {"trailing", "/a/"}, {"double-slash", "/a//b"}, {"double-root", "//a"}, {"root", "/"},
				{"quote", "/quo'te"}, {"newline", "/new\nline"}, {"tab", "/a\tb"}, {"DEL", "/a\x7fb"}, {"C1", "/a\u0085b"}, {"NUL", "/a\x00b"}, {"invalid-UTF8", "/a\xffb"},
			} {
				args := append([]string{}, valid...)
				args[i+1] = bad.path
				cases = append(cases, struct {
					name string
					args []string
				}{valid[i] + "-" + bad.name, args})
			}
		}
		if !on {
			for _, flag := range []string{"--trusted-source", "--worktree"} {
				cases = append(cases, struct {
					name string
					args []string
				}{"off-" + flag, append(append([]string{}, valid...), flag, filepath.Join(f.root, "source"))})
			}
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("on=%t/%s", on, tc.name), func(t *testing.T) {
				// Exercise the pure parser too: filesystem refusals must not mask
				// missing character checks on paths that happen not to exist.
				if _, err := parseImplementConfig(tc.args); err == nil {
					t.Fatal("parser accepted invalid arguments")
				}
				before := configSnapshot(t, f.root)
				assertImplementConfigRefusal(t, tc.args)
				if after := configSnapshot(t, f.root); after != before {
					t.Fatal("validation changed the filesystem")
				}
			})
		}
	}
}

func assertImplementConfigRefusal(t *testing.T, args []string) {
	t.Helper()
	var out, stderr bytes.Buffer
	if code := implementConfigCommand(args, &out, &stderr); code != 2 || out.Len() != 0 || !strings.HasPrefix(stderr.String(), "argonreview implement-config: ") || strings.Count(stderr.String(), "\n") != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, &out, &stderr)
	}
}

func TestImplementConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *implementConfigFixture)
	}{
		{"binary-missing", func(_ *testing.T, f *implementConfigFixture) { f.c.binary += "-missing" }},
		{"binary-directory", func(_ *testing.T, f *implementConfigFixture) { f.c.binary = filepath.Dir(f.c.binary) }},
		{"binary-symlink", func(t *testing.T, f *implementConfigFixture) {
			link := f.c.binary + "-link"
			configSymlink(t, f.c.binary, link)
			f.c.binary = link
		}},
		{"binary-not-executable", func(t *testing.T, f *implementConfigFixture) { configWrite(t, f.c.binary, nil, 0600) }},
		{"binary-symlink-ancestor", func(t *testing.T, f *implementConfigFixture) {
			link := filepath.Join(f.root, "bin-link")
			configSymlink(t, filepath.Dir(f.c.binary), link)
			f.c.binary = filepath.Join(link, "argonreview")
		}},
		{"guard-missing", func(_ *testing.T, f *implementConfigFixture) { f.c.guardDir += "-missing" }},
		{"guard-file", func(_ *testing.T, f *implementConfigFixture) { f.c.guardDir = f.c.binary }},
		{"guard-symlink", func(t *testing.T, f *implementConfigFixture) {
			link := f.c.guardDir + "-link"
			configSymlink(t, f.c.guardDir, link)
			f.c.guardDir = link
		}},
		{"source-missing", func(_ *testing.T, f *implementConfigFixture) { f.c.source += "-missing" }},
		{"source-file", func(_ *testing.T, f *implementConfigFixture) { f.c.source = f.c.binary }},
		{"worktree-missing", func(_ *testing.T, f *implementConfigFixture) { f.c.worktree += "-missing" }},
		{"worktree-file", func(_ *testing.T, f *implementConfigFixture) { f.c.worktree = f.c.binary }},
		{"source-worktree-equal", func(_ *testing.T, f *implementConfigFixture) { f.c.worktree = f.c.source }},
		{"source-inside-worktree", func(t *testing.T, f *implementConfigFixture) {
			f.c.source = filepath.Join(f.c.worktree, "nested")
			configMkdir(t, f.c.source)
		}},
		{"worktree-inside-source", func(t *testing.T, f *implementConfigFixture) {
			f.c.worktree = filepath.Join(f.c.source, "nested")
			configMkdir(t, f.c.worktree)
		}},
		{"source-worktree-case-alias", func(t *testing.T, f *implementConfigFixture) {
			alias := filepath.Join(f.root, "SOURCE")
			a, err := os.Stat(alias)
			if errors.Is(err, os.ErrNotExist) {
				t.Skip("case-sensitive filesystem")
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.Stat(f.c.source)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(a, b) {
				t.Skip("distinct case spelling")
			}
			f.c.worktree = alias
		}},
		{"out-parent-missing", func(_ *testing.T, f *implementConfigFixture) { f.c.out = filepath.Join(f.root, "missing", "home") }},
		{"out-parent-symlink", func(t *testing.T, f *implementConfigFixture) {
			link := filepath.Join(f.root, "output-link")
			configSymlink(t, filepath.Dir(f.c.out), link)
			f.c.out = filepath.Join(link, "home")
		}},
		{"out-nonempty", func(t *testing.T, f *implementConfigFixture) {
			configMkdir(t, f.c.out)
			configWrite(t, filepath.Join(f.c.out, "keep"), []byte("keep"), 0600)
		}},
		{"out-symlink", func(t *testing.T, f *implementConfigFixture) {
			target := filepath.Join(f.root, "empty")
			configMkdir(t, target)
			configSymlink(t, target, f.c.out)
		}},
		{"out-file", func(t *testing.T, f *implementConfigFixture) { configWrite(t, f.c.out, []byte("keep"), 0600) }},
	}
	for _, root := range []string{"source", "worktree", "guard", "binary"} {
		for _, equal := range []bool{false, true} {
			cases = append(cases, struct {
				name   string
				change func(*testing.T, *implementConfigFixture)
			}{"out-" + root + fmt.Sprintf("-equal=%t", equal), func(_ *testing.T, f *implementConfigFixture) {
				path := map[string]string{"source": f.c.source, "worktree": f.c.worktree, "guard": f.c.guardDir, "binary": filepath.Dir(f.c.binary)}[root]
				f.c.out = path
				if !equal {
					f.c.out = filepath.Join(path, "home")
				}
			}})
		}
	}
	for _, field := range []string{"source", "worktree"} {
		for _, other := range []string{"binary", "guard"} {
			for _, direction := range []string{"equal", "inside", "contains"} {
				cases = append(cases, struct {
					name   string
					change func(*testing.T, *implementConfigFixture)
				}{field + "-overlap-" + other + "-" + direction, func(t *testing.T, f *implementConfigFixture) {
					root := f.c.guardDir
					if other == "binary" {
						root = filepath.Dir(f.c.binary)
					}
					path := root
					if direction == "inside" {
						path = filepath.Join(root, "nested")
						configMkdir(t, path)
					}
					if direction == "contains" {
						path = f.root
					}
					if field == "source" {
						f.c.source = path
					} else {
						f.c.worktree = path
					}
				}})
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newImplementConfigFixture(t, true)
			tc.change(t, &f)
			before := configSnapshot(t, f.root)
			assertImplementConfigRefusal(t, f.args())
			if configSnapshot(t, f.root) != before {
				t.Fatal("validation changed the filesystem")
			}
		})
	}
}

func TestImplementConfigCaseAliases(t *testing.T) {
	for _, root := range []string{"guard", "worktree", "source", "bin"} {
		t.Run(root, func(t *testing.T) {
			f := newImplementConfigFixture(t, true)
			alias := filepath.Join(f.root, strings.ToUpper(root))
			a, err := os.Stat(alias)
			if errors.Is(err, os.ErrNotExist) {
				t.Skip("case-sensitive filesystem")
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.Stat(filepath.Join(f.root, root))
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(a, b) {
				t.Skip("distinct case spelling")
			}
			f.c.out = filepath.Join(alias, "home")
			before := configSnapshot(t, f.root)
			assertImplementConfigRefusal(t, f.args())
			if configSnapshot(t, f.root) != before {
				t.Fatal("validation changed the filesystem")
			}
		})
	}
}

func TestImplementConfigFailure(t *testing.T) {
	for _, on := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			for _, failure := range []string{"second-write", "rename", "read-error", "hooks-tampered", "settings-tampered", "mixed", "extra-entry", "wrong-mode", "symlink-file"} {
				t.Run(fmt.Sprintf("on=%t/existing=%t/%s", on, existing, failure), func(t *testing.T) {
					f := newImplementConfigFixture(t, on)
					if existing {
						configMkdir(t, f.c.out)
					}
					ops := implementConfigIO{writeImplementConfigFile, os.ReadFile, os.Rename}
					injected := errors.New("injected failure")
					ops.write = func(path string, data []byte) error {
						if failure == "second-write" && filepath.Base(path) == "settings.json" {
							return injected
						}
						if err := writeImplementConfigFile(path, data); err != nil {
							return err
						}
						if filepath.Base(path) == "mcp_config.json" {
							switch failure {
							case "extra-entry":
								configWrite(t, filepath.Join(filepath.Dir(path), "extra"), nil, 0600)
							case "wrong-mode":
								if err := os.Chmod(path, 0644); err != nil {
									t.Fatal(err)
								}
							case "symlink-file":
								if err := os.Remove(path); err != nil {
									t.Fatal(err)
								}
								configSymlink(t, f.c.binary, path)
							}
						}
						return nil
					}
					ops.read = func(path string) ([]byte, error) {
						if failure == "read-error" {
							return nil, injected
						}
						if (failure == "hooks-tampered" || failure == "mixed") && filepath.Base(path) == "hooks.json" {
							return implementerHooks(!on, f.c.binary, f.c.guardDir)
						}
						if failure == "settings-tampered" && filepath.Base(path) == "settings.json" {
							return implementerSettings(!on), nil
						}
						if failure == "mixed" && filepath.Base(path) == "mcp_config.json" {
							return implementerMCPConfig(!on, f.c.binary, filepath.Join(f.root, "source"), filepath.Join(f.root, "worktree"))
						}
						return os.ReadFile(path)
					}
					if failure == "rename" {
						ops.rename = func(string, string) error { return injected }
					}
					var out, stderr bytes.Buffer
					if code := implementConfigCommandWithIO(f.args(), &out, &stderr, ops); code != 1 || out.Len() != 0 || !strings.HasPrefix(stderr.String(), "argonreview implement-config: ") || strings.Count(stderr.String(), "\n") != 1 {
						t.Fatalf("exit=%d stdout=%q stderr=%q", code, &out, &stderr)
					}
					entries, err := os.ReadDir(filepath.Dir(f.c.out))
					if err != nil {
						t.Fatal(err)
					}
					// Rename failure follows the requested remove-without-recreate rule.
					wantEmpty := existing && failure != "rename"
					if wantEmpty {
						if len(entries) != 1 || entries[0].Name() != filepath.Base(f.c.out) {
							t.Fatalf("parent entries=%v", entries)
						}
						children, err := os.ReadDir(f.c.out)
						if err != nil || len(children) != 0 {
							t.Fatalf("output=%v err=%v", children, err)
						}
					} else if len(entries) != 0 {
						t.Fatalf("leftover entries=%v", entries)
					}
				})
			}
		}
	}
}

func TestImplementConfigMixedStates(t *testing.T) {
	for _, verify := range []bool{false, true} {
		for bits := 0; bits < 8; bits++ {
			t.Run(fmt.Sprintf("verify=%t/forms=%03b", verify, bits), func(t *testing.T) {
				h, err := implementerHooks(bits&1 != 0, "/trusted/bin/argonreview", "/trusted/guard")
				if err != nil {
					t.Fatal(err)
				}
				m, err := implementerMCPConfig(bits&4 != 0, "/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree")
				if err != nil {
					t.Fatal(err)
				}
				err = checkImplementerConfig(verify, h, implementerSettings(bits&2 != 0), m, "/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree", "/trusted/guard")
				want := !verify && bits == 0 || verify && bits == 7
				if (err == nil) != want {
					t.Fatalf("accepted=%t want=%t err=%v", err == nil, want, err)
				}
			})
		}
	}
}

func TestImplementConfigUsage(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run(nil, nil, &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), implementConfigUsage) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, &out, &stderr)
	}
}
