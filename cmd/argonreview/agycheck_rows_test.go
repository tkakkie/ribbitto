package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type agyCheckCommandHarness struct {
	d                         agyCheckDeps
	x                         agyCheckExecutionDeps
	m                         agyCheckMarker
	agy, tool, settings       string
	validations, starts, pins int
	writes                    []string
}

func agyCheckNewCommandHarness(t *testing.T, row, deviation string) *agyCheckCommandHarness {
	t.Helper()
	home := agyCheckRealTemp(t)
	isolation := filepath.Join(home, "isolation")
	if err := os.Mkdir(isolation, 0o700); err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	h := &agyCheckCommandHarness{agy: agyCheckFakeBinary(t), tool: agyCheckToolBinary(t)}
	h.m = agyCheckMarker{1, account.Username, home, isolation, "/protected/account", "/protected/repository", []string{"/protected/worker"}, "/protected/review"}
	b, _ := json.Marshal(h.m)
	agyCheckTestWrite(t, filepath.Join(home, agyCheckMarkerName), b, 0o600)
	b, _ = json.Marshal(agyCheckRowFake{Rows: true, RealHooks: deviation == "real-hooks", Home: home, Isolation: isolation, Row: row, Deviation: deviation})
	agyCheckTestWrite(t, h.agy+".fixture", b, 0o600)
	h.settings = filepath.Join(home, ".gemini/antigravity-cli/settings.json")
	agyCheckTestWrite(t, h.settings, []byte("original marked-home settings\n"), 0o600)
	agyCheckTestWrite(t, filepath.Join(home, ".gemini/antigravity-cli/builtin/fixture"), []byte("builtin fixture\n"), 0o600)
	h.d = agyCheckDefaults()
	h.d.executable = func() (string, error) { return h.tool, nil }
	trusted := map[string]bool{}
	for _, path := range []string{h.agy, h.tool} {
		for {
			trusted[path] = true
			if path == filepath.Dir(path) {
				break
			}
			path = filepath.Dir(path)
		}
	}
	h.d.lstat = func(path string) (os.FileInfo, error) {
		for _, root := range h.m.canaries() {
			if implementRunInside(root, path) {
				t.Fatalf("protected path inspected: %s", path)
			}
		}
		info, err := os.Lstat(path)
		if err == nil && trusted[path] {
			return agyCheckOwnedInfo{info, info.Mode(), 0}, nil
		}
		return info, err
	}
	h.d.access = func(path string, mode uint32) error {
		for _, root := range h.m.canaries() {
			if path == root {
				return syscall.EACCES
			}
		}
		if trusted[path] && mode == 2 {
			return syscall.EACCES
		}
		return syscall.Access(path, mode)
	}
	h.x = agyCheckExecutionDeps{agyCheckRunnerDefaults(), systemCleanup(), 10 * time.Second}
	h.x.runner.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
		h.validations++
		return agyCheckValidate(o, d)
	}
	h.x.runner.signals = func(fn func(<-chan os.Signal) (int, error)) (int, error) { return fn(nil) }
	h.x.ops.scan = func(int) ([]int, error) { return nil, nil }
	wait := h.x.ops.wait
	seen := map[int]bool{}
	h.x.ops.wait = func(pid int, status *syscall.WaitStatus) (int, error) {
		if !seen[pid] {
			seen[pid] = true
			h.starts++
		}
		return wait(pid, status)
	}
	open := h.d.open
	h.d.open = func(path string) (io.ReadCloser, error) { h.pins++; return open(path) }
	fileOpen := h.x.runner.files.open
	h.x.runner.files.open = func(path string, flags int, mode os.FileMode) (*os.File, error) {
		h.writes = append(h.writes, path)
		return fileOpen(path, flags, mode)
	}
	h.d.execution = &h.x
	return h
}
func (h *agyCheckCommandHarness) run(t *testing.T, calls int) (int, string) {
	t.Helper()
	args := []string{"--home", h.m.Home, "--agy", h.agy}
	if calls > 0 {
		args = append(args, "--calls", fmt.Sprint(calls))
	}
	var out, stderr bytes.Buffer
	code := agyCheckCommand(args, &out, &stderr, h.d)
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	return code, out.String()
}
func (h *agyCheckCommandHarness) restored(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(h.settings)
	if err != nil || string(data) != "original marked-home settings\n" {
		t.Fatal("settings not restored byte-identically")
	}
	entries, err := os.ReadDir(h.m.Isolation)
	if err != nil || len(entries) != 0 {
		t.Fatalf("isolation not cleaned: %v %v", entries, err)
	}
	for _, path := range h.writes {
		if implementRunInside(h.m.Isolation, path) {
			continue
		}
		if path == h.settings || path == filepath.Join(h.m.Home, ".gemini/config/hooks.json") || path == filepath.Join(h.m.Home, ".gemini/config/mcp_config.json") {
			continue
		}
		t.Fatalf("created path outside isolation: %s", path)
	}
	// Snapshot the complete home, not just the runner's instrumented writes:
	// fake tool effects and subprocess-created claim files must be gone too.
	allowed := map[string]bool{h.m.Home: true, h.m.Isolation: true, filepath.Join(h.m.Home, agyCheckMarkerName): true, h.settings: true}
	for _, p := range []string{".gemini", ".gemini/antigravity-cli", ".gemini/antigravity-cli/builtin", ".gemini/antigravity-cli/builtin/fixture"} {
		allowed[filepath.Join(h.m.Home, p)] = true
	}
	err = filepath.WalkDir(h.m.Home, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !allowed[path] {
			t.Fatalf("unexpected created path remains: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Predicate deviations need one real row's collection and effects. The other
// rows use the same passing observations as the report unit tests; full command
// coverage remains in match, larger-calls, timeout and the lifecycle cases.
func (h *agyCheckCommandHarness) runRow(t *testing.T, id string) (int, string) {
	t.Helper()
	row := agyCheckObserveFindRow(t, id)
	s, err := agyCheckScenarioFor(id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := agyCheckNewRowDirs(h.m.Isolation, h.x.runner.files)
	if err != nil {
		t.Fatal(err)
	}
	config, fixtures, err := s.config(h.tool, row, r)
	if err != nil {
		t.Fatal(err)
	}
	var evidence agyCheckEvidence
	var diagnostic string
	h.x.runner.check = h.d
	run, err := agyCheckRunAgy(context.Background(), agyCheckOptions{home: h.m.Home, agy: h.agy, model: agyCheckModel}, h.x.runner, h.m, r, s.prompt(r, 30), config, fixtures, h.x.limit, h.x.ops, func(r agyCheckRowDirs) error {
		var err error
		evidence, diagnostic, err = agyCheckCollect(h.m.Isolation, r, s, h.x.runner.files)
		return err
	})
	if err != nil {
		t.Fatalf("row execution failed: %v", err)
	}
	observation := agyCheckEvidenceObservation(row, s, evidence)
	if diagnostic != "" {
		observation = agyCheckObservation{ID: id, Outcome: diagnostic}
	}
	_, observations := agyCheckFixtures()
	for i := range observations {
		if observations[i].ID == id {
			observations[i] = observation
		}
	}
	parsed, _ := agyCheckTranscriptParse(evidence.Input.Transcript)
	result := agyCheckEvaluate(run.Before, run.After, observations, 30, "", parsed.LoadedPaths...)
	listing, err := agyCheckBuiltinListing(h.m.Home, h.x.runner.files)
	if err != nil {
		t.Fatal(err)
	}
	result.BuiltinInfo = agyCheckBuiltinInfo(listing)
	var out strings.Builder
	if err := agyCheckRender(&out, result); err != nil {
		t.Fatal(err)
	}
	return agyCheckExitCode(result), out.String()
}

func TestAgyCheckCommandEndToEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, row, deviation string
		calls, code          int
	}{
		{"match", "", "real-hooks", 0, 0}, {"larger-calls", "", "", 42, 0},
		{"flipped", "hook-silent", "flip", 0, 1},
		{"deny-other-cause", "hook-decision", "no-claim", 0, 1},
		{"hook-never-ran", "task-on", "no-claim", 0, 1},
		{"stub-never-started", "mcp-on", "no-stub", 0, 1},
		{"stub-response", "mcp-off", "stub-response", 0, 1},
		{"resource-missing-response", "read-resource", "missing-response", 0, 1},
		{"resource-denied", "list-resources", "flip", 0, 1},
		{"retry", "task-off", "retry", 0, 1},
		{"same-name-missing-home", "hook-same-name-agent", "one-workspace-claim", 0, 1},
		{"wrong-tag", "command-on", "wrong-tag", 0, 1},
		{"gap", "reconciliation", "gap", 0, 1},
		{"contradiction", "reconciliation", "contradictory", 0, 1},
		{"extra-file", "reconciliation", "extra-file", 0, 1},
		{"protected", "task-on", "protected-load", 0, 0},
		{"unprotected", "task-on", "unprotected-load", 0, 1},
		{"unparsed", "hook-silent", "unparsed", 0, 1},
		{"nonzero-with-result", "hook-exit1", "nonzero-result", 0, 0},
		{"nonzero-without-result", "hook-exit1", "nonzero-no-result", 0, 1},
		{"timeout", "hook-silent", "timeout", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := agyCheckNewCommandHarness(t, tc.row, tc.deviation)
			if tc.name == "timeout" {
				// Only this fake sleeps for a minute. Five seconds still allows
				// ordinary modeled rows and their real stub to exit under race.
				h.x.limit = 5 * time.Second
			}
			var code int
			var out string
			if tc.row == "" || tc.name == "timeout" {
				code, out = h.run(t, tc.calls)
			} else {
				code, out = h.runRow(t, tc.row)
			}
			if code != tc.code {
				t.Fatalf("command exit=%d want %d:\n%s", code, tc.code, out)
			}
			if strings.Contains(out, "RESULT INVALID") || !strings.Contains(out, "Rows: 29;") && tc.deviation != "unprotected-load" {
				t.Fatalf("row run did not produce a valid table: %s", out)
			}
			wantDiagnostic := map[string]string{
				"hook-never-ran": "invalid hook claim",
				"gap":            "invalid call records",
				"contradiction":  "contradictory reconciliation records",
				"unparsed":       "unparsed transcript",
				"retry":          "no single intended step",
				"timeout":        "run timeout",
			}[tc.name]
			if wantDiagnostic != "" && !strings.Contains(out, wantDiagnostic) {
				t.Fatalf("row diagnostic changed: want %q in %s", wantDiagnostic, out)
			}
			if tc.name == "resource-denied" && !strings.Contains(out, "invalid stub evidence") {
				t.Fatalf("denied resource executed RPC: %s", out)
			}
			if tc.code == 0 && !strings.HasSuffix(out, agyCheckSuccessLine+"\n") {
				t.Fatal("verbatim success final line missing")
			}
			if tc.code == 1 {
				if !strings.Contains(out, "differing: 1\n") {
					t.Fatalf("single deviation changed other rows: %s", out)
				}
				if strings.Contains(out, agyCheckSuccessLine) {
					t.Fatal("difference printed success")
				}
				differing := false
				for _, line := range strings.Split(out, "\n") {
					if strings.HasPrefix(line, tc.row+" ") && strings.Contains(line, "differs") {
						differing = true
					}
				}
				if tc.deviation == "unprotected-load" {
					differing = strings.Contains(out, "unlisted-load")
				}
				if !differing {
					t.Fatalf("named row did not differ: %s", out)
				}
			}
			if tc.deviation == "protected-load" || tc.deviation == "unprotected-load" {
				if !strings.HasPrefix(out, "LOADED BUT UNLISTED") || strings.Index(out, "INFO builtin/") > strings.Index(out, "ID ") {
					t.Fatal("extra blocks in wrong order")
				}
			}
			validations, starts, pins := 3, 3, 2
			if tc.row == "" || tc.name == "timeout" {
				validations, starts, pins = 88, 87, 58
			}
			if h.validations != validations || h.starts != starts || h.pins != pins {
				t.Fatalf("per-start validation/pins lost: validations=%d starts=%d pins=%d", h.validations, h.starts, h.pins)
			}
			wantCalls := 30
			if tc.calls > 0 {
				wantCalls = tc.calls
			}
			if tc.row != "reconciliation" && !strings.Contains(out, fmt.Sprintf("steps=%d hooks=%d", wantCalls, wantCalls)) {
				t.Fatal("--calls not honoured")
			}
			h.restored(t)
		})
	}
}

func TestAgyCheckCommandInvalidLifecycle(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pin-before", "pin-after", "pin-between", "version-between", "executable", "validation", "marker-changed", "restore", "cleanup", "collection", "unmarked", "setup"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := agyCheckNewCommandHarness(t, "", "")
			switch name {
			case "setup":
				open := h.x.runner.files.open
				h.x.runner.files.open = func(path string, flags int, mode os.FileMode) (*os.File, error) {
					if filepath.Base(path) == agyCheckHookSentinel {
						return nil, os.ErrPermission
					}
					return open(path, flags, mode)
				}
			case "marker-changed":
				validate := h.x.runner.validate
				h.x.runner.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
					if h.validations == 4 {
						changed := h.m
						changed.User = "other"
						b, _ := json.Marshal(changed)
						if err := os.WriteFile(filepath.Join(h.m.Home, agyCheckMarkerName), b, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					return validate(o, d)
				}
			case "executable":
				open := h.d.open
				h.d.open = func(path string) (io.ReadCloser, error) {
					if h.pins == 1 {
						// Replace only the disposable fake with another intact signed
						// executable after --version, before hashing its real bytes.
						data, err := os.ReadFile("/bin/echo")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(h.agy); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(h.agy, data, 0o500); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(h.agy, 0o500); err != nil {
							t.Fatal(err)
						}
					}
					return open(path)
				}
			case "pin-before", "pin-after", "pin-between", "version-between":
				validate := h.x.runner.validate
				h.x.runner.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
					m, err := validate(o, d)
					at := map[string]int{"pin-before": 2, "pin-after": 4, "pin-between": 5, "version-between": 5}[name]
					if h.validations == at || name == "pin-between" && h.validations == 8 {
						version := "1.4.8"
						if name == "pin-before" {
							version = "unknown"
						}
						if h.validations == 8 {
							version = "1.4.7"
						}
						b, _ := json.Marshal(agyCheckRowFake{Rows: true, Version: version, Home: h.m.Home, Isolation: h.m.Isolation})
						if e := os.WriteFile(h.agy+".fixture", b, 0o600); e != nil {
							t.Fatal(e)
						}
					}
					return m, err
				}
			case "validation":
				validate := h.x.runner.validate
				h.x.runner.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
					m, err := validate(o, d)
					if h.validations == 6 {
						return m, errors.New("mid-run abort")
					}
					return m, err
				}
			case "restore":
				remove := h.x.runner.files.remove
				n := 0
				h.x.runner.files.remove = func(path string) error {
					if path == h.settings {
						n++
						if n == 2 {
							return os.ErrPermission
						}
					}
					return remove(path)
				}
			case "cleanup":
				remove := h.x.runner.files.remove
				h.x.runner.files.remove = func(path string) error {
					if filepath.Dir(path) == h.m.Isolation && strings.HasPrefix(filepath.Base(path), "run-") {
						return os.ErrPermission
					}
					return remove(path)
				}
			case "collection":
				read := h.x.runner.files.read
				h.x.runner.files.read = func(path string, cap int64) ([]byte, error) {
					if filepath.Base(path) == "out.jsonl" {
						return nil, os.ErrPermission
					}
					return read(path, cap)
				}
			case "unmarked":
				if err := os.Remove(filepath.Join(h.m.Home, agyCheckMarkerName)); err != nil {
					t.Fatal(err)
				}
			}
			code, out := h.run(t, 0)
			if code != 1 || !strings.Contains(out, "RESULT INVALID:") || strings.Contains(out, "Rows:") || strings.Contains(out, agyCheckSuccessLine) {
				t.Fatalf("invalid lifecycle accepted or table printed: code=%d %s", code, out)
			}
			if h.starts >= 87 {
				t.Fatal("remaining rows ran after invalidity")
			}
			if name == "unmarked" {
				if h.starts != 0 {
					t.Fatal("unmarked home started agy")
				}
				return
			}
			if name != "restore" && name != "cleanup" {
				h.restored(t)
			}
		})
	}
}
