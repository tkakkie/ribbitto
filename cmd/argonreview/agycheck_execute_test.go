package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Script=false preserves the existing pin helper's fixture and behaviour.
type agyCheckFakeScript struct {
	Script                                                                      bool
	Version, NextVersion, Home, Cwd, Tmp, Prompt, Model, PrintLimit, Transcript string
	Isolation                                                                   string
	VersionCode                                                                 int
	Code                                                                        int
	Sleep                                                                       time.Duration
	Config                                                                      map[string]string
	Fixtures                                                                    map[string]string
}

func agyCheckScriptedFake(data []byte) bool {
	var f agyCheckFakeScript
	if json.Unmarshal(data, &f) != nil || !f.Script {
		return false
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		tmp := os.Getenv("TMPDIR")
		cwd, err := os.Getwd()
		if err != nil || cwd != tmp || filepath.Dir(tmp) != f.Isolation || !strings.HasPrefix(filepath.Base(tmp), "pin-") || os.Getenv("HOME") != f.Home {
			os.Exit(94)
		}
		fmt.Println(f.Version)
		os.Exit(f.VersionCode)
	}
	cwd, err := os.Getwd()
	want := []string{"-p", f.Prompt, "--model", f.Model, "--sandbox", "--print-timeout", f.PrintLimit, "--output-format", "stream-json"}
	env := []string{"HOME=" + f.Home, "TMPDIR=" + f.Tmp, "PATH=" + agyCheckRunPath}
	gotEnv := os.Environ()
	slices.Sort(gotEnv)
	slices.Sort(env)
	if err != nil || cwd != f.Cwd || !slices.Equal(os.Args[1:], want) || !slices.Equal(gotEnv, env) {
		fmt.Fprintln(os.Stderr, "fake argv/cwd/environment mismatch")
		os.Exit(91)
	}
	for root, files := range map[string]map[string]string{f.Home: f.Config, f.Cwd: f.Fixtures} {
		for rel, want := range files {
			got, e := os.ReadFile(filepath.Join(root, rel))
			info, se := os.Lstat(filepath.Join(root, rel))
			if e != nil || se != nil || string(got) != want || info.Mode() != 0o600 {
				fmt.Fprintln(os.Stderr, "fake configuration/fixture mismatch")
				os.Exit(92)
			}
		}
	}
	if f.NextVersion != "" {
		f.Version = f.NextVersion
		f.NextVersion = ""
		data, e := json.Marshal(f)
		if e != nil || os.WriteFile(os.Args[0]+".fixture", data, 0o600) != nil {
			os.Exit(93)
		}
	}
	fmt.Print(f.Transcript)
	time.Sleep(f.Sleep)
	os.Exit(f.Code)
	return true
}
func agyCheckScriptFixture(t *testing.T, path string, f agyCheckFakeScript) {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	agyCheckTestWrite(t, path+".fixture", data, 0o600)
}
func agyCheckToolBinary(t *testing.T) string {
	t.Helper()
	return agyCheckLinkedBinary(t, "agy-check-tool")
}

type agyCheckRunnerHarness struct {
	m           agyCheckMarker
	r           agyCheckRowDirs
	d           agyCheckRunnerDeps
	o           agyCheckOptions
	config      agyCheckConfigFiles
	script      agyCheckFakeScript
	ops         cleanupOps
	events      []string
	validations int
	starts      int
	limit       time.Duration
}

