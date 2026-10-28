package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// browserRecordFixture models the read-only harness directory and restores it
// before TempDir cleanup, even when a refusal assertion fails.
func browserRecordFixture(t *testing.T) (browserSecurity, string, browserSecurityEntry) {
	t.Helper()
	b, _, entry := browserGateFixture(t)
	b.beforePublish = func() {
		fd, err := unix.Open(filepath.Join(filepath.Dir(b.path), browserGateLock), unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Error(err)
			return
		}
		defer closeFD(fd)
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			t.Errorf("writer lock not held through publication: %v", err)
		}
	}
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
	lockPath := filepath.Join(filepath.Dir(b.path), browserGateLock)
	if fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0); err == nil {
		defer closeFD(fd)
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatalf("record left lock held: %v", err)
		}
	}
	return log.String()
}

func TestBrowserCheckRecordConcurrent(t *testing.T) {
	b, _, entry := browserRecordFixture(t)
	browserWrite(t, b.path, string(entry.bytes()), 0o600)
	if b.admit(entry.switchPath) != "" {
		t.Fatal("fresh admission refused")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(b.path), browserGateLock)); !os.IsNotExist(err) {
		t.Fatalf("admission created writer lock: %v", err)
	}
	read, release := make(chan struct{}), make(chan struct{})
	a := b
	a.afterRead = func() { close(read); <-release }
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- a.record(false, "", entry.completed.Add(time.Minute), entry.record) }()
	select {
	case <-read:
	case <-time.After(3 * time.Second):
		t.Fatal("first refresh did not read entry")
	}
	waiting := make(chan struct{}, 1)
	b.sleep = func(d time.Duration) {
		select {
		case waiting <- struct{}{}:
		default:
		}
		time.Sleep(d)
	}
	go func() { second <- b.record(false, "", entry.completed.Add(2*time.Minute), entry.record) }()
	select {
	case <-waiting:
	case err := <-second:
		close(release)
		<-first
		t.Fatalf("second refresh completed while first held lock: %v", err)
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("second refresh did not attempt lock")
	}
	select {
	case err := <-second:
		close(release)
		<-first
		t.Fatalf("second refresh completed while first held lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	entry.completed = entry.completed.Add(2 * time.Minute)
	data, err := os.ReadFile(b.path)
	if err != nil || !bytes.Equal(data, entry.bytes()) {
		t.Fatalf("completion regressed: %q, %v", data, err)
	}
	browserRecordCall(t, b, browserRecordArgs(entry, false), 0)
}

func TestBrowserCheckRecordLock(t *testing.T) {
	for _, kind := range []string{"symlink", "mode", "two-links", "directory", "timeout", "init"} {
		t.Run(kind, func(t *testing.T) {
			b, _, entry := browserRecordFixture(t)
			if kind != "init" {
				browserWrite(t, b.path, string(entry.bytes()), 0o600)
			}
			path := filepath.Join(filepath.Dir(b.path), browserGateLock)
			browserWrite(t, path, "", 0o600)
			switch kind {
			case "symlink", "two-links":
				if err := os.Link(path, path+"-real"); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					browserRemove(t, path)
					if err := os.Symlink(path+"-real", path); err != nil {
						t.Fatal(err)
					}
				}
			case "mode":
				browserWrite(t, path, "", 0o644)
			case "directory":
				browserRemove(t, path)
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			want := "browser-check-record: security check lock unavailable or unsafe\n"
			if kind == "timeout" || kind == "init" {
				parent, err := b.parent()
				if err != nil {
					t.Fatal(err)
				}
				defer closeFD(parent)
				fd, err := b.lockRecord(parent)
				if err != nil {
					t.Fatal(err)
				}
				now, slept := b.now(), time.Duration(0)
				b.now = func() time.Time { return now }
				b.sleep = func(d time.Duration) { now = now.Add(d); slept += d }
				// Admission must neither wait on nor create the writer lock.
				_ = b.admit(entry.switchPath)
				var out, log bytes.Buffer
				code := browserRecordCommand(browserRecordArgs(entry, kind == "init"), &out, &log, b)
				closeFD(fd)
				if code != 1 || log.String() != "browser-check-record: security check is being updated\n" || slept != b.lockLimit || out.Len() != 0 {
					t.Fatalf("lock timeout: code=%d log=%q slept=%v", code, &log, slept)
				}
			} else if log := browserRecordCall(t, b, browserRecordArgs(entry, false), 1); log != want {
				t.Fatalf("unsafe lock refusal=%q", log)
			}
			data, _ := os.ReadFile(b.path)
			if kind != "init" && !bytes.Equal(data, entry.bytes()) || kind == "init" && data != nil {
				t.Fatal("lock refusal changed gate bytes")
			}
			browserRemove(t, path)
			browserRecordCall(t, b, browserRecordArgs(entry, kind == "init"), 0)
		})
	}
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

func TestBrowserCheckRecordClockAfterLock(t *testing.T) {
	for _, kind := range []string{"expiry-on", "expiry-off", "future", "init"} {
		t.Run(kind, func(t *testing.T) {
			b, path, entry := browserRecordFixture(t)
			early := b.now()
			late := early.Add(2 * time.Minute)
			before, after := late, early
			next := entry
			next.completed = late.Add(-time.Second)
			want := 1
			message := "browser-check-record: completion is in the future\n"
			if strings.HasPrefix(kind, "expiry-") {
				entry.completed = early
				early = entry.completed.Add(browserSecurityAge - time.Minute)
				late = entry.completed.Add(browserSecurityAge + time.Minute)
				before, after = early, late
				next.completed = early.Add(-time.Second)
				message = "browser-check-record: expired check cannot be refreshed while browser-mode is on\n"
				if kind == "expiry-on" {
					browserWrite(t, path, "on", 0o400)
				} else {
					want, message = 0, ""
				}
			}
			if kind != "init" {
				browserWrite(t, b.path, string(entry.bytes()), 0o600)
			}
			locked, clockAfterLock := false, false
			b.afterLock = func() { locked = true }
			// Lock retries also read the clock, so phase, not call count, sets time.
			b.now = func() time.Time {
				if locked {
					clockAfterLock = true
					return after
				}
				return before
			}
			log := browserRecordCall(t, b, browserRecordArgs(next, kind == "init"), want)
			if log != message {
				t.Fatalf("clock refusal=%q want=%q", log, message)
			}
			if !clockAfterLock {
				t.Fatal("record did not sample the decision clock after locking")
			}
			if kind == "init" {
				if _, err := os.Lstat(b.path); !os.IsNotExist(err) {
					t.Fatalf("future init created gate: %v", err)
				}
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
