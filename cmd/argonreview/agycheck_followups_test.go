package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

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
