package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestImplementRunOffPostBeforeRestore(t *testing.T) {
	for _, target := range []string{"none", "settings", "hooks", "mcp"} {
		t.Run(target, func(t *testing.T) {
			x := newImplementRun(t)
			paths := map[string]string{"settings": ".gemini/antigravity-cli/settings.json", "hooks": ".gemini/config/hooks.json", "mcp": ".gemini/config/mcp_config.json"}
			if target != "none" {
				write(t, filepath.Join(x.fake, "mutate"), fmt.Sprintf("echo tampered > \"$HOME/%s\"\n", paths[target]))
			}
			want := 0
			if target != "none" {
				want = 1
			}
			status := x.run(t, want)
			if !strings.Contains(strings.Join(x.order, ","), "post-check,check,restore-settings,restore-hooks,restore-mcp") {
				t.Fatal("post-check did not precede all restoration", x.order)
			}
			if target != "none" && (!strings.Contains(status, "post-check=1") || !strings.Contains(status, "counts=false")) {
				t.Fatal("tampering counted", status)
			}
			x.cleaned(t)
			data, err := os.ReadFile(filepath.Join(x.f.home, paths["mcp"]))
			if err != nil || len(data) != 0 {
				t.Fatalf("off MCP not restored empty: %q %v", data, err)
			}
		})
	}
}

func TestImplementRunObservedHashes(t *testing.T) {
	for _, mode := range []string{"off", "on"} {
		t.Run(mode, func(t *testing.T) {
			x := newImplementRun(t)
			if mode == "on" {
				featureOn(t, x)
			}
			write(t, filepath.Join(x.fake, "mutate"), `echo tampered-settings > "$HOME/.gemini/antigravity-cli/settings.json"
echo tampered-hooks > "$HOME/.gemini/config/hooks.json"
echo tampered-mcp > "$HOME/.gemini/config/mcp_config.json"
`)
			synced, inspected := false, false
			x.deps.syncLog = func(f *os.File) error {
				if filepath.Base(f.Name()) == "post.txt" {
					synced = true
				}
				return f.Sync()
			}
			step := x.deps.step
			x.deps.step = func(name string) error {
				if name == "restore-settings" {
					inspected = true
					data, err := os.ReadFile(filepath.Join(x.logs, "post.txt"))
					if err != nil || !synced {
						t.Fatalf("observation not saved before restoration: %v synced=%t", err, synced)
					}
					for _, file := range []string{"settings", "hooks", "mcp"} {
						want := fmt.Sprintf("observed-%s-sha256=%x\n", file, sha256.Sum256([]byte("tampered-"+file+"\n")))
						if !strings.Contains(string(data), want) {
							t.Fatalf("missing tampered hash before restoration: %q in %s", want, data)
						}
					}
				}
				return step(name)
			}
			x.run(t, 1)
			if !inspected {
				t.Fatal("restoration not attempted")
			}
			x.cleaned(t)
		})
	}
}

func TestImplementRunObservedReadError(t *testing.T) {
	x := newImplementRun(t)
	featureOn(t, x)
	write(t, filepath.Join(x.fake, "mutate"), `rm "$HOME/.gemini/config/hooks.json"`)
	check := x.deps.check
	x.deps.check = func(args []string, out, stderr io.Writer) int {
		code := check(args, out, stderr)
		if x.checks == 3 {
			// Isolate the evidence reader's error from the independent check.
			return 0
		}
		return code
	}
	status := x.run(t, 1)
	data, err := os.ReadFile(filepath.Join(x.logs, "post.txt"))
	if err != nil || !strings.Contains(string(data), "observed-hooks-read-error=") || !strings.Contains(string(data), "observed-mcp-sha256=") || !strings.Contains(status, "observing hooks:") {
		t.Fatalf("read error lost or observation stopped: %s %v %s", data, err, status)
	}
	x.cleaned(t)
}

