package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// browserGatePath is host state, never an MCP argument or environment override.
const browserGatePath = "/var/cache/argonreview/browser-security-check"

const browserGateLock = "browser-security-check.lock"

// browserSecurityAge makes the exact 14-day boundary fail closed.
const browserSecurityAge = 14 * 24 * time.Hour

// browserOverdue covers unsafe and unparseable records without disclosing bytes.
const browserOverdue = "NOT VERIFIED: browser check paused: security check overdue"

// browserSecurity keeps the clock and host-state seams out of the wire protocol.
type browserSecurity struct {
	path          string
	uid           int
	now           func() time.Time
	sleep         func(time.Duration)
	lockLimit     time.Duration
	afterRead     func()
	beforePublish func()
}

// browserSecurityEntry stores only validated control data; the URL is never read.
type browserSecurityEntry struct {
	switchPath string
	completed  time.Time
	record     string
}

// newBrowserSecurity fixes production state while tests use private directories.
func newBrowserSecurity() browserSecurity {
	return browserSecurity{path: browserGatePath, uid: os.Geteuid(), now: time.Now,
		sleep: time.Sleep, lockLimit: 10 * time.Second}
}

// browserCompleted requires canonical UTC, including canonical fractional seconds.
func browserCompleted(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !strings.HasSuffix(value, "Z") || t.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("completion must be canonical RFC 3339 UTC ending in Z")
	}
	return t, nil
}

// browserRecordURL limits references to this repository's issues and comments.
func browserRecordURL(value string) bool {
	const prefix = "https://github.com/tkakkie/ribbitto/issues/"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	issue, comment, found := strings.Cut(strings.TrimPrefix(value, prefix), "#issuecomment-")
	return browserRecordDigits(issue) && (!found || browserRecordDigits(comment))
}

func browserRecordDigits(value string) bool {
	if len(value) < 1 || len(value) > 20 {
		return false
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// parent pins the checked directory for all descriptor-relative I/O.
func (b browserSecurity) parent() (int, error) {
	fd, err := unix.Open(filepath.Dir(b.path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("opening security check directory: %w", err)
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !budgetSafeStat(st, true, b.uid) {
		closeFD(fd)
		return -1, errors.New("unsafe security check directory")
	}
	return fd, nil
}

// readAt uses the same identity rules as lock files, with a bounded content read.
func (b browserSecurity) readAt(parent int) (browserSecurityEntry, error) {
	var entry browserSecurityEntry
	fd, err := unix.Openat(parent, filepath.Base(b.path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return entry, fmt.Errorf("opening security check (run --init if missing): %w", err)
	}
	f := os.NewFile(uintptr(fd), "browser-security-check")
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !budgetSafeStat(st, false, b.uid) {
		return entry, errors.New("unsafe security check file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 2049))
	if err != nil {
		return entry, fmt.Errorf("reading security check: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(data) > 2048 || len(lines) != 4 || lines[3] != "" || !strings.HasPrefix(lines[0], "switch=") || !strings.HasPrefix(lines[1], "completed=") || !strings.HasPrefix(lines[2], "record=") {
		return entry, errors.New("malformed security check")
	}
	entry.switchPath = strings.TrimPrefix(lines[0], "switch=")
	entry.completed, err = browserCompleted(strings.TrimPrefix(lines[1], "completed="))
	entry.record = strings.TrimPrefix(lines[2], "record=")
	if err != nil || browserModePath(entry.switchPath) != nil || !browserRecordURL(entry.record) {
		return browserSecurityEntry{}, errors.New("malformed security check fields")
	}
	return entry, nil
}

// admit never locks or creates files: atomic rename lets runners read without
// blocking on the host writer, after the switch and before budget reservation.
func (b browserSecurity) admit(switchPath string) string {
	parent, err := b.parent()
	if err != nil {
		return browserOverdue
	}
	defer closeFD(parent)
	entry, err := b.readAt(parent)
	if err != nil || switchPath == "" || entry.switchPath != switchPath {
		return browserOverdue
	}
	now := b.now()
	if entry.completed.After(now) || now.Sub(entry.completed) >= browserSecurityAge {
		return browserOverdue
	}
	return ""
}

// bytes emits the sole accepted format so readers can reject extra syntax.
func (e browserSecurityEntry) bytes() []byte {
	return []byte("switch=" + e.switchPath + "\ncompleted=" + e.completed.Format(time.RFC3339Nano) + "\nrecord=" + e.record + "\n")
}
