package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// browserRecordFixture models the read-only harness directory and restores it
// before TempDir cleanup, even when a refusal assertion fails.
func browserRecordFixture(t *testing.T) (browserSecurity, string, browserSecurityEntry) {
	t.Helper()
	b, _, entry := browserGateFixture(t)
	dir := filepath.Join(filepath.Dir(b.path), "implementer")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "browser-mode")
	browserWrite(t, path, "off", 0o400)
	browserWrite(t, filepath.Join(dir, "verify-mode"), "on", 0o400)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	entry.switchPath = path
	return b, path, entry
}

// browserRecordArgs separates the one-time path from all routine calls.
func browserRecordArgs(entry browserSecurityEntry, init bool) []string {
	args := []string{"--completed-at", entry.completed.Format(time.RFC3339Nano), "--record", entry.record}
	if init {
		args = append(args, "--init", "--browser-mode", entry.switchPath)
	}
	return args
}

// browserRecordCall observes fixed diagnostics, untouched state on refusals,
// successful mode and absence of temporary entries after every command.
func browserRecordCall(t *testing.T, b browserSecurity, args []string, want int) string {
	t.Helper()
	before, readErr := os.ReadFile(b.path)
	beforeInfo, _ := os.Lstat(b.path)
	var out, log bytes.Buffer
	code := browserRecordCommand(args, &out, &log, b)
	if code != want {
		t.Fatalf("record exit=%d want=%d stdout=%q stderr=%q", code, want, &out, &log)
	}
	if want != 0 {
		after, err := os.ReadFile(b.path)
		afterInfo, _ := os.Lstat(b.path)
		if !bytes.Equal(before, after) || (readErr == nil) != (err == nil) {
			t.Fatal("refused record changed gate bytes")
		}
		if beforeInfo != nil && (afterInfo == nil || !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Mode() != afterInfo.Mode()) {
			t.Fatal("refused record replaced gate entry")
		}
		if log.Len() == 0 || out.Len() != 0 {
			t.Fatalf("refusal streams stdout=%q stderr=%q", &out, &log)
		}
	} else {
		info, err := os.Lstat(b.path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("published file: %v %v", info, err)
		}
		if log.Len() != 0 || out.String() != "browser-check-record: recorded\n" {
			t.Fatalf("success streams stdout=%q stderr=%q", &out, &log)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(b.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".browser-security-check-") {
			t.Fatal("staging file left behind")
		}
	}
	return log.String()
}

// TestBrowserCheckRecord proves initial provisioning and each refresh refusal
// independently; every refusal preserves the previous file byte for byte.
func TestBrowserCheckRecord(t *testing.T) {
	t.Run("init-once", func(t *testing.T) {
		b, _, entry := browserRecordFixture(t)
		browserRecordCall(t, b, browserRecordArgs(entry, true), 0)
		data, err := os.ReadFile(b.path)
		if err != nil || !bytes.Equal(data, entry.bytes()) {
			t.Fatalf("init bytes=%q err=%v", data, err)
		}
		entry.completed = entry.completed.Add(time.Minute)
		browserRecordCall(t, b, browserRecordArgs(entry, true), 1)
	})
	for _, kind := range []string{"existing-symlink", "switch-symlink", "switch-directory", "switch-owner", "directory-mode", "directory-symlink", "missing-verify", "verify-symlink", "verify-directory", "future", "future-invalid", "bad-URL", "missing-path", "path-without-init", "repeat-init", "false-init", "repeat-path", "missing-completion", "missing-record", "relative-path"} {
		t.Run("init/"+kind, func(t *testing.T) {
			b, path, entry := browserRecordFixture(t)
			dir := filepath.Dir(path)
			chmod := func(mode os.FileMode) {
				t.Helper()
				if err := os.Chmod(dir, mode); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "existing-symlink":
				browserWrite(t, b.path+"-real", "preserve\n", 0o600)
				if err := os.Symlink(b.path+"-real", b.path); err != nil {
					t.Fatal(err)
				}
			case "switch-symlink":
				chmod(0o700)
				browserWrite(t, path+"-real", "off", 0o400)
				browserRemove(t, path)
				if err := os.Symlink(path+"-real", path); err != nil {
					t.Fatal(err)
				}
				chmod(0o500)
			case "switch-directory":
				chmod(0o700)
				browserRemove(t, path)
				if err := os.Mkdir(path, 0o400); err != nil {
					t.Fatal(err)
				}
				chmod(0o500)
			case "switch-owner":
				if err := browserInitSwitch(path, b.uid+1); err == nil {
					t.Fatal("init accepted wrong switch owner")
				}
				return
			case "directory-mode":
				chmod(0o700)
			case "directory-symlink":
				link := filepath.Join(filepath.Dir(b.path), "alias")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				entry.switchPath = filepath.Join(link, "browser-mode")
			case "missing-verify":
				chmod(0o700)
				browserRemove(t, filepath.Join(dir, "verify-mode"))
				chmod(0o500)
			case "verify-symlink":
				chmod(0o700)
				browserRemove(t, filepath.Join(dir, "verify-mode"))
				if err := os.Symlink(path, filepath.Join(dir, "verify-mode")); err != nil {
					t.Fatal(err)
				}
				chmod(0o500)
			case "verify-directory":
				chmod(0o700)
				browserRemove(t, filepath.Join(dir, "verify-mode"))
				if err := os.Mkdir(filepath.Join(dir, "verify-mode"), 0o700); err != nil {
					t.Fatal(err)
				}
				chmod(0o500)
			case "future":
				entry.completed = b.now().Add(time.Nanosecond)
			}
			args := browserRecordArgs(entry, true)
			want := 1
			switch kind {
			case "future-invalid":
				args = featureFlag(args, "completed-at", "2026-10-28T12:00:00+00:00")
				want = 2
			case "bad-URL":
				args = featureFlag(args, "record", "https://example.invalid/private-value")
				want = 2
			case "missing-path":
				args = args[:len(args)-2]
				want = 2
			case "path-without-init":
				args = append(browserRecordArgs(entry, false), "--browser-mode", path)
				want = 2
			case "repeat-init":
				args = append(args, "--init")
				want = 2
			case "false-init":
				args = append(browserRecordArgs(entry, false), "--init=false")
				want = 2
			case "repeat-path":
				args = append(args, "--browser-mode", path)
				want = 2
			case "missing-completion":
				args = args[2:]
				want = 2
			case "missing-record":
				args = append(args[:2], args[4:]...)
				want = 2
			case "relative-path":
				args[len(args)-1] = "relative"
				want = 2
			}
			log := browserRecordCall(t, b, args, want)
			if strings.Contains(log, "private-value") {
				t.Fatal("record diagnostic echoed input")
			}
		})
	}
	for _, kind := range []string{"fresh", "equal", "expired-off", "expired-malformed", "expired-missing-switch", "expired-on", "future", "older", "missing", "unsafe", "malformed", "path-argument", "positional-path", "init-without-path"} {
		t.Run("refresh/"+kind, func(t *testing.T) {
			b, path, entry := browserRecordFixture(t)
			if strings.HasPrefix(kind, "expired") {
				entry.completed = b.now().Add(-browserSecurityAge)
			}
			browserWrite(t, b.path, string(entry.bytes()), 0o600)
			next := entry
			next.completed = b.now()
			want := 1
			switch kind {
			case "fresh", "expired-off", "expired-malformed", "expired-missing-switch":
				want = 0
			case "equal":
				next.completed = entry.completed
				want = 0
			case "future":
				next.completed = b.now().Add(time.Nanosecond)
			case "older":
				next.completed = entry.completed.Add(-time.Nanosecond)
			case "missing":
				browserRemove(t, b.path)
			case "unsafe":
				if err := os.Chmod(b.path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				browserWrite(t, b.path, "private-value", 0o600)
			}
			if kind == "expired-on" {
				browserWrite(t, path, "on", 0o400)
			}
			if kind == "expired-malformed" {
				browserWrite(t, path, "ON", 0o400)
			}
			if kind == "expired-missing-switch" {
				if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				browserRemove(t, path)
				if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
					t.Fatal(err)
				}
			}
			args := browserRecordArgs(next, false)
			switch kind {
			case "path-argument":
				args = append(args, "--browser-mode", path)
				want = 2
			case "positional-path":
				args = append(args, path)
				want = 2
			case "init-without-path":
				args = append(args, "--init")
				want = 2
			}
			log := browserRecordCall(t, b, args, want)
			if kind == "missing" && !strings.Contains(log, "--init") {
				t.Fatal("missing entry did not request --init")
			}
			if strings.Contains(log, "private-value") {
				t.Fatal("record diagnostic echoed contents")
			}
			if want == 0 {
				data, err := os.ReadFile(b.path)
				if err != nil || !bytes.Equal(data, next.bytes()) {
					t.Fatalf("refresh bytes=%q err=%v", data, err)
				}
			}
		})
	}
}
