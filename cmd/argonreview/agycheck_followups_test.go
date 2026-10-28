package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAgyCheckRowFailureAttribution(t *testing.T) {
	for _, step := range []string{"restoration", "execution"} {
		t.Run(step, func(t *testing.T) {
			h := agyCheckNewCommandHarness(t, "", "")
			failure := errors.New("private failure detail")
			injected := false
			if step == "restoration" {
				remove := h.x.runner.files.remove
				settingsRemovals := 0
				h.x.runner.files.remove = func(path string) error {
					if path == h.settings {
						settingsRemovals++
						if settingsRemovals == 2 {
							injected = true
							return failure
						}
					}
					return remove(path)
				}
			} else {
				scan := h.x.ops.scan
				h.x.ops.scan = func(pid int) ([]int, error) {
					if h.starts == 2 {
						injected = true
						return nil, failure
					}
					return scan(pid)
				}
			}
			code, report := h.run(t, 0)
			if !injected || h.starts != 3 || code != 1 || !strings.Contains(report, step+" failed") {
				t.Fatalf("missing %s failure: injected %v, starts %d, code %d, report %q", step, injected, h.starts, code, report)
			}
			if step == "restoration" && strings.Contains(report, "execution failed") {
				t.Fatalf("restoration-only failure labelled execution failed: %q", report)
			}
			if report != "RESULT INVALID: row failed: "+step+" failed\n" {
				t.Fatalf("unexpected row failure report: %q", report)
			}
		})
	}
}

func TestAgyCheckRowFailureMultipleSteps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []error
		want  string
	}{
		{"two", []error{agyCheckCleanup, agyCheckCollection}, "collection failed; cleanup failed"},
		{"several", []error{agyCheckInterruption, agyCheckExecution, agyCheckCleanup, agyCheckRestoration, agyCheckCollection, agyCheckPinning, agyCheckValidation, agyCheckCleanup}, "validation failed; pin failed; collection failed; restoration failed; cleanup failed; execution failed; agy-check interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := strings.Repeat("PRIVATE_CAUSE /host/path ENV_VALUE transcript\n\x1b[31m", 2048)
			err := agyCheckRowFailure(errors.Join(errors.Join(tc.steps...), errors.New(cause)))
			if strings.ContainsAny(err.Error(), "\r\n") || !strings.Contains(err.Error(), "; ") {
				t.Errorf("row error must use single-line step separators")
			}
			if err.Error() != "row failed: "+tc.want {
				t.Errorf("row error changed fixed label order or content")
			}
			for _, step := range tc.steps {
				if !errors.Is(err, step) {
					t.Errorf("row error lost step %q", step)
				}
			}
			var report bytes.Buffer
			if err := agyCheckRender(&report, agyCheckResult{InvalidReason: err.Error()}); err != nil {
				t.Fatal(err)
			}
			if report.Len() > 256 {
				t.Errorf("row report is unbounded: %d bytes", report.Len())
			}
			if strings.Count(report.String(), "\n") != 1 || !strings.HasSuffix(report.String(), "\n") {
				t.Error("rendered row report must occupy one line")
			}
			for _, sensitive := range []string{"PRIVATE_CAUSE", "/host/path", "ENV_VALUE", "transcript", "\x1b"} {
				if strings.Contains(err.Error(), sensitive) || strings.Contains(report.String(), sensitive) {
					t.Errorf("row error or report exposed raw cause %q", sensitive)
				}
			}
		})
	}
}

func TestAgyCheckRowFailureSteps(t *testing.T) {
	for _, step := range []string{"validation", "pin", "collection", "restoration", "cleanup"} {
		t.Run(step, func(t *testing.T) {
			h := agyCheckNewCommandHarness(t, "", "")
			canary := h.m.Home + " ENV_VALUE credentials transcript\n\x1b[31m"
			failure := errors.New(strings.Repeat(canary, 2048))
			switch step {
			case "validation":
				validate := h.x.runner.validate
				h.x.runner.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
					m, err := validate(o, d)
					if h.validations == 2 {
						err = failure
					}
					return m, err
				}
			case "pin":
				h.d.open = func(string) (io.ReadCloser, error) { return nil, failure }
			case "collection":
				read := h.x.runner.files.read
				h.x.runner.files.read = func(path string, cap int64) ([]byte, error) {
					if filepath.Base(path) == "out.jsonl" {
						return nil, failure
					}
					return read(path, cap)
				}
			case "restoration", "cleanup":
				remove := h.x.runner.files.remove
				settingsRemovals := 0
				h.x.runner.files.remove = func(path string) error {
					if path == h.settings {
						settingsRemovals++
					}
					if step == "restoration" && path == h.settings && settingsRemovals == 2 ||
						step == "cleanup" && filepath.Dir(path) == h.m.Isolation && strings.HasPrefix(filepath.Base(path), "run-") {
						return failure
					}
					return remove(path)
				}
			}
			code, report := h.run(t, 0)
			if code != 1 || !strings.HasPrefix(report, "RESULT INVALID: row failed:") || !strings.Contains(report, step+" failed") {
				t.Fatalf("missing failing step %s: code %d, report %q", step, code, report)
			}
			if len(report) > 256 {
				t.Fatalf("row detail is unbounded: %d bytes", len(report))
			}
			for _, sensitive := range []string{h.m.Home, "ENV_VALUE", "credentials", "transcript", "\x1b", agyCheckSuccessLine} {
				if strings.Contains(report, sensitive) {
					t.Fatalf("row report leaked %q", sensitive)
				}
			}
		})
	}
	joined := agyCheckRowFailure(errors.Join(agyCheckCollection, agyCheckCleanup, errors.New("private detail")))
	if !errors.Is(joined, agyCheckCollection) || !errors.Is(joined, agyCheckCleanup) || strings.Contains(joined.Error(), "private detail") {
		t.Fatal("joined step details lost or raw cause exposed")
	}
}

func TestAgyCheckRunnerLateSignal(t *testing.T) {
	for _, at := range []string{"final validation", "final pin", "collection", "cleanup"} {
		t.Run(at, func(t *testing.T) {
			h := agyCheckNewRunner(t)
			signals := make(chan os.Signal, 1)
			handling := false
			h.d.signals = func(fn func(<-chan os.Signal) (int, error)) (int, error) {
				handling = true
				defer func() { handling = false }()
				return fn(signals)
			}
			send := func() { signals <- syscall.SIGTERM }
			validate := h.d.validate
			h.d.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
				m, err := validate(o, d)
				if at == "final validation" && h.validations == 3 {
					send()
				}
				return m, err
			}
			open := h.d.check.open
			h.d.check.open = func(path string) (io.ReadCloser, error) {
				if at == "final pin" && h.validations == 3 {
					send()
				}
				return open(path)
			}
			remove := h.d.files.remove
			h.d.files.remove = func(path string) error {
				err := remove(path)
				if at == "cleanup" && path == h.r.Run {
					if !handling {
						t.Error("signal handling ended before cleanup")
					}
					send()
				}
				return err
			}
			collected := false
			_, err := h.run(t, func(agyCheckRowDirs) error {
				collected = true
				if at == "collection" {
					send()
				}
				return nil
			})
			if !errors.Is(err, agyCheckInterruption) || !collected || h.starts != 3 || len(signals) != 0 {
				t.Fatalf("late signal lost or collection skipped: err %v, collected %v, starts %d, pending %d", err, collected, h.starts, len(signals))
			}
			if _, err := os.Lstat(h.r.Run); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("late signal skipped cleanup")
			}
		})
	}
}
