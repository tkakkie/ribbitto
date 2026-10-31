package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type agyCheckKeptInfo struct {
	name string
	mode os.FileMode
	size int64
	stat *syscall.Stat_t
}

func (i agyCheckKeptInfo) Name() string       { return i.name }
func (i agyCheckKeptInfo) Mode() os.FileMode  { return i.mode }
func (i agyCheckKeptInfo) Size() int64        { return i.size }
func (i agyCheckKeptInfo) IsDir() bool        { return i.mode.IsDir() }
func (i agyCheckKeptInfo) ModTime() time.Time { return time.Time{} }
func (i agyCheckKeptInfo) Sys() any {
	if i.stat == nil {
		return nil
	}
	return i.stat
}

type agyCheckKeptNilStat struct{ agyCheckKeptInfo }

func (i agyCheckKeptNilStat) Sys() any { return (*syscall.Stat_t)(nil) }

func TestAgyCheckKeepFinalMetadata(t *testing.T) {
	for _, name := range []string{
		"valid", "off", "empty", "absent-empty", "absent-kept", "extra-entry", "missing-id", "wrong-id",
		"extra-file", "missing-file", "wrong-file", "stat-error", "kept-stat-error", "entries-error", "row-entries-error", "no-stat", "nil-stat",
		"kept-symlink", "row-symlink", "file-symlink", "file-directory", "file-special",
		"kept-mode", "row-mode", "file-mode", "kept-owner", "row-owner", "file-owner",
		"nlink-two", "out-oversize", "err-oversize",
	} {
		t.Run(name, func(t *testing.T) {
			const root, kept, row = "/isolation", "/isolation/kept", "/isolation/kept/hook-silent"
			out, stderr := row+"/out.jsonl", row+"/err.txt"
			f := agyCheckFiles{uid: 123}
			nodes := map[string]agyCheckKeptInfo{}
			for path, mode := range map[string]os.FileMode{kept: os.ModeDir | 0o700, row: os.ModeDir | 0o700, out: 0o600, stderr: 0o600} {
				nodes[path] = agyCheckKeptInfo{filepath.Base(path), mode, 0, &syscall.Stat_t{Uid: uint32(f.uid), Nlink: 1}}
			}
			info := nodes[out]
			info.size = agyCheckTranscriptMaxBytes
			nodes[out] = info
			info = nodes[stderr]
			info.size = 64 << 10
			nodes[stderr] = info
			ids := []string{"hook-silent"}
			switch name {
			case "empty", "absent-empty":
				ids = nil
				delete(nodes, row)
				delete(nodes, out)
				delete(nodes, stderr)
				if name == "absent-empty" {
					delete(nodes, kept)
				}
			case "absent-kept":
				delete(nodes, kept)
			case "extra-entry":
				info := nodes[row]
				info.name = "extra"
				nodes[kept+"/extra"] = info
				nodes[kept+"/extra/out.jsonl"] = nodes[out]
				nodes[kept+"/extra/err.txt"] = nodes[stderr]
			case "missing-id":
				delete(nodes, row)
				delete(nodes, out)
				delete(nodes, stderr)
			case "wrong-id":
				info := nodes[row]
				info.name = "other"
				nodes[kept+"/other"] = info
				delete(nodes, row)
				nodes[kept+"/other/out.jsonl"] = nodes[out]
				nodes[kept+"/other/err.txt"] = nodes[stderr]
				delete(nodes, out)
				delete(nodes, stderr)
			case "extra-file", "wrong-file":
				info := nodes[out]
				info.name = "extra"
				info.size = 0
				nodes[row+"/extra"] = info
				if name == "wrong-file" {
					delete(nodes, out)
				}
			case "missing-file":
				delete(nodes, out)
			default:
				path := out
				if strings.HasPrefix(name, "kept-") {
					path = kept
				} else if strings.HasPrefix(name, "row-") {
					path = row
				} else if name == "err-oversize" {
					path = stderr
				}
				info := nodes[path]
				switch {
				case strings.HasSuffix(name, "symlink"):
					info.mode = os.ModeSymlink | info.mode.Perm()
				case strings.HasSuffix(name, "mode"):
					info.mode |= 0o044
				case strings.HasSuffix(name, "owner"):
					info.stat.Uid++
				case strings.HasSuffix(name, "oversize"):
					info.size++
				case name == "file-directory":
					info.mode = os.ModeDir | 0o600
				case name == "file-special":
					info.mode = os.ModeNamedPipe | 0o600
				case name == "nlink-two":
					info.stat.Nlink = 2
				case name == "no-stat":
					info.stat = nil
				}
				nodes[path] = info
			}
			keptStats := 0
			f.lstat = func(path string) (os.FileInfo, error) {
				if name == "off" {
					t.Fatal("flag off inspected kept metadata")
				}
				if name == "stat-error" && path == out {
					return nil, os.ErrPermission
				}
				if name == "kept-stat-error" && path == kept {
					keptStats++
					if keptStats == 1 {
						return nil, os.ErrPermission
					}
				}
				info, ok := nodes[path]
				if !ok {
					return nil, os.ErrNotExist
				}
				if name == "nil-stat" && path == out {
					return agyCheckKeptNilStat{info}, nil
				}
				return info, nil
			}
			f.entriesN = func(path string, n int) ([]os.DirEntry, error) {
				if name == "entries-error" && path == kept || name == "row-entries-error" && path == row {
					return nil, os.ErrPermission
				}
				var entries []os.DirEntry
				for p, info := range nodes {
					if filepath.Dir(p) == path {
						entries = append(entries, fs.FileInfoToDirEntry(info))
					}
				}
				return entries[:min(n, len(entries))], nil
			}
			f.read = func(string, int64) ([]byte, error) { t.Fatal("read retained content"); return nil, nil }
			f.open = func(string, int, os.FileMode) (*os.File, error) { t.Fatal("opened retained content"); return nil, nil }
			result := agyCheckFinishKeep(agyCheckOptions{keepTranscripts: name != "off"}, root, f, ids, agyCheckResult{KeptRows: ids})
			want := "kept transcripts directory changed after retention"
			if name == "valid" || name == "off" || name == "empty" || name == "absent-empty" {
				want = ""
			}
			if result.InvalidReason != want || want != "" && agyCheckExitCode(result) != 1 || len(ids) > 0 && !reflect.DeepEqual(result.KeptRows, ids) {
				t.Fatalf("final metadata: %+v; want reason %q, IDs %v", result, want, ids)
			}
		})
	}
}