func agyCheckNewRunner(t *testing.T) *agyCheckRunnerHarness {
	t.Helper()
	m, r, f := agyCheckTestRow(t)
	path, err := filepath.EvalSymlinks(agyCheckFakeBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	h := &agyCheckRunnerHarness{m: m, r: r, o: agyCheckOptions{home: m.Home, agy: path, model: "fixture-model"}, config: agyCheckConfigFiles{[]byte("hooks"), []byte("settings"), []byte("mcp")}, limit: 10 * time.Second}
	h.script = agyCheckFakeScript{Script: true, Version: "1.4.7", Home: m.Home, Isolation: m.Isolation, Cwd: r.Workspace, Tmp: r.Tmp, Prompt: "fixture prompt", Model: h.o.model, PrintLimit: "5s", Transcript: "{\"event\":\"result\"}\n", Code: 7,
		Config: map[string]string{".gemini/config/hooks.json": "hooks", ".gemini/config/mcp_config.json": "mcp", ".gemini/antigravity-cli/settings.json": "settings"}, Fixtures: map[string]string{".agent/hooks.json": "workspace"}}
	h.d = agyCheckRunnerDefaults()
	h.d.files = f
	h.d.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
		h.validations++
		h.events = append(h.events, "validate")
		return h.m, nil
	}
	h.d.signals = func(fn func(<-chan os.Signal) (int, error)) (int, error) { return fn(nil) }
	h.d.check.open = func(string) (io.ReadCloser, error) {
		h.events = append(h.events, "pin")
		return io.NopCloser(strings.NewReader("executable")), nil
	}
	h.ops = systemCleanup()
	wait := h.ops.wait
	seen := make(map[int]bool)
	h.ops.wait = func(pid int, status *syscall.WaitStatus) (int, error) {
		if !seen[pid] {
			seen[pid] = true
			h.starts++
			kind := "start-version"
			if h.starts == 2 {
				kind = "start-agent"
			}
			h.events = append(h.events, kind)
		}
		return wait(pid, status)
	}
	// This helper never forks. Real StartProcess, wait and group termination stay;
	// ps enumeration is separately injected, because #183 forbids ps here.
	h.ops.scan = func(int) ([]int, error) { return nil, nil }
	return h
}
func (h *agyCheckRunnerHarness) run(t *testing.T, collect func(agyCheckRowDirs) error) (agyCheckRunResult, error) {
	t.Helper()
	agyCheckScriptFixture(t, h.o.agy, h.script)
	return agyCheckRunAgy(context.Background(), h.o, h.d, h.m, h.r, h.script.Prompt, h.config, map[string][]byte{".agent/hooks.json": []byte("workspace")}, h.limit, h.ops, collect)
}

