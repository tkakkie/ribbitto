package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func budgetLockFixture(t *testing.T) budgetLocks {
	t.Helper()
	b := newBudgetLocks()
	dir := t.TempDir()
	for i := range b.paths {
		b.paths[i] = filepath.Join(dir, fmt.Sprintf("lock-%d", i))
	}
	return b
}

func budgetTake(t *testing.T, b budgetLocks, locks ...budgetLock) func() {
	t.Helper()
	release, result := b.acquire(context.Background(), locks)
	if result != "" {
		t.Fatalf("acquire: %s", result)
	}
	t.Cleanup(release)
	return release
}

func TestBudgetLockOrder(t *testing.T) {
	b := budgetLockFixture(t)
	var order []budgetLock
	b.acquired = func(k budgetLock, _ *os.File) { order = append(order, k) }
	release := budgetTake(t, b, budgetSlot2, budgetSlot0, budgetBrowser, budgetSlot1)
	if !slices.Equal(order, []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2}) {
		t.Fatalf("order: %v", order)
	}
	release()
	release()
	budgetTake(t, b, budgetBrowser)
}

func TestBudgetLockSlots(t *testing.T) {
	b := budgetLockFixture(t)
	b.limit = 0
	var holders []func()
	for _, slot := range []budgetLock{budgetSlot0, budgetSlot1, budgetSlot2} {
		holders = append(holders, budgetTake(t, b, slot))
	}
	for _, slot := range []budgetLock{budgetSlot0, budgetSlot1, budgetSlot2} {
		if release, result := b.acquire(context.Background(), []budgetLock{slot}); release != nil || result != budgetBusy {
			t.Fatalf("fourth unit acquired slot %d: %s", slot, result)
		}
	}
	for _, release := range holders {
		release()
	}
	budgetTake(t, b, budgetBrowser, budgetSlot0, budgetSlot1)()
}

func TestBudgetLockWait(t *testing.T) {
	b := budgetLockFixture(t)
	holder := budgetTake(t, b, budgetSlot1)
	now := time.Unix(0, 0)
	b.now = func() time.Time { return now }
	polls := 0
	b.aboutToSleep = func() {
		// A failed set must release browser and slot 0 before waiting on slot 1.
		probe := newBudgetLocks()
		probe.paths, probe.limit = b.paths, 0
		budgetTake(t, probe, budgetBrowser, budgetSlot0)()
	}
	b.sleep = func(_ context.Context, d time.Duration) error {
		polls++
		if d != time.Second || polls > 120 {
			t.Fatalf("unbounded or wrong poll: %d, %v", polls, d)
		}
		now = now.Add(d)
		return nil
	}
	_, result := b.acquire(context.Background(), []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1})
	if result.line("verification budget") != "NOT VERIFIED: verification budget busy" || polls != 120 {
		t.Fatalf("result=%s polls=%d", result, polls)
	}
	if result.line("browser check") != "NOT VERIFIED: browser check busy" {
		t.Fatal("caller noun lost")
	}
	// The next call succeeds after contention clears, including mid-wait.
	b.sleep = func(_ context.Context, _ time.Duration) error { holder(); return nil }
	budgetTake(t, b, budgetSlot1)()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, got := b.acquire(ctx, []budgetLock{budgetSlot0}); release != nil || got != budgetCanceled {
		t.Fatalf("canceled: %s", got)
	}
	ctx, cancel = context.WithCancel(context.Background())
	b.sleep = budgetSleep
	b.aboutToSleep = cancel
	budgetTake(t, b, budgetSlot2)
	if _, got := b.acquire(ctx, []budgetLock{budgetSlot2}); got != budgetCanceled {
		t.Fatalf("cancellation while waiting: %s", got)
	}
}

func TestBudgetUnknownState(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "directory", "link", "group", "world", "owner", "missing", "parent-link", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			b := budgetLockFixture(t)
			path := b.paths[budgetSlot0]
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "mode":
				err = os.Chmod(path, 0o640)
			case "symlink", "directory", "fifo":
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink":
					err = os.Symlink("absent", path)
				case "directory":
					err = os.Mkdir(path, 0o700)
				case "fifo":
					err = unix.Mkfifo(path, 0o600)
				}
			case "link":
				err = os.Link(path, path+"-extra")
			case "group":
				err = os.Chmod(filepath.Dir(path), 0o720)
			case "world":
				err = os.Chmod(filepath.Dir(path), 0o702)
			case "owner":
				b.uid++
			case "missing":
				b.paths[budgetSlot0] = filepath.Join(path+"-missing", "lock")
			case "parent-link":
				alias := filepath.Join(t.TempDir(), "alias")
				err = os.Symlink(filepath.Dir(path), alias)
				b.paths[budgetSlot0] = filepath.Join(alias, filepath.Base(path))
			}
			if err != nil {
				t.Fatal(err)
			}
			if release, result := b.acquire(context.Background(), []budgetLock{budgetSlot0}); release != nil || result != budgetUnsafeLock {
				t.Fatalf("unsafe lock accepted: %s", result)
			}
		})
	}
	st := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Nlink: 1, Uid: uint32(os.Getuid())}
	if !budgetSafeStat(st, false, os.Getuid()) || budgetSafeStat(st, false, os.Getuid()+1) {
		t.Fatal("file ownership check")
	}
	for _, locks := range [][]budgetLock{nil, {budgetSlot0, budgetSlot0}, {4}} {
		if _, result := budgetLockFixture(t).acquire(context.Background(), locks); result != budgetBadRequest {
			t.Fatalf("invalid set accepted: %v", locks)
		}
	}
}

func TestBudgetNoDeadlock(t *testing.T) {
	for _, row := range []struct {
		name string
		run  func(*testing.T)
	}{{"primitive", budgetPrimitiveNoDeadlock}, {"wired", budgetWiredNoDeadlock}} {
		t.Run(row.name, row.run)
	}
}

func budgetPrimitiveNoDeadlock(t *testing.T) {
	b := budgetLockFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, shape := range [][]budgetLock{{budgetBrowser, budgetSlot0, budgetSlot1}, {budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2}, {budgetSlot0}, {budgetSlot1}} {
		wg.Go(func() {
			own := b
			own.poll = time.Millisecond
			var opened []*os.File
			own.acquired = func(_ budgetLock, f *os.File) { opened = append(opened, f) }
			own.aboutToSleep = func() {
				for _, f := range opened {
					if f.Fd() != ^uintptr(0) {
						t.Error("partial set held while sleeping")
					}
				}
				opened = nil
			}
			for range 15 {
				release, result := own.acquire(ctx, shape)
				if result != "" {
					t.Errorf("acquirer did not finish: %s", result)
					return
				}
				time.Sleep(time.Millisecond)
				release()
				opened = nil
			}
		})
	}
	wg.Wait()
}
