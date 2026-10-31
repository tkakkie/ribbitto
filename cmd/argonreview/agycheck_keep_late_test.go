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

// Late invocation failures invalidate the command, never create differing rows.
func TestAgyCheckKeepLateFailure(t *testing.T) {
	for _, name := range []string{"final-pin", "cleanup", "signal"} {
		t.Run(name, func(t *testing.T) {
			h := agyCheckNewCommandHarness(t, "", "")
			signals := make(chan os.Signal, 1)
			h.x.runner.signals = func(fn func(<-chan os.Signal) (int, error)) (int, error) { return fn(signals) }
			want := "pin failed"
			if name == "final-pin" {
				open := h.d.open
				h.d.open = func(path string) (io.ReadCloser, error) {
					if h.validations == 4 {
						return nil, errors.New(agyCheckKeepMarker)
					}
					return open(path)
				}
			} else {
				want = "cleanup failed"
				if name == "signal" {
					want = "agy-check interrupted"
				}
				remove := h.x.runner.files.remove
				h.x.runner.files.remove = func(path string) error {
					if filepath.Dir(path) == h.m.Isolation && strings.HasPrefix(filepath.Base(path), "run-") {
						if name == "cleanup" {
							return errors.New(agyCheckKeepMarker)
						}
						signals <- syscall.SIGTERM
					}
					return remove(path)
				}
			}
			agyCheckKeepGuard(t, nil, func() {
				var out, stderr bytes.Buffer
				code := agyCheckCommand([]string{"--home", h.m.Home, "--agy", h.agy, "--keep-transcripts"}, &out, &stderr, h.d)
				if code != 1 || stderr.Len() != 0 || h.starts != 3 ||
					out.String() != "RESULT INVALID: row failed: "+want+"\nkept transcripts: none\n" {
					t.Fatalf("late failure became a classified row: code=%d starts=%d %s %s", code, h.starts, out.String(), stderr.String())
				}
				if _, err := os.Lstat(filepath.Join(h.m.Isolation, "kept")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("otherwise-ok invocation retained: %v", err)
				}
			})
		})
	}
}
