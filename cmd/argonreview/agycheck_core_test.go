package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func agyCheckRealTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
func agyCheckTestWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
func agyCheckTestRow(t *testing.T) (agyCheckMarker, agyCheckRowDirs, agyCheckFiles) {
	t.Helper()
	home := agyCheckRealTemp(t)
	isolation := filepath.Join(home, "isolation")
	if err := os.Mkdir(isolation, 0o700); err != nil {
		t.Fatal(err)
	}
	f := agyCheckFilesDefaults()
	r, err := agyCheckNewRowDirs(isolation, f)
	if err != nil {
		t.Fatal(err)
	}
	return agyCheckMarker{Home: home, Isolation: isolation}, r, f
}

type agyCheckOwnedInfo struct {
	os.FileInfo
	mode os.FileMode
	uid  int
}

func (i agyCheckOwnedInfo) Mode() os.FileMode { return i.mode }
func (i agyCheckOwnedInfo) IsDir() bool       { return i.mode.IsDir() }
func (i agyCheckOwnedInfo) Sys() any          { return &syscall.Stat_t{Uid: uint32(i.uid)} }

func TestAgyCheckClaimsStrict(t *testing.T) {
	for _, name := range []string{"absent", "valid", "gap", "extra-file", "extra-dir", "symlink", "mode", "owner", "calls-mode", "calls-owner", "calls-file", "calls-symlink", "non-digit", "short", "word", "alternate", "tag", "trailing", "newline", "empty", "size", "count", "read-error", "list-error", "stat-error", "stub-word"} {
		t.Run(name, func(t *testing.T) {
			isolation := agyCheckRealTemp(t)
			dir := filepath.Join(isolation, "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			f := agyCheckFilesDefaults()
			calls := filepath.Join(dir, "calls")
			valid := name == "absent" || name == "valid"
			if name != "absent" {
				agyCheckTestWrite(t, filepath.Join(calls, "000002"), []byte("deny other\n"), 0o600)
				agyCheckTestWrite(t, filepath.Join(calls, "000001"), []byte("allow tag\n"), 0o600)
			}
			leaf := filepath.Join(calls, "000002")
			switch name {
			case "valid":
				entries := f.entries
				f.entries = func(p string) ([]os.DirEntry, error) {
					got, err := entries(p)
					for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
						got[i], got[j] = got[j], got[i]
					}
					return got, err
				}
			case "gap":
				if err := os.Rename(leaf, filepath.Join(calls, "000003")); err != nil {
					t.Fatal(err)
				}
			case "extra-file":
				agyCheckTestWrite(t, filepath.Join(calls, "extra"), []byte("allow tag\n"), 0o600)
			case "extra-dir":
				if err := os.Mkdir(filepath.Join(calls, "000003"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(calls, "000001"), leaf); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(leaf, 0o640); err != nil {
					t.Fatal(err)
				}
			case "owner", "calls-owner":
				original := f.lstat
				target := leaf
				if name == "calls-owner" {
					target = calls
				}
				f.lstat = func(p string) (os.FileInfo, error) {
					info, e := original(p)
					if e == nil && p == target {
						return agyCheckOwnedInfo{info, info.Mode(), f.uid + 1}, nil
					}
					return info, e
				}
			case "calls-mode":
				if err := os.Chmod(calls, 0o750); err != nil {
					t.Fatal(err)
				}
			case "calls-file", "calls-symlink":
				if err := os.RemoveAll(calls); err != nil {
					t.Fatal(err)
				}
				if name == "calls-file" {
					agyCheckTestWrite(t, calls, []byte("x"), 0o700)
				} else if err := os.Symlink(dir, calls); err != nil {
					t.Fatal(err)
				}
			case "non-digit", "short":
				n := "00000a"
				if name == "short" {
					n = "00002"
				}
				if err := os.Rename(leaf, filepath.Join(calls, n)); err != nil {
					t.Fatal(err)
				}
			case "word", "alternate", "tag", "trailing", "newline", "empty", "size", "stub-word":
				data := map[string]string{"word": "sensitive-content tag\n", "alternate": "alternate tag\n", "tag": "allow BAD\n", "trailing": "allow tag\ngarbage", "newline": "allow tag", "empty": "", "size": strings.Repeat("x", 65), "stub-word": "start -\n"}[name]
				if err := os.WriteFile(leaf, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			case "count":
				original := f.entries
				f.entries = func(p string) ([]os.DirEntry, error) {
					es, e := original(p)
					if e == nil {
						for len(es) <= agyCheckHookCallCap {
							es = append(es, es[0])
						}
					}
					return es, e
				}
			case "read-error":
				f.read = func(string, int64) ([]byte, error) { return nil, errors.New("sensitive-content") }
			case "list-error":
				f.entries = func(string) ([]os.DirEntry, error) { return nil, errors.New("sensitive-content") }
			case "stat-error":
				original := f.lstat
				f.lstat = func(p string) (os.FileInfo, error) {
					if p == leaf {
						return nil, errors.New("sensitive-content")
					}
					return original(p)
				}
			}
			got, err := agyCheckReadClaims(isolation, dir, f)
			if valid {
				if err != nil {
					t.Fatalf("valid records refused: %v", err)
				}
				if name == "absent" && len(got) != 0 {
					t.Fatal("absent calls must mean zero records")
				}
				if name == "valid" && (len(got) != 2 || got[0] != (agyCheckClaim{1, "allow", "tag"}) || got[1] != (agyCheckClaim{2, "deny", "other"})) {
					t.Fatalf("claims not ordered by number: %+v", got)
				}
			} else if err == nil || got != nil {
				t.Fatalf("malformed records accepted or partial evidence returned: %+v %v", got, err)
			}
			if err != nil && (strings.Contains(err.Error(), "sensitive-content") || agyCheckPrintable(err.Error()) != err.Error()) {
				t.Fatal("record diagnostics leaked content")
			}
		})
	}
}

func TestAgyCheckClaimRepliesStrict(t *testing.T) {
	got, err := agyCheckClaimReplies([]agyCheckClaim{{3, "allow", "tag"}, {1, "allow", "tag"}, {2, "deny", "other"}}, "tag")
	if err != nil || strings.Join(got, ",") != "allow,allow" {
		t.Fatalf("decision order/tag lost: %v %v", got, err)
	}
	for _, word := range []string{"silent", "exit2", "start", "invalid"} {
		if got, err := agyCheckClaimReplies([]agyCheckClaim{{1, word, "other"}}, "tag"); err == nil || got != nil {
			t.Fatalf("non-decision word accepted: %s", word)
		}
	}
}

func TestAgyCheckConfinement(t *testing.T) {
	for _, name := range []string{"outside", "symlink", "dotdot", "case", "state-outside", "stub-outside", "sentinel", "layout"} {
		t.Run(name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			path := filepath.Join(m.Isolation, "new")
			switch name {
			case "outside":
				path = filepath.Join(m.Home, "outside")
			case "symlink":
				link := filepath.Join(m.Home, "alias")
				if err := os.Symlink(m.Isolation, link); err != nil {
					t.Fatal(err)
				}
				m.Isolation = link
				path = filepath.Join(link, "new")
			case "dotdot":
				path = m.Isolation + "/x/../new"
			case "case":
				path = strings.ToUpper(m.Isolation) + "/new"
			case "state-outside":
				r.State = filepath.Join(m.Home, "outside")
			case "stub-outside":
				r.Stub = filepath.Join(m.Home, "outside")
			case "sentinel":
				if err := os.Remove(filepath.Join(r.State, agyCheckHookSentinel)); err != nil {
					t.Fatal(err)
				}
			case "layout":
				r.Outside = r.Workspace
			}
			var err error
			if strings.Contains(name, "outside") && name != "outside" || name == "sentinel" || name == "layout" {
				err = r.validate(m.Isolation, f)
			} else {
				err = f.path(m.Isolation, path)
			}
			if err == nil {
				t.Fatal("unsafe confined path accepted")
			}
		})
	}
	m, r, f := agyCheckTestRow(t)
	another, err := agyCheckNewRowDirs(m.Isolation, f)
	if err != nil || another.Run == r.Run {
		t.Fatal("row directory reused")
	}
	for _, p := range []string{r.Run, r.Row, r.State, r.Stub, r.Workspace, r.Outside, r.Tmp} {
		if err := f.private(p, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.cleanup(m.Isolation, r.Run); err != nil {
		t.Fatal(err)
	}
}

func TestAgyCheckPlace(t *testing.T) {
	for _, name := range []string{"absent", "present", "error", "panic", "symlink", "oversize", "fixture-invalid", "fixture-existing", "fixture-symlink", "restore-failure", "verify-failure", "write-failure"} {
		t.Run(name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			path := filepath.Join(m.Home, ".gemini/config/hooks.json")
			original := []byte{0, 1, 2, '\n'}
			present := name != "absent"
			if present {
				agyCheckTestWrite(t, path, original, 0o640)
			}
			fixture := map[string][]byte{".agent/hooks.json": []byte("fixture")}
			called := false
			restoring := false
			switch name {
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(r.Row, "target"), path); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, (1<<20)+1), 0o640); err != nil {
					t.Fatal(err)
				}
			case "fixture-invalid":
				fixture = map[string][]byte{"../escape": nil}
			case "fixture-existing":
				agyCheckTestWrite(t, filepath.Join(r.Workspace, ".agent/hooks.json"), []byte("untouched"), 0o600)
			case "fixture-symlink":
				if err := os.Symlink(r.Outside, filepath.Join(r.Workspace, ".agent")); err != nil {
					t.Fatal(err)
				}
			case "write-failure":
				open := f.open
				f.open = func(p string, flags int, mode os.FileMode) (*os.File, error) {
					if strings.HasSuffix(p, "mcp_config.json") {
						return nil, os.ErrPermission
					}
					return open(p, flags, mode)
				}
			case "verify-failure":
				read := f.read
				f.read = func(p string, n int64) ([]byte, error) {
					if restoring && p == path {
						return []byte("wrong restored bytes"), nil
					}
					return read(p, n)
				}
			}
			err := agyCheckPlace(m, r, agyCheckConfigFiles{[]byte("hooks"), []byte("settings"), []byte("mcp")}, fixture, f, func() error {
				called = true
				got, e := os.ReadFile(path)
				info, se := os.Lstat(path)
				if e != nil || se != nil || string(got) != "hooks" || info.Mode() != 0o600 {
					t.Fatal("configuration not placed privately during run")
				}
				if data, e := os.ReadFile(filepath.Join(r.Workspace, ".agent/hooks.json")); e != nil || string(data) != "fixture" {
					t.Fatal("workspace fixture not placed")
				}
				switch name {
				case "error":
					return errors.New("injected run failure")
				case "panic":
					panic("injected panic")
				case "restore-failure":
					// The deps value is copied; inject an actual changed target type.
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "verify-failure":
					restoring = true
				}
				return nil
			})
			wantErr := name != "absent" && name != "present"
			if (err != nil) != wantErr {
				t.Fatalf("placement failure lost: %v", err)
			}
			early := name == "symlink" || name == "oversize" || strings.HasPrefix(name, "fixture-") || name == "write-failure"
			if called == early {
				t.Fatal("unsafe placement reached run or valid placement skipped it")
			}
			if early && name != "write-failure" {
				return
			}
			if name == "restore-failure" {
				if !strings.Contains(err.Error(), "restoring") {
					t.Fatal("restoration failure not surfaced")
				}
				return
			}
			if present {
				got, e := os.ReadFile(path)
				info, se := os.Lstat(path)
				if e != nil || se != nil || !bytes.Equal(got, original) || info.Mode() != 0o640 {
					t.Fatalf("home not restored byte-identically: %q %v %v", got, e, se)
				}
			} else if _, e := os.Lstat(filepath.Join(m.Home, ".gemini")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("absent home tree not restored")
			}
			if _, e := os.Lstat(filepath.Join(r.Workspace, ".agent")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("workspace fixture not removed")
			}
		})
	}
}

