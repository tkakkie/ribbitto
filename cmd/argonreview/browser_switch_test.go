package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// browserWrite keeps modes explicit even when replacing an existing fixture.
func browserWrite(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// browserRemove fails immediately when a fixture cannot be changed as intended.
func browserRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// TestBrowserSwitch isolates each property so no other refusal masks a defect.
func TestBrowserSwitch(t *testing.T) {
	for _, row := range []struct {
		name, data string
		on         bool
	}{
		{"on", "on", true}, {"newline", "on\n", true}, {"empty", "", false},
		{"off", "off", false}, {"uppercase", "ON", false}, {"CRLF", "on\r\n", false},
		{"space", "on ", false}, {"two-newlines", "on\n\n", false}, {"NUL", "on\x00", false},
		{"long", "on\n" + strings.Repeat("x", 4096), false},
	} {
		t.Run(row.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "browser-mode")
			browserWrite(t, path, row.data, 0o400)
			if got := browserSwitchOn(path, os.Geteuid()); got != row.on {
				t.Fatalf("switch accepted=%t, want %t", got, row.on)
			}
		})
	}
	for _, kind := range []string{"missing", "unreadable", "symlink", "directory", "owner", "group-write", "world-write", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "browser-mode")
			browserWrite(t, path, "on", 0o400)
			uid := os.Geteuid()
			switch kind {
			case "missing":
				browserRemove(t, path)
			case "unreadable":
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := path + "-real"
				browserWrite(t, target, "on", 0o400)
				browserRemove(t, path)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				browserRemove(t, path)
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "owner":
				uid++
			case "group-write":
				if err := os.Chmod(path, 0o420); err != nil {
					t.Fatal(err)
				}
			case "world-write":
				if err := os.Chmod(path, 0o402); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				browserRemove(t, path)
				if err := unix.Mkfifo(path, 0o400); err != nil {
					t.Fatal(err)
				}
			}
			if browserSwitchOn(path, uid) {
				t.Fatal("unsafe switch admitted")
			}
		})
	}
	for _, order := range [][]string{{"on", "off"}, {"off", "on"}} {
		t.Run(strings.Join(order, "-then-"), func(t *testing.T) {
			b, path, entry := browserGateFixture(t)
			browserWrite(t, b.path, string(entry.bytes()), 0o600)
			browserWrite(t, path, order[0], 0o400)
			v := newContainerVerifier("unused", path)
			v.browserSecurity = b
			for _, value := range order {
				browserWrite(t, path, value, 0o400)
				want := browserPaused
				if value == "on" {
					want = ""
				}
				if got := v.browserAdmission(); got != want {
					t.Fatalf("per-call switch %s: line=%q, want %q", value, got, want)
				}
			}
		})
	}
}

// TestBrowserLock proves contention is on browser.lock even with two free units.
func TestBrowserLock(t *testing.T) {
	b, i, _ := budgetSlotsFixture(t)
	b.locks.limit = newBudgetLocks().limit
	if b.locks.limit != 120*time.Second {
		t.Fatalf("browser wait limit=%s, want 120s", b.locks.limit)
	}
	holder := budgetTake(t, b.locks, budgetBrowser)
	// One label occupies slot 2: slots 0 and 1 are free throughout the wait.
	i.containers = []budgetContainer{budgetContainerFixture("2")}
	now := time.Unix(0, 0)
	start := now
	polls := 0
	b.locks.now = func() time.Time { return now }
	b.locks.sleep = func(_ context.Context, d time.Duration) error { polls++; now = now.Add(d); return nil }
	var acquired []budgetLock
	b.locks.acquired = func(k budgetLock, _ *os.File) { acquired = append(acquired, k) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r, cause := b.budgetReserve(ctx, budgetBrowserRequest)
	if r.release != nil {
		r.release()
		t.Fatal("browser reservation admitted while browser.lock held")
	}
	if line := browserBudgetLine(cause); line != "NOT VERIFIED: browser check busy" {
		t.Fatalf("browser busy line=%q", line)
	}
	if now.Sub(start) != 120*time.Second || polls != 120 || len(acquired) != 0 {
		t.Fatalf("wait=%s polls=%d acquired=%v; no reservation may start a container", now.Sub(start), polls, acquired)
	}
	// A single unit can proceed while the same browser holder remains alive.
	single := budgetAssertReserve(t, b, budgetSingle, "0")
	if slices.Contains(acquired, budgetBrowser) {
		t.Fatal("one-unit request took browser.lock")
	}
	single.release()
	acquired = nil
	polls = 0
	b.locks.sleep = func(_ context.Context, d time.Duration) error {
		polls++
		now = now.Add(d)
		if polls == 30 {
			holder()
		}
		return nil
	}
	r, cause = b.budgetReserve(ctx, budgetBrowserRequest)
	if cause != "" || r.release == nil {
		t.Fatalf("released holder did not permit reservation: %s", cause)
	}
	defer r.release()
	if polls != 30 || r.label != "0,1" || !slices.Equal(acquired, []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1}) {
		t.Fatalf("mid-wait release: polls=%d label=%q locks=%v", polls, r.label, acquired)
	}
	for _, cause := range []budgetResult{"", budgetUnsafeLock, budgetCanceled, budgetResult("paused: an exclusive container is present"), budgetStateUnknown} {
		if got, want := browserBudgetLine(cause), cause.line("verification budget"); got != want {
			t.Fatalf("non-busy line=%q want=%q", got, want)
		}
	}
}
