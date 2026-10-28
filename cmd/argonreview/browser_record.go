package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// browserRecordUsage keeps initial provisioning distinct from routine recording.
const browserRecordUsage = "browser-check-record [--init --browser-mode <abs path>] --completed-at <RFC3339 UTC Z> --record <issue URL>"

// browserInitFlag refuses repeated or false init flags rather than treating a
// provisioning typo as a routine update.
type browserInitFlag struct{ set bool }

// String supplies the fixed flag default without exposing input.
func (b *browserInitFlag) String() string { return "false" }

// IsBoolFlag permits the standalone --init spelling.
func (b *browserInitFlag) IsBoolFlag() bool { return true }

// Set admits only one affirmative provisioning request.
func (b *browserInitFlag) Set(value string) error {
	if b.set || value != "true" {
		return errors.New("invalid init flag")
	}
	b.set = true
	return nil
}

// browserRecordCommand is host-only; neither MCP nor the guard dispatches here.
func browserRecordCommand(args []string, stdout, stderr io.Writer, b browserSecurity) int {
	flags := flag.NewFlagSet("browser-check-record", flag.ContinueOnError)
	// The flag package echoes invalid values; keep all diagnostics fixed instead.
	flags.SetOutput(io.Discard)
	var init browserInitFlag
	var path, completed, record implementOnceFlag
	flags.Var(&init, "init", "initial provisioning")
	flags.Var(&path, "browser-mode", "trusted switch")
	flags.Var(&completed, "completed-at", "completion time")
	flags.Var(&record, "record", "issue URL")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || init.set != path.set {
		_, _ = fmt.Fprintln(stderr, "usage: argonreview "+browserRecordUsage)
		return 2
	}
	t, err := browserCompleted(completed.value)
	if err != nil || !browserRecordURL(record.value) || init.set && browserModePath(path.value) != nil {
		_, _ = fmt.Fprintln(stderr, "browser-check-record: invalid completion, reference or switch path")
		return 2
	}
	if err := b.record(init.set, path.value, t, record.value); err != nil {
		_, _ = fmt.Fprintf(stderr, "browser-check-record: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "browser-check-record: recorded"); err != nil {
		return 1
	}
	return 0
}

// browserInitSwitch checks the provisioning location, not whether it is enabled.
func browserInitSwitch(path string, uid int) error {
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(uid) {
		return errors.New("initial switch must be a regular owned file")
	}
	dir := filepath.Dir(path)
	if unix.Lstat(dir, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(uid) || st.Mode&0o7777 != 0o500 {
		return errors.New("initial switch directory must be owned and mode 0500")
	}
	info, err := os.Lstat(filepath.Join(dir, "verify-mode"))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("initial switch directory must contain regular verify-mode")
	}
	return nil
}

// record never takes a routine switch argument: its identity comes only from
// the once-provisioned record, so a fresh check cannot select a different switch.
func (b browserSecurity) record(init bool, path string, completed time.Time, reference string) error {
	parent, err := b.parent()
	if err != nil {
		return err
	}
	defer closeFD(parent)
	lock, err := b.lockRecord(parent)
	if err != nil {
		return err
	}
	defer closeFD(lock)
	if b.afterLock != nil {
		b.afterLock()
	}
	var entry browserSecurityEntry
	if init {
		if err := browserInitSwitch(path, b.uid); err != nil {
			return err
		}
	} else {
		entry, err = b.readAt(parent)
		if err != nil {
			return err
		}
		if b.afterRead != nil {
			b.afterRead()
		}
		path = entry.switchPath
	}
	// Waiting for the writer and reading its entry must precede time decisions.
	now := b.now()
	if completed.After(now) {
		return errors.New("completion is in the future")
	}
	if !init {
		if completed.Before(entry.completed) {
			return errors.New("completion is older than the recorded check")
		}
		// Refreshing an expired entry while on would silently re-enable checks.
		// An explicit pause must precede that refresh and subsequent approval.
		if now.Sub(entry.completed) >= browserSecurityAge && browserSwitchOn(path, b.uid) {
			return errors.New("expired check cannot be refreshed while browser-mode is on")
		}
	}
	entry = browserSecurityEntry{path, completed, reference}
	if len(entry.bytes()) > 2048 {
		return errors.New("security check exceeds size limit")
	}
	return b.publish(parent, entry.bytes(), init)
}

// lockRecord uses a sibling because publication replaces the gate inode; a
// lock on the gate itself would not exclude writers opening its replacement.
func (b browserSecurity) lockRecord(parent int) (int, error) {
	unsafe := errors.New("security check lock unavailable or unsafe")
	fd, err := unix.Openat(parent, browserGateLock, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return -1, unsafe
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !budgetSafeStat(st, false, b.uid) {
		closeFD(fd)
		return -1, unsafe
	}
	// This clock only bounds the lock wait; it makes no record-time decision.
	deadline := b.now().Add(b.lockLimit)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return fd, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			closeFD(fd)
			return -1, unsafe
		}
		remaining := deadline.Sub(b.now())
		if remaining <= 0 {
			closeFD(fd)
			return -1, errors.New("security check is being updated")
		}
		b.sleep(min(100*time.Millisecond, remaining))
	}
}

// publish uses a same-directory exclusive temporary file; linkat publishes an
// init without replacement on both macOS and Linux, including existing symlinks.
func (b browserSecurity) publish(parent int, data []byte, init bool) error {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("naming security check: %w", err)
	}
	name := fmt.Sprintf(".browser-security-check-%x", suffix)
	fd, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("creating security check: %w", err)
	}
	defer func() { _ = unix.Unlinkat(parent, name, 0) }()
	f := os.NewFile(uintptr(fd), "browser-security-check")
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("writing security check: %w", err)
	}
	target := filepath.Base(b.path)
	if b.beforePublish != nil {
		b.beforePublish()
	}
	if init {
		err = unix.Linkat(parent, name, parent, target, 0)
	} else {
		err = unix.Renameat(parent, name, parent, target)
	}
	if err != nil {
		return fmt.Errorf("publishing security check: %w", err)
	}
	if init {
		if err := unix.Unlinkat(parent, name, 0); err != nil {
			return fmt.Errorf("removing security check staging link: %w", err)
		}
	}
	if err := unix.Fsync(parent); err != nil {
		return fmt.Errorf("syncing security check directory: %w", err)
	}
	return nil
}