func TestAgyCheckBuiltinListing(t *testing.T) {
	for _, name := range []string{"valid", "symlink", "device", "oversize", "count", "duplicate", "extra", "ancestor"} {
		t.Run(name, func(t *testing.T) {
			home := agyCheckRealTemp(t)
			f := agyCheckFilesDefaults()
			root := filepath.Join(home, ".gemini/antigravity-cli/builtin")
			agyCheckTestWrite(t, filepath.Join(root, "z"), []byte("z"), 0o600)
			agyCheckTestWrite(t, filepath.Join(root, "a/b"), []byte("a"), 0o600)
			baseline, err := agyCheckBuiltinListing(home, f)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "z"), filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			case "device":
				lstat := f.lstat
				f.lstat = func(p string) (os.FileInfo, error) {
					info, e := lstat(p)
					if p == filepath.Join(root, "z") && e == nil {
						return agyCheckOwnedInfo{info, os.ModeDevice | 0o600, f.uid}, nil
					}
					return info, e
				}
			case "oversize":
				agyCheckTestWrite(t, filepath.Join(root, "z"), bytes.Repeat([]byte{'x'}, (1<<20)+1), 0o600)
			case "count":
				entries := f.entries
				f.entries = func(p string) ([]os.DirEntry, error) {
					es, e := entries(p)
					if p == root && e == nil {
						for len(es) <= agyCheckHookCallCap {
							es = append(es, es[0])
						}
					}
					return es, e
				}
			case "duplicate":
				entries := f.entries
				f.entries = func(p string) ([]os.DirEntry, error) {
					es, e := entries(p)
					if p == root && e == nil {
						es = append(es, es[1])
					}
					return es, e
				}
			case "extra":
				agyCheckTestWrite(t, filepath.Join(root, "extra"), []byte("extra"), 0o600)
			case "ancestor":
				if err := os.Rename(filepath.Join(home, ".gemini"), filepath.Join(home, "real")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, "real"), filepath.Join(home, ".gemini")); err != nil {
					t.Fatal(err)
				}
			}
			got, err := agyCheckBuiltinListing(home, f)
			if name != "valid" && name != "extra" {
				if err == nil || got != nil {
					t.Fatal("unsafe builtin tree accepted")
				}
				return
			}
			parsed, e := implementBuiltinList(got)
			if err != nil || e != nil || !strings.Contains(agyCheckBuiltinInfo(got), "information only") {
				t.Fatalf("listing round-trip failed: %q %v %v", got, err, e)
			}
			if name == "extra" {
				old, _ := implementBuiltinList(baseline)
				if len(parsed) != len(old)+1 || parsed["extra"] == "" {
					t.Fatal("extra file hidden from implement-check comparison")
				}
				return
			}
			if len(parsed) != 2 || !strings.HasSuffix(string(got), "\n") || string(got) != string(baseline) {
				t.Fatal("listing bytes/order drift")
			}
		})
	}
}