func TestAgyCheckRunner(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"success", "version", "hash", "pin-error", "pin-after-error", "pin-version-failure", "malformed-pin", "validate-before", "validate-main", "validate-after", "marker-change", "oversize", "timeout", "signal", "panic", "collect-error", "cleanup-error", "straggler", "straggler-failure", "reused", "long-limit"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := agyCheckNewRunner(t)
			original := []byte("original home configuration\n")
			path := filepath.Join(h.m.Home, ".gemini/config/hooks.json")
			agyCheckTestWrite(t, path, original, 0o640)
			switch name {
			case "version":
				h.script.NextVersion = "1.4.8"
			case "hash":
				open := h.d.check.open
				h.d.check.open = func(p string) (io.ReadCloser, error) {
					if h.validations == 3 {
						h.events = append(h.events, "pin")
						return io.NopCloser(strings.NewReader("changed")), nil
					}
					return open(p)
				}
			case "pin-error":
				h.d.check.open = func(string) (io.ReadCloser, error) { return nil, os.ErrPermission }
			case "pin-after-error":
				open := h.d.check.open
				h.d.check.open = func(p string) (io.ReadCloser, error) {
					if h.validations == 3 {
						return nil, os.ErrPermission
					}
					return open(p)
				}
			case "long-limit":
				h.limit = 10 * time.Minute
				h.script.PrintLimit = "8m0s"
			case "pin-version-failure":
				h.script.VersionCode = 3
			case "malformed-pin":
				h.script.Version = "unknown"
			case "validate-before", "validate-main", "validate-after", "marker-change":
				original := h.d.validate
				h.d.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
					m, e := original(o, d)
					at := map[string]int{"validate-before": 1, "validate-main": 2, "validate-after": 3, "marker-change": 2}[name]
					if h.validations == at {
						if name == "marker-change" {
							m.User = "changed"
						} else {
							e = errors.New("fresh validation refused")
						}
					}
					return m, e
				}
			case "oversize":
				h.script.Transcript = strings.Repeat("x", agyCheckTranscriptMaxBytes+1)
			case "timeout":
				h.limit = 200 * time.Millisecond
				h.script.PrintLimit = "100ms"
				h.script.Sleep = 5 * time.Second
			case "signal":
				h.script.Sleep = 5 * time.Second
				signals := make(chan os.Signal, 1)
				wait := h.ops.wait
				sent := false
				h.ops.wait = func(pid int, status *syscall.WaitStatus) (int, error) {
					n, e := wait(pid, status)
					if !sent && h.validations == 2 {
						sent = true
						signals <- syscall.SIGTERM
					}
					return n, e
				}
				h.d.signals = func(fn func(<-chan os.Signal) (int, error)) (int, error) { return fn(signals) }
			case "cleanup-error":
				remove := h.d.files.remove
				h.d.files.remove = func(p string) error {
					if p == h.r.Run {
						return os.ErrPermission
					}
					return remove(p)
				}
			case "straggler":
				scans := 0
				h.ops.scan = func(int) ([]int, error) {
					scans++
					if scans == 2 {
						return []int{999999}, nil
					}
					return nil, nil
				}
				h.ops.killMember = func(int, *child) { h.events = append(h.events, "kill") }
				h.ops.commandLine = func(int) string { return "fake straggler" }
			case "straggler-failure":
				h.ops.scan = func(int) ([]int, error) {
					if h.validations == 2 {
						return nil, errors.New("session scan failed")
					}
					return nil, nil
				}
			case "reused":
				agyCheckTestWrite(t, filepath.Join(h.r.Row, "started"), nil, 0o600)
			}
			collected := false
			result, err := h.run(t, func(r agyCheckRowDirs) error {
				collected = true
				h.events = append(h.events, "collect")
				if _, e := os.Lstat(r.Row); e != nil {
					t.Fatal("evidence cleaned before collect")
				}
				data, e := os.ReadFile(path)
				if e != nil || string(data) != string(original) {
					t.Fatal("collect ran before home restoration")
				}
				if name == "panic" {
					panic("collect panic")
				}
				if name == "collect-error" {
					return errors.New("collect failed")
				}
				return nil
			})
			wantErr := name != "success" && name != "timeout" && name != "signal" && name != "straggler" && name != "long-limit"
			if (err != nil) != wantErr {
				t.Fatalf("runner invalidity lost: result=%+v err=%v", result, err)
			}
			if name == "reused" {
				if h.validations != 0 {
					t.Fatal("reused row started agy")
				}
				return
			}
			if name == "success" || name == "straggler" || name == "long-limit" {
				if h.validations != h.starts || h.starts != 3 || strings.Join(h.events, ",") != map[bool]string{false: "validate,start-version,pin,validate,start-agent,validate,start-version,pin,collect", true: "validate,start-version,pin,validate,start-agent,kill,validate,start-version,pin,collect"}[name == "straggler"] {
					t.Fatalf("fresh validation/pin/start order lost: %v", h.events)
				}
				if result.Code != 7 || string(result.Transcript) != h.script.Transcript || !collected {
					t.Fatalf("exit/transcript/collect lost: %+v", result)
				}
				if name == "straggler" && strings.Join(result.Stragglers, ",") != "fake straggler" {
					t.Fatal("straggler evidence lost")
				}
			}
			if name == "timeout" && result.Code != 124 {
				t.Fatalf("timeout not supervised: %d", result.Code)
			}
			if name == "signal" && result.Code != 143 {
				t.Fatalf("signal not supervised: %d", result.Code)
			}
			if name != "cleanup-error" {
				if _, e := os.Lstat(h.r.Run); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("run directory not cleaned")
				}
			}
			data, e := os.ReadFile(path)
			info, se := os.Lstat(path)
			if e != nil || se != nil || string(data) != string(original) || info.Mode() != 0o640 {
				t.Fatal("home not restored after runner exit")
			}
		})
	}
}
