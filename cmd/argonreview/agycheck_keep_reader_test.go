package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A regression must fail locally, not leave a FIFO open hanging the suite.
func agyCheckKeepGuard(t *testing.T, release func(), run func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); run() }()
	select {
	case <-done:
		return
	case <-time.After(10 * time.Second):
		t.Error("retention did not finish before timeout")
	}
	if release != nil {
		release()
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retention still blocked after release")
	}
}

func agyCheckKeepReleaseFIFO(path string) {
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err == nil {
		_ = file.Close()
	}
}

func TestAgyCheckKeepPrefix(t *testing.T) {
	for _, name := range []string{"regular", "cap", "fifo", "device", "swapped-fifo", "swapped-owner", "swapped-mode", "swapped-links", "closed"} {
		t.Run(name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			path := filepath.Join(r.Row, "out.jsonl")
			data := []byte(agyCheckKeepMarker)
			agyCheckTestWrite(t, path, data, 0o600)
			cap := int64(len(data))
			if name == "cap" {
				cap = 7
			}
			if name == "fifo" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "swapped-owner" {
				f.uid++
				lstat := f.lstat
				f.lstat = func(p string) (os.FileInfo, error) {
					info, err := lstat(p)
					if err == nil && p == path {
						return agyCheckOwnedInfo{info, info.Mode(), f.uid}, nil
					}
					return info, err
				}
			}
			open := f.open
			var opened *os.File
			f.open = func(p string, flags int, mode os.FileMode) (*os.File, error) {
				if name == "fifo" {
					t.Error("plain FIFO reached open instead of failing the private pre-check")
				}
				switch name {
				case "swapped-fifo":
					if err := os.Remove(p); err != nil {
						return nil, err
					}
					if err := syscall.Mkfifo(p, 0o600); err != nil {
						return nil, err
					}
				case "swapped-mode":
					if err := os.Chmod(p, 0o640); err != nil {
						return nil, err
					}
				case "swapped-links":
					if err := os.Link(p, p+".link"); err != nil {
						return nil, err
					}
				case "device":
					p = "/dev/null"
				}
				file, err := open(p, flags, mode)
				opened = file
				if name == "closed" && err == nil {
					err = file.Close()
				}
				return file, err
			}
			agyCheckKeepGuard(t, func() { agyCheckKeepReleaseFIFO(path) }, func() {
				got, err := f.readPrefixInside(m.Isolation, path, cap)
				if name == "regular" || name == "cap" {
					if err != nil || !bytes.Equal(got, data[:cap]) {
						t.Fatalf("prefix/cap: %q %v", got, err)
					}
				} else if err == nil {
					t.Fatal("unsafe opened transcript accepted")
				}
				if opened != nil {
					if _, err := opened.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatalf("transcript descriptor left open: %v", err)
					}
				}
			})
		})
	}
}

func TestAgyCheckKeepPrefixMetadata(t *testing.T) {
	for _, name := range []string{"valid", "owner", "mode", "setuid", "links", "fifo", "no-stat", "nil-stat"} {
		t.Run(name, func(t *testing.T) {
			info := agyCheckKeptInfo{mode: 0o600, stat: &syscall.Stat_t{Uid: 123, Nlink: 1}}
			switch name {
			case "owner":
				info.stat.Uid++
			case "mode":
				info.mode = 0o640
			case "setuid":
				info.mode |= os.ModeSetuid
			case "links":
				info.stat.Nlink = 2
			case "fifo":
				info.mode |= os.ModeNamedPipe
			case "no-stat", "nil-stat":
				info.stat = nil
			}
			var metadata os.FileInfo = info
			if name == "nil-stat" {
				metadata = agyCheckKeptNilStat{info}
			}
			if agyCheckPrefixMetadata(metadata, 123) != (name == "valid") {
				t.Fatal("opened metadata accepted incorrectly")
			}
		})
	}
}

func TestAgyCheckKeepSwappedFIFOCommand(t *testing.T) {
	h := agyCheckNewCommandHarness(t, "hook-decision", "flip")
	open := h.x.runner.files.open
	var swapped string
	fifo := make(chan string, 1)
	h.x.runner.files.open = func(path string, flags int, mode os.FileMode) (*os.File, error) {
		if filepath.Base(path) == "out.jsonl" && flags&syscall.O_ACCMODE == os.O_RDONLY {
			if err := os.Rename(path, path+".original"); err != nil {
				return nil, err
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				return nil, err
			}
			swapped = path
			fifo <- path
			file, err := open(path, flags, mode)
			// Restore the original before cleanup; validation must use the fd,
			// and cleanup must still run after the retention failure.
			restoreErr := os.Rename(path+".original", path)
			return file, errors.Join(err, restoreErr)
		}
		return open(path, flags, mode)
	}
	agyCheckKeepGuard(t, func() {
		select {
		case path := <-fifo:
			agyCheckKeepReleaseFIFO(path)
		default:
		}
	}, func() {
		var out, stderr bytes.Buffer
		code := agyCheckCommand([]string{"--home", h.m.Home, "--agy", h.agy, "--keep-transcripts"}, &out, &stderr, h.d)
		if swapped == "" || h.starts != 3 || code != 1 || stderr.Len() != 0 ||
			out.String() != "RESULT INVALID: row failed: keeping transcripts failed\nkept transcripts: hook-decision\n" {
			t.Fatalf("swapped FIFO report: code=%d starts=%d %s %s", code, h.starts, out.String(), stderr.String())
		}
		entries, err := os.ReadDir(h.m.Isolation)
		if err != nil || len(entries) != 1 || entries[0].Name() != "kept" {
			t.Fatalf("row cleanup lost: %v %v", entries, err)
		}
		data, err := os.ReadFile(h.settings)
		if err != nil || string(data) != "original marked-home settings\n" {
			t.Fatalf("settings restoration lost: %q %v", data, err)
		}
	})
}
