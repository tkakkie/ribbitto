package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestVerifyPrepared(t *testing.T) {
	for _, tc := range []struct {
		name, path, change string
	}{
		{"prepared", "", ""},
		{"extra agent", "config/agents/extra.md", "write"},
		{"agent bytes", "config/agents/argon-reviewer.md", "write"},
		{"hooks bytes", "config/hooks.json", "write"},
		{"settings bytes", "antigravity-cli/settings.json", "write"},
		{"mcp bytes", "config/mcp_config.json", "write"},
		{"agents symlink", "config/agents", "symlink"},
		{"agent symlink", "config/agents/argon-reviewer.md", "symlink"},
		{"missing directory", "config/agents", "remove"},
		{"missing file", "config/hooks.json", "remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := resolved(t, t.TempDir())
			if _, err := prepare(home, account, guardBinary); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".gemini", tc.path)
			switch tc.change {
			case "write":
				write(t, path, "changed")
			case "symlink":
				moved := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				symlink(t, moved, path)
			case "remove":
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, home)
			if err := verifyPrepared(home, account, guardBinary); (err != nil) != (tc.change != "") {
				t.Fatalf("verifyPrepared = %v", err)
			}
			if !reflect.DeepEqual(before, snapshot(t, home)) {
				t.Fatal("verification changed HOME")
			}
			unchanged()
		})
	}
}

func TestVerifyPreparedLock(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%t", timeout), func(t *testing.T) {
			account, unchanged := fixture(t)
			home := resolved(t, t.TempDir())
			if _, err := prepare(home, account, guardBinary); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, prepareLockName)
			fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer closeFD(fd)
			if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			wait := time.Second
			if timeout {
				wait = 40 * time.Millisecond
			}
			done := make(chan error, 1)
			go func() {
				time.Sleep(80 * time.Millisecond)
				done <- unix.Flock(fd, unix.LOCK_UN)
			}()
			before, start := snapshot(t, home), time.Now()
			err = verifyPreparedWith(home, account, guardBinary, hooks{lockWait: wait, lockPoll: time.Millisecond})
			elapsed := time.Since(start)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if timeout {
				want := fmt.Sprintf("refusing prepare: another argonreview prepare holds lock %s; wait for it or kill it", path)
				if err == nil || err.Error() != want || elapsed < wait || elapsed > 2*time.Second {
					t.Fatalf("verify = %v after %s; want %s", err, elapsed, want)
				}
			} else if err != nil || elapsed < 40*time.Millisecond {
				t.Fatalf("verify = %v after %s; did not wait for lock", err, elapsed)
			}
			if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatalf("verifier retained lock: %v", err)
			}
			if !reflect.DeepEqual(before, snapshot(t, home)) {
				t.Fatal("verification changed HOME")
			}
			unchanged()
		})
	}
}
