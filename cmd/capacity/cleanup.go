package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type worker struct {
	process  *os.Process
	pid      int // Process.Release changes Process.Pid to -1; retain the session ID.
	exited   bool
	exitCode int
}

func startCheck(root string, packages, parallel int) (*worker, string, error) {
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		return nil, "", fmt.Errorf("creating log directory: %w", err)
	}
	log, err := os.CreateTemp(filepath.Join(root, "bin"), "ai-capacity-*.log")
	if err != nil {
		return nil, "", fmt.Errorf("creating log: %w", err)
	}
	defer func() { _ = log.Close() }()
	path, err := exec.LookPath("make")
	if err != nil {
		return nil, "", fmt.Errorf("finding make: %w", err)
	}
	env := []string{"RIBBITTO_REQUIRE_DB=1",
		"GOCACHE=" + filepath.Join(root, "bin/.cache/go-build"),
		"GOLANGCI_LINT_CACHE=" + filepath.Join(root, "bin/.cache/lint")}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "RIBBITTO_REQUIRE_DB=") && !strings.HasPrefix(value, "GOCACHE=") && !strings.HasPrefix(value, "GOLANGCI_LINT_CACHE=") {
			env = append(env, value)
		}
	}
	// Poll and reap in one goroutine: a background Wait could release the PID
	// between checking completion and killing its group, reaching a reused PID.
	process, err := os.StartProcess(path, []string{"make", "check", fmt.Sprintf("GO_TEST_FLAGS=-count=1 -p %d -parallel %d", packages, parallel)}, &os.ProcAttr{
		Dir: root, Env: env, Files: []*os.File{os.Stdin, log, log}, Sys: &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return nil, "", fmt.Errorf("starting check: %w", err)
	}
	return &worker{process: process, pid: process.Pid}, log.Name(), nil
}

func (w *worker) poll() error {
	if w.exited {
		return nil
	}
	var status syscall.WaitStatus
	pid, err := syscall.Wait4(w.pid, &status, syscall.WNOHANG, nil)
	if err == syscall.EINTR {
		return nil
	}
	if err != nil {
		return fmt.Errorf("polling check: %w", err)
	}
	if pid != 0 {
		w.exited, w.exitCode = true, status.ExitStatus()
		if status.Signaled() {
			w.exitCode = -int(status.Signal())
		}
		_ = w.process.Release()
	}
	return nil
}

type cleanupOps struct {
	scan       func(int) ([]int, error)
	killGroup  func(int)
	killMember func(int, *worker)
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

func (c cleanupOps) stop(w *worker) bool {
	_ = w.poll()
	// A reaped leader no longer reserves its PID; only an unreaped group is safe.
	if !w.exited {
		c.killGroup(w.pid)
	}
	confirmed := false
	for range 100 {
		members, err := c.scan(w.pid)
		if err != nil {
			break
		}
		// The kernel never hands out a PID still in use as a session ID, so
		// once the leader is reaped, a process holding its PID means nothing
		// of ours is left in that session (#1053). Nobody holding it leaves
		// the members as our orphans, within the limit accepted in #636.
		if w.exited && c.held(w.pid) {
			confirmed = true
			break
		}
		remaining := 0
		for _, pid := range members {
			if !w.canBeMember(pid) {
				continue
			}
			remaining++
			c.killMember(pid, w)
		}
		if remaining == 0 {
			confirmed = true
			break
		}
		// A member can fork before it dies; its child inherits the same session.
		time.Sleep(20 * time.Millisecond)
	}
	if !w.exited {
		_, _ = w.process.Wait()
		w.exited = true
	}
	return confirmed
}

func sessionMembers(session int) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,stat=").Output()
	if err != nil {
		return nil, fmt.Errorf("scanning check session: %w", err)
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

func (w *worker) canBeMember(pid int) bool {
	// A reaped leader's PID can only belong to a process that is not ours
	// (Muse Code on #1035); stop skips that whole session (#1053).
	return !w.exited || pid != w.pid
}

func killMember(pid int, w *worker) {
	// macOS ps omits session IDs. Recheck just before the signal to narrow the
	// PID-reuse window (no pidfd there; the maintainer accepted this in #636).
	// A descendant calling setsid leaves this session; Chromium does not (#636).
	if !w.canBeMember(pid) {
		return
	}
	if sid, err := unix.Getsid(pid); err == nil && sid == w.pid {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
