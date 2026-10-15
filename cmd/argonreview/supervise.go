package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// child is agy, the leader of its own session. Adapted from cmd/capacity's
// cleanup (#1059), because commands do not import each other.
type child struct {
	process *os.Process
	pid     int // Process.Release changes Process.Pid to -1; retain the session ID.
	exited  bool
	code    int
}

// supervisor runs one command with a time limit and leaves nothing it started.
type supervisor struct {
	limit, grace time.Duration
	signals      <-chan os.Signal
	ops          cleanupOps
}

// run starts path in its own session with stdin closed and returns its exit
// status: 124 when the time limit ends it, 128 plus the signal when an INT or
// TERM received here does, as timeout(1) and shells report them. A leader
// killed by a signal also reports 128 plus that signal.
func (s supervisor) run(path string, args, env []string, dir string, stdout, stderr *os.File) (int, error) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return 1, fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	// Poll and reap in this goroutine only: a background Wait could release
	// the PID between checking completion and signalling the group, reaching
	// a reused PID (#1059).
	process, err := os.StartProcess(path, append([]string{path}, args...), &os.ProcAttr{
		Dir: dir, Env: env, Files: []*os.File{stdin, stdout, stderr}, Sys: &syscall.SysProcAttr{Setsid: true},
	})
	_ = stdin.Close()
	if err != nil {
		return 1, fmt.Errorf("starting %s: %w", path, err)
	}
	c := &child{process: process, pid: process.Pid}
	limit := time.NewTimer(s.limit)
	defer limit.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	code := -1
	var pollErr error
	for code < 0 {
		if pollErr = c.poll(); pollErr != nil {
			code = 1
			break
		}
		if c.exited {
			code = c.code
			break
		}
		select {
		case <-limit.C:
			code = 124
		case sig := <-s.signals:
			code = 128 + int(sig.(syscall.Signal))
		case <-ticker.C:
		}
	}
	if !c.exited {
		s.terminate(c)
	}
	if !s.ops.stop(c) {
		pollErr = errors.Join(pollErr, fmt.Errorf("could not confirm that every process %s started stopped", path))
	}
	return code, pollErr
}

// terminate sends TERM to the unreaped leader's group and gives it the grace
// period to exit; cleanupOps.stop kills whatever is left.
func (s supervisor) terminate(c *child) {
	// Unreaped, the leader still holds its PID, so the group is ours.
	_ = syscall.Kill(-c.pid, syscall.SIGTERM)
	deadline := time.Now().Add(s.grace)
	for !c.exited && time.Now().Before(deadline) {
		if c.poll() != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *child) poll() error {
	if c.exited {
		return nil
	}
	var status syscall.WaitStatus
	pid, err := syscall.Wait4(c.pid, &status, syscall.WNOHANG, nil)
	if err == syscall.EINTR {
		return nil
	}
	if err != nil {
		return fmt.Errorf("polling %d: %w", c.pid, err)
	}
	if pid != 0 {
		c.exited, c.code = true, status.ExitStatus()
		if status.Signaled() {
			c.code = 128 + int(status.Signal())
		}
		_ = c.process.Release()
	}
	return nil
}

type cleanupOps struct {
	scan       func(int) ([]int, error)
	killGroup  func(int)
	killMember func(int, *child)
	held       func(int) bool
}

func systemCleanup() cleanupOps {
	return cleanupOps{sessionMembers, func(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }, killMember, pidHeld}
}

// pidHeld reports whether any process, ours or not, has the PID; EPERM means
// one exists that this process may not signal.
func pidHeld(pid int) bool {
	return syscall.Kill(pid, 0) != syscall.ESRCH
}

// stop kills the leader's group and every member left in its session, and
// reports whether it could confirm that none is left.
func (o cleanupOps) stop(c *child) bool {
	_ = c.poll()
	// A reaped leader no longer reserves its PID; only an unreaped group is safe.
	if !c.exited {
		o.killGroup(c.pid)
	}
	// The kernel never hands out a PID still in use as a session ID, so once
	// the leader is reaped, a process holding its PID means nothing of ours
	// is left in that session (#1053). Checked before each scan, so a failing
	// scan cannot leave such a session unconfirmed (the maintainer on #1059),
	// and again after it.
	reused := func() bool { return c.exited && o.held(c.pid) }
	for range 100 {
		if reused() {
			return o.reap(c, true)
		}
		members, err := o.scan(c.pid)
		if err != nil {
			return o.reap(c, false)
		}
		if reused() {
			return o.reap(c, true)
		}
		remaining := 0
		for _, pid := range members {
			if c.canBeMember(pid) {
				remaining++
				o.killMember(pid, c)
			}
		}
		if remaining == 0 {
			return o.reap(c, true)
		}
		// A member can fork before it dies; its child inherits the session.
		time.Sleep(20 * time.Millisecond)
	}
	return o.reap(c, false)
}

func (o cleanupOps) reap(c *child, confirmed bool) bool {
	if !c.exited {
		_, _ = c.process.Wait()
		c.exited = true
	}
	return confirmed
}

func sessionMembers(session int) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,stat=").Output()
	if err != nil {
		return nil, fmt.Errorf("scanning session %d: %w", session, err)
	}
	return members(string(out), session, unix.Getsid), nil
}

func members(listing string, session int, getsid func(int) (int, error)) []int {
	var found []int
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		// Zombies run nothing and cannot be killed, even if init never reaps them.
		if len(fields) == 0 || len(fields) > 1 && strings.HasPrefix(fields[1], "Z") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == os.Getpid() {
			continue
		}
		if sid, err := getsid(pid); err == nil && sid == session {
			found = append(found, pid)
		}
	}
	return found
}

func (c *child) canBeMember(pid int) bool {
	// A reaped leader's PID can only belong to a process that is not ours;
	// stop skips that whole session (#1053).
	return !c.exited || pid != c.pid
}

func killMember(pid int, c *child) {
	// macOS ps omits session IDs. Recheck just before the signal to narrow the
	// PID-reuse window (no pidfd there; the maintainer accepted this in #636).
	// A descendant calling setsid leaves this session, the same limit as #636.
	if !c.canBeMember(pid) {
		return
	}
	if sid, err := unix.Getsid(pid); err == nil && sid == c.pid {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