// Keep writer failures distinguishable from a normal successful stub response.
type agyCheckFailWriter struct{}

func (agyCheckFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAgyCheckCleanupFailures(t *testing.T) {
	for _, name := range []string{"remove", "leftover", "symlink"} {
		t.Run(name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			switch name {
			case "remove":
				f.remove = func(string) error { return os.ErrPermission }
			case "leftover":
				f.remove = func(string) error { return nil }
			case "symlink":
				if err := os.Symlink(m.Home, filepath.Join(r.Row, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.cleanup(m.Isolation, r.Run); err == nil {
				t.Fatal("cleanup failure not surfaced")
			}
		})
	}
}

func TestAgyCheckCleanupEntryLimits(t *testing.T) {
	for _, tc := range []struct {
		name, dir string
		entries   int
		refuse    bool
	}{
		{"calls at cap", "calls", agyCheckHookCallCap, false},
		{"events above call cap", "events", agyCheckHookCallCap + 1, false},
		{"events at doubled cap", "events", 2 * agyCheckHookCallCap, false},
		{"events above doubled cap", "events", 2*agyCheckHookCallCap + 1, true},
		{"calls above cap", "calls", agyCheckHookCallCap + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			dir := filepath.Join(agyCheckHookTestDir, tc.dir)
			entries := make([]os.DirEntry, tc.entries)
			for i := range entries {
				path := filepath.Join(dir, fmt.Sprintf("%06d", i+1))
				info := agyCheckInfo{0o600, 501}
				h.info[path] = info
				entries[i] = fs.FileInfoToDirEntry(agyCheckNamedInfo{FileInfo: info, name: filepath.Base(path)})
			}
			removed := 0
			f := agyCheckFiles{lstat: h.d.lstat,
				entries: func(path string) ([]os.DirEntry, error) {
					if path != dir {
						t.Fatalf("unexpected directory read: %s", path)
					}
					return entries, nil
				},
				remove: func(path string) error { delete(h.info, path); removed++; return nil },
			}
			err := f.cleanup(agyCheckHookTestDir, dir)
			if tc.refuse {
				if err == nil || err.Error() != "cleanup entry limit exceeded" || removed != 0 {
					t.Fatalf("over-cap cleanup: err=%v removed=%d", err, removed)
				}
			} else if err != nil || removed != tc.entries+1 {
				t.Fatalf("within-cap cleanup: err=%v removed=%d want=%d", err, removed, tc.entries+1)
			}
		})
	}
}

type agyCheckNamedInfo struct {
	os.FileInfo
	name string
}

func (i agyCheckNamedInfo) Name() string { return i.name }

func TestAgyCheckStubDecoder(t *testing.T) {
	isolation := agyCheckRealTemp(t)
	dir := filepath.Join(isolation, "state")
	f := agyCheckFilesDefaults()
	for i, record := range []string{"start -\n", "request verify\n", "response verify\n"} {
		agyCheckTestWrite(t, filepath.Join(dir, "calls", fmt.Sprintf("%06d", i+1)), []byte(record), 0o600)
	}
	got, err := agyCheckReadStubRecords(isolation, dir, f)
	if err != nil || len(got) != 3 || got[2] != (agyCheckStubRecord{3, "response", "verify"}) {
		t.Fatalf("stub records lost: %+v %v", got, err)
	}
	agyCheckTestWrite(t, filepath.Join(dir, "calls/000003"), []byte("allow -\n"), 0o600)
	if got, err := agyCheckReadStubRecords(isolation, dir, f); err == nil || got != nil {
		t.Fatal("hook word accepted as stub record")
	}
}

func TestAgyCheckEvidenceOutside(t *testing.T) {
	isolation := agyCheckRealTemp(t)
	outside := agyCheckRealTemp(t)
	f := agyCheckFilesDefaults()
	agyCheckTestWrite(t, filepath.Join(outside, "calls/000001"), []byte("allow tag\n"), 0o600)
	if got, err := agyCheckReadClaims(isolation, outside, f); err == nil || got != nil {
		t.Fatal("outside claim state accepted")
	}
	agyCheckTestWrite(t, filepath.Join(outside, "calls/000001"), []byte("start -\n"), 0o600)
	if got, err := agyCheckReadStubRecords(isolation, outside, f); err == nil || got != nil {
		t.Fatal("outside stub state accepted")
	}
}

func TestAgyCheckBuiltinComparison(t *testing.T) {
	home := agyCheckRealTemp(t)
	f := agyCheckFilesDefaults()
	root := filepath.Join(home, ".gemini/antigravity-cli/builtin")
	agyCheckTestWrite(t, filepath.Join(root, "nested/tool"), []byte("whole file\n"), 0o600)
	listing, err := agyCheckBuiltinListing(home, f)
	if err != nil {
		t.Fatal(err)
	}
	agyCheckTestWrite(t, filepath.Join(home, "implementer/builtin.agy-1.4.7.txt"), listing, 0o600)
	d := implementCheckDefaults()
	d.version = func(string) ([]byte, error) { return []byte("1.4.7\n"), nil }
	var out bytes.Buffer
	c := implementChecker{deps: d, out: &out}
	c.builtin(home, "fake-path-not-executed")
	if c.problems != 0 {
		t.Fatalf("producer differs from implement-check: %s", out.String())
	}
	agyCheckTestWrite(t, filepath.Join(root, "extra"), []byte("extra"), 0o600)
	c.builtin(home, "fake-path-not-executed")
	if c.problems != 1 || !strings.Contains(out.String(), "extra builtin/ entry") {
		t.Fatal("implement-check did not refuse the extra file")
	}
}

func TestAgyCheckBuiltinOrder(t *testing.T) {
	home := agyCheckRealTemp(t)
	f := agyCheckFilesDefaults()
	root := filepath.Join(home, ".gemini/antigravity-cli/builtin")
	agyCheckTestWrite(t, filepath.Join(root, "z"), []byte("z"), 0o600)
	agyCheckTestWrite(t, filepath.Join(root, "a"), []byte("a"), 0o600)
	listing, err := agyCheckBuiltinListing(home, f)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(listing)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "./a") || !strings.HasSuffix(lines[1], "./z") {
		t.Fatal("builtin paths not sorted")
	}
}

func TestAgyCheckPlaceAllTargets(t *testing.T) {
	for _, location := range []string{".gemini/config/hooks.json", ".gemini/config/mcp_config.json", ".gemini/antigravity-cli/settings.json"} {
		t.Run(location, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			path := filepath.Join(m.Home, location)
			original := []byte{0, 255, 1, '\n'}
			agyCheckTestWrite(t, path, original, 0o640)
			err := agyCheckPlace(m, r, agyCheckConfigFiles{[]byte("hooks"), []byte("settings"), []byte("mcp")}, nil, f, func() error { return nil })
			got, e := os.ReadFile(path)
			info, se := os.Lstat(path)
			if err != nil || e != nil || se != nil || !bytes.Equal(got, original) || info.Mode() != 0o640 {
				t.Fatal("configuration target not restored exactly")
			}
		})
	}
}

func TestAgyCheckPlaceModeVerification(t *testing.T) {
	m, r, f := agyCheckTestRow(t)
	path := filepath.Join(m.Home, ".gemini/config/hooks.json")
	agyCheckTestWrite(t, path, []byte("original"), 0o640)
	restoring := false
	lstat := f.lstat
	f.lstat = func(p string) (os.FileInfo, error) {
		info, e := lstat(p)
		if e == nil && p == path && restoring {
			return agyCheckOwnedInfo{info, 0o600, f.uid}, nil
		}
		return info, e
	}
	err := agyCheckPlace(m, r, agyCheckConfigFiles{}, nil, f, func() error { restoring = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "restoration verification failed") {
		t.Fatal("restoration mode verification skipped")
	}
}