func TestImplementRunMarkerFinalStatus(t *testing.T) {
	for _, fail := range []string{"none", "remove", "final-sync", "final-output"} {
		t.Run(fail, func(t *testing.T) {
			x := newImplementRun(t)
			marker := filepath.Join(x.f.home, "guard/implement-run.recovery-needed")
			saved, finalSync := false, false
			x.deps.saveStatus = func(f *os.File, status string) error {
				if _, err := os.Lstat(marker); err != nil || !strings.Contains(status, "in-progress-marker=remains\n") || strings.Contains(status, "in-progress-marker=removed\n") {
					t.Fatalf("initial/failure status does not retain marker: %v %s", err, status)
				}
				if saved {
					data, err := os.ReadFile(f.Name())
					if err != nil || strings.Contains(string(data), "in-progress-marker=removed\n") {
						t.Fatalf("marker recreated while saved status claims removal: %s %v", data, err)
					}
				}
				saved = true
				return implementRunSaveStatus(f, status)
			}
			step := x.deps.step
			x.deps.step = func(name string) error {
				if name == "remove-marker" {
					if !saved {
						t.Fatal("marker removed before initial status save")
					}
					if fail == "remove" {
						return errors.New("injected removal")
					}
				}
				return step(name)
			}
			x.deps.syncLog = func(f *os.File) error {
				if filepath.Base(f.Name()) == "status" && !finalSync {
					finalSync = true
					if _, err := os.Lstat(marker); !os.IsNotExist(err) {
						t.Fatal("final status synced before marker removal", err)
					}
					if fail == "final-sync" {
						return errors.New("injected final sync")
					}
				}
				return f.Sync()
			}
			var out, stderr bytes.Buffer
			var writer io.Writer = &out
			if fail == "final-output" {
				writer = &implementFinalFailWriter{}
			}
			code := implementRunCommand(x.args, writer, &stderr, x.deps)
			data, err := os.ReadFile(filepath.Join(x.logs, "status"))
			if err != nil {
				t.Fatal(err)
			}
			if fail == "none" {
				if code != 0 || !finalSync || !strings.HasSuffix(string(data), "in-progress-marker=removed\n") {
					t.Fatalf("missing durable final removal: code=%d sync=%t %s", code, finalSync, data)
				}
			} else if code != 1 || !strings.Contains(string(data), "counts=false\n") || strings.Contains(string(data), "in-progress-marker=removed\n") || !strings.Contains(string(data), "in-progress-marker=remains\n") || !strings.Contains(string(data), fmt.Sprintf("in-progress-marker-path=%q\n", marker)) {
				t.Fatalf("failed completion claims success: code=%d %s %s", code, data, &stderr)
			}
			_, err = os.Lstat(marker)
			if os.IsNotExist(err) != (fail == "none") {
				t.Fatal("marker after completion", err)
			}
		})
	}
}

type implementFinalFailWriter struct{ calls int }

func (w *implementFinalFailWriter) Write(data []byte) (int, error) {
	w.calls++
	if w.calls == 2 {
		return 0, errors.New("injected final output")
	}
	return len(data), nil
}

func TestImplementRunFailedCheckSync(t *testing.T) {
	for _, phase := range []string{"pre.txt", "post.txt", "at-rest.txt"} {
		t.Run(phase, func(t *testing.T) {
			x := newImplementRun(t)
			check := x.deps.check
			synced := false
			failedCheck := map[string]int{"pre.txt": 2, "post.txt": 3, "at-rest.txt": 4}[phase]
			x.deps.check = func(args []string, out, stderr io.Writer) int {
				code := check(args, out, stderr)
				if x.checks == failedCheck {
					return 1
				}
				return code
			}
			x.deps.syncLog = func(f *os.File) error {
				if filepath.Base(f.Name()) == phase && x.checks == failedCheck {
					synced = true
					return errors.New("injected log sync")
				}
				return f.Sync()
			}
			status := x.run(t, 1)
			if !synced {
				t.Fatal("failed check skipped log sync")
			}
			if phase == "at-rest.txt" && (!strings.Contains(status, "at-rest check failed") || !strings.Contains(status, "syncing at-rest log: injected log sync")) {
				t.Fatal("lost check or sync error", status)
			}
			x.cleaned(t)
		})
	}
}

func TestImplementFeatureUsageOrder(t *testing.T) {
	var out bytes.Buffer
	if code := run(nil, nil, io.Discard, &out); code != 2 {
		t.Fatal("usage exit", code)
	}
	for _, name := range []string{"implement-check", "implement-run"} {
		if !strings.Contains(out.String(), name+" --verify off|on [--trusted-source <abs>] --home <dir>") {
			t.Fatal("inconsistent verify position", out.String())
		}
	}
}

func TestImplementFeatureRecoveryDocumentation(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("../../docs/workflow", name))
		if err != nil {
			t.Fatal(err)
		}
		if n := utf8.RuneCount(data); n > 11000 {
			t.Fatalf("%s: %d runes", name, n)
		}
		return string(data)
	}
	read("argon-implement-run.md")
	t.Run("role-entry", func(t *testing.T) {
		if !strings.Contains(read("README.md"), "Start with your role: [Codex implementer](#implementer-codex),") {
			t.Fatal("role entry missing")
		}
	})
	recovery := strings.Join(strings.Fields(read("argon-implement-run-recovery.md")), " ")
	for name, want := range map[string]string{
		"evidence-before-restore": "2. Before restoring, record whether the session's files were tampered with.",
		"worktree-at-rest":        "The run leaves `guard/implementer-worktree` at rest containing the absolute run worktree path followed by a newline;",
		"failed-run-marker":       "After every failed run, remove the marker by hand after the recovery check, even if automatic cleanup passed.",
		"caller-verify":           "The caller chooses `--verify`. At the switch, the host wrapper must derive it from the maintainer's trusted verify-mode file; that wrapper is outside this PR.",
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(recovery, want) {
				t.Fatal("recovery instruction missing:", want)
			}
		})
	}
}
