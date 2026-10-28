package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// Each fixture has its own name and sidecar, but shares the immutable executable.
func agyCheckLinkedBinary(t *testing.T, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agyCheckRealTemp(t), name)
	if err := os.Link(self, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// Removing private() must not redirect its injected fault into path(), which
// would let a second guard hide a missing metadata check in a mutation run.
func agyCheckPrivateStat() bool {
	pc, _, _, ok := runtime.Caller(2)
	return ok && strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".agyCheckHookPrivate")
}

func agyCheckPathStat() bool {
	pc, _, _, ok := runtime.Caller(2)
	return ok && strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".agyCheckFiles.path")
}

func TestAgyCheckRunnerExitCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code    int
		signal  syscall.Signal
		wantErr bool
	}{
		{128, 0, true}, {130, 0, true}, {127, 0, false}, {124, 0, false},
		{137, syscall.SIGKILL, true},
	} {
		t.Run(fmt.Sprintf("code-%d", tc.code), func(t *testing.T) {
			t.Parallel()
			h := agyCheckNewRunner(t)
			h.script.Code, h.script.Signal = tc.code, tc.signal
			collected := false
			result, err := h.run(t, func(agyCheckRowDirs) error {
				collected = true
				return nil
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("exit code invalidity lost: code=%d err=%v", result.Code, err)
			}
			if result.Code != tc.code || !collected || h.starts != 3 {
				t.Fatalf("exit code or final pin/collect lost: %+v collected=%v starts=%d", result, collected, h.starts)
			}
		})
	}
}

func TestAgyCheckClaimsStatePrivate(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "mode", "owner", "symlink", "file", "private"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			isolation := agyCheckRealTemp(t)
			dir := filepath.Join(isolation, "state")
			if name != "missing" {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, stub := range []bool{false, true} {
				f := agyCheckFilesDefaults()
				lstat := f.lstat
				f.lstat = func(p string) (os.FileInfo, error) {
					info, err := lstat(p)
					if err != nil {
						return info, err
					}
					if p == dir {
						// path() gets the real view; only private() gets the fault.
						if agyCheckPrivateStat() {
							switch name {
							case "mode":
								return agyCheckOwnedInfo{info, os.ModeDir | 0o750, f.uid}, nil
							case "owner":
								return agyCheckOwnedInfo{info, info.Mode(), f.uid + 1}, nil
							case "symlink":
								return agyCheckOwnedInfo{info, os.ModeSymlink | 0o700, f.uid}, nil
							case "file":
								return agyCheckOwnedInfo{info, 0o700, f.uid}, nil
							}
						}
					}
					return info, nil
				}
				got, err := agyCheckDecodeClaims(isolation, dir, stub, f)
				if name == "private" {
					if err != nil || len(got) != 0 {
						t.Fatalf("private state without calls refused: %v %v", got, err)
					}
				} else if err == nil || got != nil {
					t.Fatalf("unsafe state directory accepted: stub=%v records=%v err=%v", stub, got, err)
				}
			}
		})
	}
}

func TestAgyCheckClaimsMetadataGuard(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"symlink", "extra-dir", "stat-error", "owner", "mode", "special-bits", "calls-file", "calls-owner", "calls-mode"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, r, f := agyCheckTestRow(t)
			calls := filepath.Join(r.State, "calls")
			leaf := filepath.Join(calls, "000001")
			agyCheckTestWrite(t, leaf, []byte("allow tag\n"), 0o600)
			lstat, read, entries := f.lstat, f.read, f.entries
			list, err := entries(calls)
			if err != nil {
				t.Fatal(err)
			}
			// Reads and listings succeed even for injected symlinks/directories.
			f.entries = func(string) ([]os.DirEntry, error) { return list, nil }
			f.read = func(p string, n int64) ([]byte, error) {
				if p == leaf {
					return []byte("allow tag\n"), nil
				}
				return read(p, n)
			}
			target := leaf
			if strings.HasPrefix(name, "calls-") {
				target = calls
			}
			f.lstat = func(p string) (os.FileInfo, error) {
				info, err := lstat(p)
				if err != nil {
					return info, err
				}
				if p == target {
					// All path checks see the unaffected view, including readInside.
					if agyCheckPrivateStat() {
						mode, uid := info.Mode(), f.uid
						switch name {
						case "symlink":
							mode = os.ModeSymlink | 0o600
						case "extra-dir":
							mode = os.ModeDir | 0o600
						case "stat-error":
							return nil, os.ErrPermission
						case "owner", "calls-owner":
							uid++
						case "mode", "calls-mode":
							mode |= 0o040
						case "special-bits":
							mode |= os.ModeSetuid
						case "calls-file":
							mode = 0o700
						}
						return agyCheckOwnedInfo{info, mode, uid}, nil
					}
				}
				return info, nil
			}
			if got, err := agyCheckReadClaims(m.Isolation, r.State, f); err == nil || got != nil {
				t.Fatalf("metadata guard accepted unsafe records: %v %v", got, err)
			}
		})
	}
}

func TestAgyCheckPlaceAccountOverlap(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"equal", "ancestor", "descendant", "separate", "unset"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, r, f := agyCheckTestRow(t)
			m.AccountHome = map[string]string{"equal": m.Home, "ancestor": filepath.Dir(m.Home), "descendant": filepath.Join(m.Home, "account"), "separate": agyCheckRealTemp(t)}[name]
			writes := 0
			open := f.open
			f.open = func(p string, flags int, mode os.FileMode) (*os.File, error) {
				writes++
				return open(p, flags, mode)
			}
			called := false
			err := agyCheckPlace(m, r, agyCheckConfigFiles{}, nil, f, func() error { called = true; return nil })
			if name == "separate" || name == "unset" {
				if err != nil || !called {
					t.Fatalf("non-overlapping placement refused: %v", err)
				}
			} else if err == nil || called || writes != 0 {
				t.Fatalf("account overlap reached placement: writes=%d called=%v err=%v", writes, called, err)
			}
		})
	}
}

func TestAgyCheckPlaceAbsentParent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"unchanged", "missing", "file", "stat-error", "snapshot-error"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, r, f := agyCheckTestRow(t)
			parent := filepath.Join(m.Home, ".gemini/config")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			lstat := f.lstat
			restoring := false
			visits := 0
			f.lstat = func(p string) (os.FileInfo, error) {
				info, err := lstat(p)
				if err != nil {
					return info, err
				}
				if p == parent {
					visits++
					if name == "snapshot-error" && visits == 2 {
						return nil, os.ErrPermission
					}
					// Keep path() unaffected even if the restoration check is removed.
					if restoring && !agyCheckPathStat() {
						switch name {
						case "missing":
							return nil, os.ErrNotExist
						case "stat-error":
							return nil, os.ErrPermission
						case "file":
							return agyCheckOwnedInfo{info, 0o700, f.uid}, nil
						}
					}
				}
				return info, err
			}
			err := agyCheckPlace(m, r, agyCheckConfigFiles{}, nil, f, func() error { restoring = true; return nil })
			if name == "unchanged" {
				if err != nil || !restoring {
					t.Fatalf("unchanged absent parent refused: %v", err)
				}
			} else if name == "snapshot-error" {
				if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("parent snapshot error lost: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "restoring agy-check configuration failed") {
				t.Fatalf("absent parent restoration failure lost: %v", err)
			}
		})
	}
}