func TestAgyCheckKeepMidRunFailure(t *testing.T) {
	h := agyCheckNewCommandHarness(t, "hook-decision", "flip")
	validate := h.x.runner.validate
	failed := false
	stat := h.x.runner.files.lstat
	h.x.runner.files.lstat = func(path string) (os.FileInfo, error) {
		// A mid-run return lists kept IDs without the completed-run recheck.
		if failed && path == filepath.Join(h.m.Isolation, "kept") {
			t.Fatal("mid-run failure ran final metadata recheck")
		}
		return stat(path)
	}
	h.x.runner.validate = func(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
		// Row A completes its three validations; row B fails before starting.
		if h.validations == 4 {
			failed = true
			return agyCheckMarker{}, os.ErrPermission
		}
		return validate(o, d)
	}
	var out, stderr bytes.Buffer
	code := agyCheckCommand([]string{"--home", h.m.Home, "--agy", h.agy, "--keep-transcripts"}, &out, &stderr, h.d)
	if code != 1 || stderr.Len() != 0 || !strings.HasPrefix(out.String(), "RESULT INVALID: row failed: validation failed\n") || !strings.HasSuffix(out.String(), "kept transcripts: hook-decision\n") {
		t.Fatalf("mid-run failure report: code=%d %s %s", code, out.String(), stderr.String())
	}
	for _, name := range []string{"out.jsonl", "err.txt"} {
		if _, err := os.Lstat(filepath.Join(h.m.Isolation, "kept", "hook-decision", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgyCheckKeepMetadataReport(t *testing.T) {
	h := agyCheckNewCommandHarness(t, "", "")
	entries := h.x.runner.files.entriesN
	h.x.runner.files.entriesN = func(path string, n int) ([]os.DirEntry, error) {
		if path == filepath.Join(h.m.Isolation, "kept") {
			return nil, errors.New(agyCheckKeepMarker)
		}
		return entries(path, n)
	}
	var out, stderr bytes.Buffer
	code := agyCheckCommand([]string{"--home", h.m.Home, "--agy", h.agy, "--keep-transcripts"}, &out, &stderr, h.d)
	if code != 1 || stderr.Len() != 0 || !strings.Contains(out.String(), "RESULT INVALID: kept transcripts directory changed after retention\n") || !strings.HasSuffix(out.String(), "kept transcripts: reconciliation\n") || strings.Contains(out.String(), agyCheckKeepMarker) || strings.Contains(out.String(), h.m.Isolation) {
		t.Fatalf("final metadata failure report: code=%d %s %s", code, out.String(), stderr.String())
	}
}
