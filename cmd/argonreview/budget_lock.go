package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	budgetBrowserPath = "/var/cache/argonreview/browser.lock"
	budgetSlot0Path   = "/var/cache/argonreview/budget/slot-0.lock"
	budgetSlot1Path   = "/var/cache/argonreview/budget/slot-1.lock"
	budgetSlot2Path   = "/var/cache/argonreview/budget/slot-2.lock"
)

// The numeric order is shared by every caller, including browser-only callers.
// sweep.lock is reserved for #1909; nothing here creates or takes it.
type budgetLock uint8

const (
	budgetBrowser budgetLock = iota
	budgetSlot0
	budgetSlot1
	budgetSlot2
)

type budgetResult string

const (
	budgetBusy       budgetResult = "busy"
	budgetUnsafeLock budgetResult = "unavailable: lock files are unavailable or unsafe"
	budgetBadRequest budgetResult = "unavailable: invalid lock set"
	budgetCanceled   budgetResult = "unavailable: lock wait interrupted"
)

// Callers select a trusted noun; no command output enters resource reports.
func (r budgetResult) line(noun string) string {
	if r == "" {
		return ""
	}
	return "NOT VERIFIED: " + noun + " " + string(r)
}

type budgetLocks struct {
	paths        [4]string
	uid          int
	now          func() time.Time
	sleep        func(context.Context, time.Duration) error
	limit, poll  time.Duration
	aboutToSleep func()
	acquired     func(budgetLock, *os.File)
}

func newBudgetLocks() budgetLocks {
	return budgetLocks{paths: [4]string{budgetBrowserPath, budgetSlot0Path, budgetSlot1Path, budgetSlot2Path},
		uid: os.Getuid(), now: time.Now, sleep: budgetSleep, limit: 120 * time.Second, poll: time.Second}
}

func budgetSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Each open creates a distinct open file description: dup would share flock
// ownership. Process death releases flock, but cannot free a container's label.
// This primitive alone is not permission to start a container; reconciliation
// under the locks belongs to budgetSlots, as wired by runContainerExecution.
func (b budgetLocks) acquire(ctx context.Context, requested []budgetLock) (func(), budgetResult) {
	locks := slices.Clone(requested)
	slices.Sort(locks)
	if len(locks) == 0 || locks[len(locks)-1] > budgetSlot2 || len(slices.Compact(slices.Clone(locks))) != len(locks) || b.poll <= 0 || b.limit < 0 {
		return nil, budgetBadRequest
	}
	deadline := b.now().Add(b.limit)
	for {
		if ctx.Err() != nil {
			return nil, budgetCanceled
		}
		release, result := b.try(locks)
		if result != budgetBusy {
			if ctx.Err() != nil && release != nil {
				release()
				return nil, budgetCanceled
			}
			return release, result
		}
		remaining := deadline.Sub(b.now())
		if remaining <= 0 {
			return nil, budgetBusy
		}
		if b.aboutToSleep != nil {
			b.aboutToSleep()
		}
		if b.sleep(ctx, min(b.poll, remaining)) != nil {
			return nil, budgetCanceled
		}
		if !b.now().Before(deadline) {
			return nil, budgetBusy
		}
	}
}

func (b budgetLocks) try(locks []budgetLock) (func(), budgetResult) {
	var files []*os.File
	var once sync.Once
	release := func() {
		once.Do(func() {
			for _, f := range files {
				_ = f.Close()
			}
		})
	}
	for _, lock := range locks {
		f, result := b.open(lock)
		if result != "" {
			// Never retain a partial set across a wait, even on the browser lock.
			release()
			return nil, result
		}
		files = append(files, f)
		if b.acquired != nil {
			b.acquired(lock, f)
		}
	}
	return release, ""
}

func budgetSafeStat(st unix.Stat_t, directory bool, uid int) bool {
	if st.Uid != uint32(uid) {
		return false
	}
	if directory {
		return st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&0o022 == 0
	}
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&0o7777 == 0o600 && st.Nlink == 1
}

func (b budgetLocks) open(lock budgetLock) (*os.File, budgetResult) {
	path := b.paths[lock]
	// Holding the checked parent descriptor prevents pathname replacement
	// from redirecting creation. Missing directories fail closed; never mkdir.
	parent, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, budgetUnsafeLock
	}
	defer closeFD(parent)
	var st unix.Stat_t
	if unix.Fstat(parent, &st) != nil || !budgetSafeStat(st, true, b.uid) {
		return nil, budgetUnsafeLock
	}
	flags := unix.O_RDWR | unix.O_CREAT | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	// As with the prepare lock, exclusive creation makes simultaneous first
	// callers open the winner's file instead of racing fresh O_CREAT opens.
	fd, err := unix.Openat(parent, filepath.Base(path), flags|unix.O_EXCL, 0o600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(parent, filepath.Base(path), flags, 0o600)
	}
	if err != nil {
		return nil, budgetUnsafeLock
	}
	f := os.NewFile(uintptr(fd), path)
	if unix.Fstat(fd, &st) != nil || !budgetSafeStat(st, false, b.uid) {
		_ = f.Close()
		return nil, budgetUnsafeLock
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, budgetBusy
		}
		return nil, budgetUnsafeLock
	}
	return f, ""
}
