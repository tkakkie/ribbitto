package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"-tailwind", "css", "-templ", "templ"}, true},
		{[]string{"-tailwind=css", "-templ=templ"}, true},
		{nil, false},
		{[]string{"-tailwind=css"}, false},
		{[]string{"-templ=templ"}, false},
		{[]string{"-tailwind=", "-templ=templ"}, false},
		{[]string{"-unknown"}, false},
		{[]string{"-tailwind=css", "-templ=templ", "extra"}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			css, templ, err := parse(tc.args)
			if (err == nil) != tc.ok || tc.ok && (css != "css" || templ != "templ") {
				t.Fatalf("parse = %q, %q, %v", css, templ, err)
			}
		})
	}
}

func TestSupervise(t *testing.T) {
	for _, tc := range []struct {
		name    string
		signal  os.Signal
		exit    string
		failure bool
	}{
		{"interrupt", os.Interrupt, "", false},
		{"terminate", syscall.SIGTERM, "", false},
		{"css exits", nil, "css", false},
		{"bundles exits", nil, "bundles", false},
		{"templ exits", nil, "templ", false},
		{"css fails", nil, "css", true},
		{"bundles fails", nil, "bundles", true},
		{"templ fails", nil, "templ", true},
		{"second start fails", nil, "start", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := helper("supervisor", root)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			cmd.Env = append(cmd.Env, "DEV_EXIT="+tc.exit, "DEV_FAILURE="+strconv.FormatBool(tc.failure))
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			// Cleanup also runs on a failed assertion or denied sandbox signal.
			defer func() {
				_ = cmd.Process.Kill()
				files, _ := filepath.Glob(filepath.Join(root, "*.pid"))
				for _, file := range files {
					data, _ := os.ReadFile(file)
					pid, _ := strconv.Atoi(string(data))
					if pid > 0 {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}()
			roles := []string{"css", "css-child", "css-child-child", "bundles", "bundles-child", "bundles-child-child", "templ", "templ-child", "templ-child-child"}
			if tc.exit == "start" {
				roles = roles[:0]
			}
			for _, role := range roles {
				waitFor(t, ctx, func() bool {
					data, _ := os.ReadFile(filepath.Join(root, role+".pid"))
					pid, err := strconv.Atoi(string(data))
					return err == nil && pid > 0
				})
			}
			if tc.signal != nil {
				if err := cmd.Process.Signal(tc.signal); err != nil {
					t.Fatal(err)
				}
			} else if tc.exit != "start" {
				if err := os.WriteFile(filepath.Join(root, "exit"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if tc.failure && tc.exit != "start" && !strings.Contains(output.String(), "exit status 7") {
					t.Errorf("watcher failure lost: %s", output.String())
				}
				if (err != nil) != tc.failure {
					t.Errorf("supervisor exit = %v, want failure = %v: %s", err, tc.failure, output.String())
				}
			case <-ctx.Done():
				t.Fatal("supervisor did not stop")
			}
			for _, role := range roles {
				data, err := os.ReadFile(filepath.Join(root, role+".pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, _ := strconv.Atoi(string(data))
				waitFor(t, ctx, func() bool { return !alive(t, pid) })
			}
		})
	}
}

// Copilot on #1042 questioned whether a leader alone can block shutdown.
func TestSuperviseLeaderIgnoresSIGTERM(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := helper("leader", root)
	done := make(chan error, 1)
	go func() { done <- supervise(ctx, []*exec.Cmd{cmd}) }()
	defer func() {
		data, _ := os.ReadFile(filepath.Join(root, "leader.pid"))
		if pid, err := strconv.Atoi(string(data)); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	ready, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	waitFor(t, ready, func() bool {
		_, err := os.Stat(filepath.Join(root, "leader.pid"))
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("supervise: %v", err)
		}
		status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Errorf("leader exit = %v, want SIGKILL", cmd.ProcessState)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supervise did not return within the shutdown deadline")
	}
}

// Copilot on #1042 found that a failure delivered before shutdown can be lost.
func TestSupervisePreservesDeliveredFailure(t *testing.T) {
	t.Run("shutdown boundary", func(t *testing.T) {
		var watchers []watcher
		// Reap the failure before the clean exit so both results are available
		// at the shutdown boundary without relying on scheduler timing.
		for _, code := range []string{"7", "0"} {
			cmd := exec.Command("/bin/sh", "-c", "exit "+code)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			w := watcher{cmd, make(chan error, 1), cmd.Process.Pid, &atomic.Bool{}}
			w.done <- cmd.Wait()
			w.reaped.Store(true)
			close(w.done)
			watchers = append(watchers, w)
		}
		var exit *exec.ExitError
		if err := shutdown(watchers); !errors.As(err, &exit) || exit.ExitCode() != 7 {
			t.Fatalf("shutdown = %v, want the delivered exit status 7", err)
		}
	})
	t.Run("supervise", func(t *testing.T) {
		commands := []*exec.Cmd{
			exec.Command("/bin/sh", "-c", "exit 7"),
			exec.Command("/bin/sh", "-c", "exit 0"),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var exit *exec.ExitError
		if err := supervise(ctx, commands); !errors.As(err, &exit) || exit.ExitCode() != 7 {
			t.Fatalf("supervise = %v, want the watcher failure", err)
		}
	})
}

// holdDelivery delays the delivery of cmd's result until release is closed,
// and closes reaped once cmd has been reaped.
func holdDelivery(t *testing.T, cmd *exec.Cmd, release <-chan struct{}) <-chan struct{} {
	t.Helper()
	reaped := make(chan struct{})
	original := deliver
	t.Cleanup(func() { deliver = original })
	deliver = func(w watcher, result error) {
		if w.cmd == cmd {
			close(reaped)
			<-release
		}
		original(w, result)
	}
	return reaped
}

func setBeforeSignals(t *testing.T, hook func()) {
	t.Helper()
	original := beforeSignals
	t.Cleanup(func() { beforeSignals = original })
	beforeSignals = hook
}

// The maintainer on #1042: a watcher that failed before shutdown signalled
// anything must be reported even when its result is delivered only after the
// signals. The failing watcher is reaped first and its result is held until
// shutdown's first signal; the other exits cleanly only once the failing one
// is reaped, so synchronisation, not timing, fixes the order.
func TestSuperviseReportsAFailureDeliveredAfterSignals(t *testing.T) {
	dir := t.TempDir()
	failing := exec.Command("/bin/sh", "-c", "exit 7")
	clean := exec.Command("/bin/sh", "-c", `until [ -e "$1" ]; do sleep 0.01; done`, "clean", filepath.Join(dir, "reaped"))
	release := make(chan struct{})
	reaped := holdDelivery(t, failing, release)
	setBeforeSignals(t, func() { close(release) })
	go func() {
		<-reaped
		_ = os.WriteFile(filepath.Join(dir, "reaped"), nil, 0o600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var exit *exec.ExitError
	if err := supervise(ctx, []*exec.Cmd{failing, clean}); !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("supervise = %v, want the failure delivered after the signals", err)
	}
}

// Codex on #1042: a watcher still running when shutdown reads the process
// table that exits with status 7 just before the first signal failed on its
// own, and must be reported whether its result is delivered at once or late.
// It ends at the same point in both runs: shutdown's hook just before the
// first signal.
func TestShutdownReportsAFailureJustBeforeTheSignals(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run("late "+strconv.FormatBool(late), func(t *testing.T) {
			dir := t.TempDir()
			stop := filepath.Join(dir, "stop")
			cmd := exec.Command("/bin/sh", "-c", `until [ -e "$1" ]; do sleep 0.01; done; exit 7`, "watcher", stop)
			release := make(chan struct{})
			reaped := holdDelivery(t, cmd, release)
			w, err := start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			setBeforeSignals(t, func() {
				if err := os.WriteFile(stop, nil, 0o600); err != nil {
					t.Error(err)
				}
				<-reaped
				if !late {
					close(release)
					for len(w.done) == 0 {
						time.Sleep(time.Millisecond)
					}
				}
			})
			if late {
				go func() { <-reaped; time.Sleep(100 * time.Millisecond); close(release) }()
			}
			var exit *exec.ExitError
			if err := shutdown([]watcher{w}); !errors.As(err, &exit) || exit.ExitCode() != 7 {
				t.Fatalf("shutdown = %v, want the exit status 7", err)
			}
		})
	}
}

// A watcher that ends because of shutdown's signals is not a failure, whether
// it dies of the signal (as Tailwind does) or exits 0 (as templ does). Each
// script writes a marker once its handling is in place, and the way it ended
// is checked, so each case exercises what it names.
func TestShutdownIgnoresExitsCausedByTheSignals(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		signaled     bool
	}{
		{"killed by the signal", `: > "$1"; while :; do sleep 0.01; done`, true},
		{"exits 0 on SIGTERM", `trap 'exit 0' TERM; : > "$1"; while :; do sleep 0.01; done`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			w, err := start(exec.Command("/bin/sh", "-c", tc.script, "watcher", ready))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			waitFor(t, ctx, func() bool { _, err := os.Stat(ready); return err == nil })
			if err := shutdown([]watcher{w}); err != nil {
				t.Fatalf("shutdown = %v, want no failure", err)
			}
			status := w.cmd.ProcessState.Sys().(syscall.WaitStatus)
			if status.Signaled() != tc.signaled || !tc.signaled && status.ExitStatus() != 0 {
				t.Fatalf("watcher ended with %v, want signaled=%v", w.cmd.ProcessState, tc.signaled)
			}
		})
	}
}

// Codex on #1042: if the process table cannot be read before the signals,
// running and failed watchers cannot be told apart, so shutdown still stops
// everything but reports the failure.
func TestShutdownReportsAnUnreadableTable(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	w, err := start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	original := listProcesses
	t.Cleanup(func() { listProcesses = original })
	calls := 0
	listProcesses = func() (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("ps denied")
		}
		return original()
	}
	if err := shutdown([]watcher{w}); err == nil || !strings.Contains(err.Error(), "reading the process table before shutdown") {
		t.Fatalf("shutdown = %v, want the first scan's failure", err)
	}
	if alive(t, w.pid) {
		t.Fatal("the watcher survived shutdown")
	}
}

// Codex on #1042: a reaped watcher whose PID now belongs to a new session
// leader must still count as exited, so its late failure is reported. The
// watcher record points at a real, unrelated session leader's PID.
func TestShutdownReportsAReapedWatcherWhosePIDWasReused(t *testing.T) {
	failing := exec.Command("/bin/sh", "-c", "exit 7")
	release := make(chan struct{})
	reaped := holdDelivery(t, failing, release)
	w, err := start(failing)
	if err != nil {
		t.Fatal(err)
	}
	<-reaped
	// exec keeps the stranger childless, so this test is about the verdict;
	// TestShutdownSkipsAReusedSession gives the stranger a child.
	stranger := exec.Command("/bin/sh", "-c", "exec sleep 30")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stranger.Process.Kill(); _ = stranger.Wait() })
	w.pid = stranger.Process.Pid
	setBeforeSignals(t, func() { close(release) })
	var exit *exec.ExitError
	err = shutdown([]watcher{w})
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || strings.Contains(err.Error(), "remain") {
		t.Fatalf("shutdown = %v, want only the reaped watcher's failure", err)
	}
	if !alive(t, stranger.Process.Pid) {
		t.Fatal("shutdown signalled the unrelated session leader")
	}
}

// Codex on #1042: between Wait reaping a watcher and its reaped flag being
// set, the PID can be reused by a new session leader. Paused exactly there,
// the watcher's PID is taken over by such a leader that is not this
// process's child, and its failure must still be reported.
func TestShutdownReportsAWatcherReusedBeforeItsFlag(t *testing.T) {
	dir := t.TempDir()
	failing := exec.Command("/bin/sh", "-c", "exit 7")
	paused, resume := make(chan struct{}), make(chan struct{})
	original := afterReap
	t.Cleanup(func() { afterReap = original })
	afterReap = func(w watcher) {
		if w.cmd == failing {
			close(paused)
			<-resume
		}
	}
	w, err := start(failing)
	if err != nil {
		t.Fatal(err)
	}
	<-paused
	if err := helper("orphan", dir).Run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "orphan.pid"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	child := 0
	t.Cleanup(func() {
		_ = syscall.Kill(orphan, syscall.SIGKILL)
		if child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waitFor(t, ctx, func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "orphan-child.pid"))
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child > 0
	})
	w.pid = orphan
	setBeforeSignals(t, func() { close(resume) })
	var exit *exec.ExitError
	err = shutdown([]watcher{w})
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || strings.Contains(err.Error(), "remain") {
		t.Fatalf("shutdown = %v, want only the watcher's failure", err)
	}
	// The child shows that the whole session is skipped (#1052).
	if !alive(t, orphan) || !alive(t, child) {
		t.Fatal("shutdown signalled the unrelated session")
	}
}

// Codex on #1042: the PID freed by a reaped watcher whose flag is not yet
// set can go to the next watcher supervise starts, which is this process's
// child and its own session leader. Paused after the reap, the first
// watcher's record is given the second watcher's PID; the first must count
// as exited and its failure be reported, and the second, still running,
// stopped by the signals without a failure.
func TestShutdownReportsAWatcherReusedByALaterWatcher(t *testing.T) {
	failing := exec.Command("/bin/sh", "-c", "exit 7")
	paused, resume := make(chan struct{}), make(chan struct{})
	original := afterReap
	t.Cleanup(func() { afterReap = original })
	afterReap = func(w watcher) {
		if w.cmd == failing {
			close(paused)
			<-resume
		}
	}
	first, err := start(failing)
	if err != nil {
		t.Fatal(err)
	}
	<-paused
	second, err := start(exec.Command("/bin/sh", "-c", "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	first.pid = second.pid
	setBeforeSignals(t, func() { close(resume) })
	var exit *exec.ExitError
	err = shutdown([]watcher{first, second})
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || strings.Contains(err.Error(), "remain") || strings.Contains(err.Error(), "signal") {
		t.Fatalf("shutdown = %v, want only the first watcher's failure", err)
	}
	if alive(t, second.pid) {
		t.Fatal("the later watcher survived shutdown")
	}
}

// Argon on #1042 (#1052): once another session leader holds a reaped
// watcher's PID, nothing in that session is ours, since the kernel never hands
// out a PID still in use as a session ID. The leader and its child both
// survive, and nothing is counted as remaining.
func TestShutdownSkipsAReusedSession(t *testing.T) {
	failing := exec.Command("/bin/sh", "-c", "exit 7")
	release := make(chan struct{})
	reaped := holdDelivery(t, failing, release)
	w, err := start(failing)
	if err != nil {
		t.Fatal(err)
	}
	<-reaped
	file := filepath.Join(t.TempDir(), "child.pid")
	stranger := exec.Command("/bin/sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "stranger", file)
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	child := 0
	t.Cleanup(func() {
		if child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
		_ = stranger.Process.Kill()
		_ = stranger.Wait()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waitFor(t, ctx, func() bool {
		data, _ := os.ReadFile(file)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child > 0
	})
	w.pid = stranger.Process.Pid
	setBeforeSignals(t, func() { close(release) })
	var exit *exec.ExitError
	err = shutdown([]watcher{w})
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || strings.Contains(err.Error(), "remain") {
		t.Fatalf("shutdown = %v, want only the reaped watcher's failure", err)
	}
	if !alive(t, stranger.Process.Pid) || !alive(t, child) {
		t.Fatal("shutdown signalled the unrelated session")
	}
}

// Argon on #1042 (#1052): a watcher stopped by the first signal is reaped
// during the loop, and once another process holds its PID its session is no
// longer ours, so shutdown ends at once instead of at the deadline. The record
// carries a stranger's PID, listed as this process's child until the watcher
// is reaped: it stands for the leader before the reap and the new holder after.
func TestShutdownSkipsASessionReusedDuringTheSignals(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	w, err := start(exec.Command("/bin/sh", "-c", `trap 'exit 0' TERM; : > "$1"; while :; do sleep 0.01; done`, "watcher", ready))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waitFor(t, ctx, func() bool { _, err := os.Stat(ready); return err == nil })
	stranger := exec.Command("/bin/sh", "-c", "exec sleep 30")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stranger.Process.Kill(); _ = stranger.Wait() })
	held, self := strconv.Itoa(stranger.Process.Pid), strconv.Itoa(os.Getpid())
	original := listProcesses
	t.Cleanup(func() { listProcesses = original })
	listProcesses = func() (string, error) {
		out, err := original()
		if err != nil || w.reaped.Load() {
			return out, err
		}
		lines := strings.Split(out, "\n")
		for i, line := range lines {
			if fields := strings.Fields(line); len(fields) == 3 && fields[0] == held {
				lines[i] = held + " " + self + " " + fields[2]
			}
		}
		return strings.Join(lines, "\n"), nil
	}
	w.pid = stranger.Process.Pid
	begin := time.Now()
	if err := shutdown([]watcher{w}); err != nil {
		t.Fatalf("shutdown = %v, want no failure", err)
	}
	if elapsed := time.Since(begin); elapsed >= 1500*time.Millisecond {
		t.Fatalf("shutdown took %v, want under half of its deadline", elapsed)
	}
	if !w.reaped.Load() {
		t.Fatal("the watcher was not reaped during shutdown")
	}
	if !alive(t, stranger.Process.Pid) {
		t.Fatal("shutdown signalled the process holding the reused PID")
	}
}

// Codex on #1052: a watcher that a signal from elsewhere kills and that is
// reaped while the first table is read has exited before shutdown's signals,
// so its failure is reported, even when its line in the table is stale and a
// new session leader holds its PID. The record carries a stranger's PID.
func TestShutdownReportsAWatcherReapedDuringTheFirstScan(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exec sleep 30")
	w, err := start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	stranger := exec.Command("/bin/sh", "-c", "exec sleep 30")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stranger.Process.Kill(); _ = stranger.Wait() })
	original := listProcesses
	t.Cleanup(func() { listProcesses = original })
	first := true
	listProcesses = func() (string, error) {
		out, err := original()
		if first {
			first = false
			_ = cmd.Process.Kill()
			for !w.reaped.Load() {
				time.Sleep(time.Millisecond)
			}
		}
		return out, err
	}
	w.pid = stranger.Process.Pid
	if err := shutdown([]watcher{w}); err == nil || !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("shutdown = %v, want the watcher killed before the signals", err)
	}
	if !alive(t, stranger.Process.Pid) {
		t.Fatal("shutdown signalled the process holding the reused PID")
	}
}

// Argon on #1055: a watcher can be reaped and its PID taken by another
// session leader after a scan's flags and table are read but before that
// scan's checks, so the table's line for it is stale. The record carries a
// stranger's PID, listed as this process's child until the watcher is reaped;
// the watcher ignores SIGTERM and is killed inside the second scan's table
// read, which returns the listing taken before the kill. The stranger's child,
// listed in that table, must not be signalled.
func TestShutdownSkipsASessionReusedWithinAScan(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command("/bin/sh", "-c", `trap '' TERM; : > "$1"; while :; do sleep 0.01; done`, "watcher", ready)
	w, err := start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waitFor(t, ctx, func() bool { _, err := os.Stat(ready); return err == nil })
	file := filepath.Join(t.TempDir(), "child.pid")
	stranger := exec.Command("/bin/sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "stranger", file)
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	child := 0
	t.Cleanup(func() {
		if child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
		_ = stranger.Process.Kill()
		_ = stranger.Wait()
	})
	waitFor(t, ctx, func() bool {
		data, _ := os.ReadFile(file)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child > 0
	})
	held, self := strconv.Itoa(stranger.Process.Pid), strconv.Itoa(os.Getpid())
	original := listProcesses
	t.Cleanup(func() { listProcesses = original })
	calls := 0
	listProcesses = func() (string, error) {
		calls++
		out, err := original()
		if err != nil || w.reaped.Load() {
			return out, err
		}
		lines := strings.Split(out, "\n")
		for i, line := range lines {
			if fields := strings.Fields(line); len(fields) == 3 && fields[0] == held {
				lines[i] = held + " " + self + " " + fields[2]
			}
		}
		if calls == 2 {
			_ = cmd.Process.Kill()
			for !w.reaped.Load() {
				time.Sleep(time.Millisecond)
			}
		}
		return strings.Join(lines, "\n"), nil
	}
	w.pid = stranger.Process.Pid
	if err := shutdown([]watcher{w}); err != nil {
		t.Fatalf("shutdown = %v, want no failure", err)
	}
	if calls < 2 {
		t.Fatal("shutdown did not scan during the signals")
	}
	if !alive(t, stranger.Process.Pid) || !alive(t, child) {
		t.Fatal("shutdown signalled the unrelated session")
	}
}

func helper(role, root string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "DEV_HELPER="+role, "DEV_ROOT="+root)
	return cmd
}

func alive(t *testing.T, pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil && len(out) == 0 {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("ps unavailable: %v", err)
		}
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.HasPrefix(state, "Z")
}

// TestHelperProcess makes descendants real processes, including a separate
// group like templ's --cmd, with ignored signals to exercise escalation.
func TestHelperProcess(t *testing.T) {
	role, root := os.Getenv("DEV_HELPER"), os.Getenv("DEV_ROOT")
	if role == "" {
		return
	}
	if role == "orphan" {
		// A new session leader whose parent exits at once, so it is nobody's
		// child here, with a child of its own in its session.
		child := exec.Command("/bin/sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "orphan", filepath.Join(root, "orphan-child.pid"))
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "orphan.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	if role == "supervisor" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		commands := []*exec.Cmd{helper("css", root), helper("bundles", root), helper("templ", root)}
		if os.Getenv("DEV_EXIT") == "start" {
			commands[1] = exec.Command(filepath.Join(root, "missing"))
		}
		if err := supervise(ctx, commands); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	signal.Ignore(os.Interrupt, syscall.SIGTERM)
	if role != "leader" && !strings.HasSuffix(role, "-child-child") {
		child := helper(role+"-child", root)
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = child.Wait() }()
	}
	if err := os.WriteFile(filepath.Join(root, role+".pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		if role == os.Getenv("DEV_EXIT") {
			if _, err := os.Stat(filepath.Join(root, "exit")); err == nil {
				if os.Getenv("DEV_FAILURE") == "true" {
					os.Exit(7)
				}
				os.Exit(0)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFor(t *testing.T, ctx context.Context, ready func() bool) {
	t.Helper()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for child readiness or cleanup")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
