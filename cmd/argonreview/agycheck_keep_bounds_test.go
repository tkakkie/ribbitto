package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestAgyCheckKeepReadDirBound(t *testing.T) {
	root := agyCheckRealTemp(t)
	for i := range 100 {
		agyCheckTestWrite(t, filepath.Join(root, fmt.Sprint(i)), nil, 0o600)
	}
	agyCheckKeepGuard(t, nil, func() {
		entries, err := agyCheckReadDirN(root, 3)
		if err != nil || len(entries) != 3 {
			t.Fatalf("directory flood was not bounded: count=%d err=%v", len(entries), err)
		}
	})
}

func TestAgyCheckKeepListingFlood(t *testing.T) {
	for _, name := range []string{"ready", "final-kept", "final-row", "cleanup-kept", "cleanup-row"} {
		t.Run(name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			for _, file := range []string{"out.jsonl", "err.txt"} {
				agyCheckTestWrite(t, filepath.Join(r.Row, file), nil, 0o600)
			}
			agyCheckKeepGuard(t, nil, func() {
				if _, err := agyCheckKeepRow(m.Isolation, r.Row, "hook-silent", f); err != nil {
					t.Fatal(err)
				}
				kept := filepath.Join(m.Isolation, "kept")
				row := filepath.Join(kept, "hook-silent")
				target, limit := kept, len(agyCheckExpected)
				if name == "ready" {
					limit = 0
				}
				if name == "final-row" || name == "cleanup-row" {
					target, limit = row, 2
				}
				entries, unlimited := f.entriesN, f.entries
				consumed := 0
				f.entriesN = func(path string, n int) ([]os.DirEntry, error) {
					if path != target {
						return entries(path, n)
					}
					if n != limit+1 {
						t.Fatalf("flood requested %d entries; want %d", n, limit+1)
					}
					// A virtual million-entry directory must supply only the request.
					consumed += n
					entry := fs.FileInfoToDirEntry(agyCheckKeptInfo{name: "hook-silent", mode: os.ModeDir | 0o700})
					result := make([]os.DirEntry, n)
					for i := range result {
						result[i] = entry
					}
					return result, nil
				}
				f.entries = func(path string) ([]os.DirEntry, error) {
					if path == kept || filepath.Dir(path) == kept {
						t.Fatal("retained directory used an unbounded listing")
					}
					return unlimited(path)
				}
				switch name {
				case "ready":
					if err := agyCheckKeepReady(m.Isolation, f); err == nil {
						t.Fatal("ready accepted directory flood")
					}
				case "final-kept", "final-row":
					result := agyCheckFinishKeep(agyCheckOptions{keepTranscripts: true}, m.Isolation, f, []string{"hook-silent"}, agyCheckResult{})
					if result.InvalidReason != "kept transcripts directory changed after retention" {
						t.Fatalf("final recheck accepted directory flood: %+v", result)
					}
				default:
					if err := f.cleanup(m.Isolation, target); err == nil {
						t.Fatal("cleanup accepted directory flood")
					}
				}
				if consumed != limit+1 {
					t.Fatalf("flood consumption: %d want %d", consumed, limit+1)
				}
			})
		})
	}
}

func TestAgyCheckKeepCleanupNestedDirectory(t *testing.T) {
	m, _, f := agyCheckTestRow(t)
	nested := filepath.Join(m.Isolation, "kept", "hook-silent", "out.jsonl")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	f.entries = func(string) ([]os.DirEntry, error) {
		t.Fatal("cleanup listed an unexpected nested retained directory")
		return nil, nil
	}
	agyCheckKeepGuard(t, nil, func() {
		if err := f.cleanup(m.Isolation, nested); err == nil {
			t.Fatal("cleanup accepted a nested retained directory")
		}
	})
}
