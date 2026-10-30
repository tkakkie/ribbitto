package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgyCheckKeepCleanupPriorReason(t *testing.T) {
	m, r, f := agyCheckTestRow(t)
	for _, name := range []string{"out.jsonl", "err.txt"} {
		agyCheckTestWrite(t, filepath.Join(r.Row, name), []byte(agyCheckKeepMarker), 0o600)
	}
	if _, err := agyCheckKeepRow(m.Isolation, r.Row, "discovery-root", f); err != nil {
		t.Fatal(err)
	}
	f.remove = func(string) error { return errors.New(agyCheckKeepMarker) }
	const prior = "pin changed after rows"
	result := agyCheckFinishKeep(agyCheckOptions{keepTranscripts: true}, m.Isolation, f, []string{"discovery-root"}, agyCheckResult{
		InvalidReason: prior,
		Rows:          []agyCheckResultRow{{agyCheckRow: agyCheckRow{ID: "discovery-root"}, OK: true}},
	})
	if result.InvalidReason != prior || agyCheckExitCode(result) != 1 || !reflect.DeepEqual(result.KeptRows, []string{"discovery-root"}) {
		t.Fatalf("cleanup replaced earlier reason or lost remaining copy: %+v", result)
	}
}

func TestAgyCheckKeepPartialReport(t *testing.T) {
	for _, name := range []string{"after-first-write", "after-mkdir", "before-mkdir-path", "before-mkdir-private"} {
		t.Run(name, func(t *testing.T) {
			h := agyCheckNewCommandHarness(t, "hook-decision", "flip")
			kept := filepath.Join(h.m.Isolation, "kept")
			row := filepath.Join(kept, "hook-decision")
			created := false
			mkdir := h.x.runner.files.mkdir
			h.x.runner.files.mkdir = func(path string, mode os.FileMode) error {
				err := mkdir(path, mode)
				if path == row && err == nil {
					created = true
				}
				return err
			}
			stat := h.x.runner.files.lstat
			h.x.runner.files.lstat = func(path string) (os.FileInfo, error) {
				if name == "after-mkdir" && path == row && created ||
					name == "before-mkdir-path" && path == kept && h.starts >= 3 {
					return nil, os.ErrPermission
				}
				info, err := stat(path)
				if name == "before-mkdir-private" && path == kept && err == nil {
					return agyCheckOwnedInfo{info, info.Mode(), os.Getuid() + 1}, nil
				}
				return info, err
			}
			open := h.x.runner.files.open
			h.x.runner.files.open = func(path string, flags int, mode os.FileMode) (*os.File, error) {
				if name == "after-first-write" && path == filepath.Join(row, "err.txt") {
					return nil, os.ErrPermission
				}
				return open(path, flags, mode)
			}
			var out, stderr bytes.Buffer
			code := agyCheckCommand([]string{"--home", h.m.Home, "--agy", h.agy, "--keep-transcripts"}, &out, &stderr, h.d)
			wantID := "none"
			if name == "after-first-write" || name == "after-mkdir" {
				wantID = "hook-decision"
			}
			if code != 1 || stderr.Len() != 0 || out.String() != "RESULT INVALID: row failed: keeping transcripts failed\nkept transcripts: "+wantID+"\n" {
				t.Fatalf("partial-copy report: code=%d %s %s", code, out.String(), stderr.String())
			}
			if created != (wantID != "none") || h.starts != 3 {
				t.Fatalf("creation boundary/early stop: created=%v starts=%d", created, h.starts)
			}
			entries, err := os.ReadDir(row)
			if wantID == "none" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pre-mkdir refusal created a row: %v %v", entries, err)
				}
			} else {
				wantFiles := 0
				if name == "after-first-write" {
					wantFiles = 1
				}
				if err != nil || len(entries) != wantFiles || wantFiles == 1 && entries[0].Name() != "out.jsonl" {
					t.Fatalf("partial copy missing: %v %v", entries, err)
				}
				if wantFiles == 1 {
					data, err := os.ReadFile(filepath.Join(row, "out.jsonl"))
					if err != nil || len(data) == 0 {
						t.Fatalf("first write did not leave content: %d %v", len(data), err)
					}
				}
			}
		})
	}
}

func TestAgyCheckKeepUnremovableReport(t *testing.T) {
	h := agyCheckNewCommandHarness(t, "", "")
	row := filepath.Join(h.m.Isolation, "kept", "discovery-root")
	remove := h.x.runner.files.remove
	h.x.runner.files.remove = func(path string) error {
		if filepath.Dir(path) == row {
			return os.ErrPermission
		}
		return remove(path)
	}
	var out, stderr bytes.Buffer
	code := agyCheckCommand([]string{"--home", h.m.Home, "--agy", h.agy, "--keep-transcripts"}, &out, &stderr, h.d)
	if code != 1 || stderr.Len() != 0 || !strings.Contains(out.String(), "RESULT INVALID: keeping transcripts failed\n") ||
		!strings.HasSuffix(out.String(), "kept transcripts: discovery-root, reconciliation\n") {
		t.Fatalf("unremovable-copy report: code=%d %s %s", code, out.String(), stderr.String())
	}
	entries, err := os.ReadDir(row)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unremovable copy missing: %v %v", entries, err)
	}
}

func TestAgyCheckKeepPartialFinalRecheck(t *testing.T) {
	for _, prior := range []string{"", "pin changed after rows"} {
		t.Run(prior, func(t *testing.T) {
			m, _, f := agyCheckTestRow(t)
			const id = "hook-silent"
			agyCheckTestWrite(t, filepath.Join(m.Isolation, "kept", id, "out.jsonl"), []byte(agyCheckKeepMarker), 0o600)
			f.read = func(string, int64) ([]byte, error) { t.Fatal("read partial retained content"); return nil, nil }
			f.open = func(string, int, os.FileMode) (*os.File, error) {
				t.Fatal("opened partial retained content")
				return nil, nil
			}
			result := agyCheckFinishKeep(agyCheckOptions{keepTranscripts: true}, m.Isolation, f, []string{id}, agyCheckResult{InvalidReason: prior})
			want := prior
			if want == "" {
				want = "kept transcripts directory changed after retention"
			}
			if result.InvalidReason != want || agyCheckExitCode(result) != 1 || !reflect.DeepEqual(result.KeptRows, []string{id}) {
				t.Fatalf("partial-copy final recheck: %+v; want reason %q", result, want)
			}
		})
	}
}
