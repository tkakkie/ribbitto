package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestImplementRunWorktreeLock(t *testing.T) {
	for _, row := range []string{
		"held",     // Delete worktree lock acquisition before execute.
		"released", // Delete the deferred worktree descriptor close.
		"status",   // Release the lock before status delivery.
	} {
		t.Run(row, func(t *testing.T) {
			x := newImplementRun(t)
			if row == "held" {
				lock, err := implementRunTreeLock(x.f.tree)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Close() }()
				// As with other pre-write refusals, seed the persistent home lock.
				homeLock, err := implementRunLock(x.f.home)
				if err != nil {
					t.Fatal(err)
				}
				_ = homeLock.Close()
				implementRunRefusedUnchanged(t, x, "acquiring worktree lock")
				return
			}
			out := io.Writer(io.Discard)
			if row == "status" {
				out = implementTreeLockedWriter{t: t, tree: x.f.tree}
			}
			var stderr bytes.Buffer
			if code := implementRunCommand(x.args, out, &stderr, x.deps); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, &stderr)
			}
			lock, err := implementRunTreeLock(x.f.tree)
			if err != nil {
				t.Fatalf("worktree lock not released: %v", err)
			}
			_ = lock.Close()
		})
	}
}

type implementTreeLockedWriter struct {
	t    *testing.T
	tree string
}

func (w implementTreeLockedWriter) Write(p []byte) (int, error) {
	lock, err := implementRunTreeLock(w.tree)
	if err == nil {
		_ = lock.Close()
		w.t.Error("worktree lock released before status")
	}
	return len(p), nil
}

func TestImplementRunWorktreeLockNotInherited(t *testing.T) {
	x := newImplementRun(t)
	lock, err := implementRunTreeLock(x.f.tree)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	// Delete O_CLOEXEC: a child must never prolong the supervisor's lock.
	flags, err := unix.FcntlInt(lock.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("worktree descriptor can survive exec: %d %v", flags, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
: > '%s/ready'
while [ ! -f '%s/release' ]; do /bin/sleep 0.01; done
export ARGONREVIEW_LOCK_CHILD=1 ARGONREVIEW_LOCK_DIR='%s'
exec '%s' -test.run=^TestSweepLockChildHelper$
`, x.fake, x.fake, x.f.tree, exe)
	write(t, filepath.Join(x.fake, "agy"), script)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(x.fake, "agy"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	waitFor(t, filepath.Join(x.fake, "ready"))
	_ = lock.Close()
	write(t, filepath.Join(x.fake, "release"), "")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("fake agy inherited worktree lock: %v", err)
	}
}

func TestImplementRunConcurrentWorktrees(t *testing.T) {
	a, b, other := newImplementRun(t), newImplementRun(t), newImplementRun(t)
	b.set("worktree", a.f.tree)
	otherTree := a.f.tree + "-other"
	if err := os.Rename(other.f.tree, otherTree); err != nil {
		t.Fatal(err)
	}
	other.f.tree = otherTree
	other.set("worktree", otherTree)
	write(t, filepath.Join(a.fake, "mutate"), "while [ ! -f '"+a.fake+"/release' ]; do /bin/sleep 0.01; done\n")
	a.set("timeout", "10s")
	done := make(chan int, 1)
	var out, stderr bytes.Buffer
	go func() { done <- implementRunCommand(a.args, &out, &stderr, a.deps) }()
	defer write(t, filepath.Join(a.fake, "release"), "")
	waitFor(t, filepath.Join(a.fake, "args"))
	// Delete the directory flock: a different home would admit the same tree.
	homeLock, err := implementRunLock(b.f.home)
	if err != nil {
		t.Fatal(err)
	}
	_ = homeLock.Close()
	implementRunRefusedUnchanged(t, b, "acquiring worktree lock")
	// Lock a shared ancestor instead: an unrelated worktree would be refused.
	other.run(t, 0)
	write(t, filepath.Join(a.fake, "release"), "")
	if code := <-done; code != 0 {
		t.Fatalf("first worker exit=%d stderr=%s status=%s", code, &stderr, &out)
	}
}

func TestImplementRunWorkersIsolation(t *testing.T) {
	for _, row := range []struct {
		name string
		real bool
	}{
		{"fixture", false},     // Redirect A's cleanup or recovery checks to B.
		{"real-session", true}, // Stop a peer session while cleaning up A.
	} {
		t.Run(row.name, func(t *testing.T) { implementRunWorkersIsolation(t, row.real) })
	}
}

func implementRunWorkersIsolation(t *testing.T, realSession bool) {
	t.Helper()
	a, b := newImplementRun(t), newImplementRun(t)
	for _, pair := range [][2]*implementRunFixture{{a, b}, {b, a}} {
		if realSession {
			pair[0].deps.ops = systemCleanup()
		}
		pair[0].args = append(pair[0].args, "--peer-home", pair[1].f.home)
	}
	a.set("timeout", "2s")
	b.set("timeout", "20s")
	write(t, filepath.Join(a.fake, "mutate"), "exec sleep 30\n")
	write(t, filepath.Join(b.fake, "mutate"), "touch '"+b.fake+"/ready'\nwhile [ ! -f '"+b.fake+"/release' ]; do /bin/sleep 0.01; done\n")
	var aout, aerr, bout, berr bytes.Buffer
	adone, bdone := make(chan int, 1), make(chan int, 1)
	go func() { adone <- implementRunCommand(a.args, &aout, &aerr, a.deps) }()
	waitFor(t, filepath.Join(a.fake, "args"))
	go func() { bdone <- implementRunCommand(b.args, &bout, &berr, b.deps) }()
	defer write(t, filepath.Join(b.fake, "release"), "")
	waitFor(t, filepath.Join(b.fake, "ready"))
	tmpBytes, err := os.ReadFile(filepath.Join(b.fake, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := strings.TrimSpace(string(tmpBytes))
	// Redirect cleanup to a peer: home files, marker, lock, logs and TMPDIR
	// must all retain their bytes and identities while B remains active.
	snapshots := make(map[string]map[string]string)
	identities := make(map[string]os.FileInfo)
	for _, path := range []string{b.f.home, b.logs, tmp} {
		snapshots[path] = implementRunSnapshot(t, path)
		for file := range snapshots[path] {
			identities[file], err = os.Lstat(file)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if code := <-adone; code != 124 || !strings.Contains(aout.String(), "counts=false") {
		t.Fatalf("A timeout exit=%d status=%s stderr=%s", code, &aout, &aerr)
	}
	if !strings.Contains(aout.String(), "stop-confirmed=true") || !strings.Contains(aout.String(), "cleanup-remove-tmp=true") {
		t.Errorf("A did not complete timeout cleanup: %s %s", &aout, &aerr)
	}
	for path, before := range snapshots {
		if !reflect.DeepEqual(before, implementRunSnapshot(t, path)) {
			t.Errorf("A changed B's files under %s", path)
		}
	}
	for path, before := range identities {
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(before, after) {
			t.Errorf("A replaced B's file %s: %v", path, err)
		}
	}
	if lock, err := implementRunLock(b.f.home); err == nil {
		_ = lock.Close()
		t.Error("A released B's home lock")
	}
	write(t, filepath.Join(b.fake, "release"), "")
	if code := <-bdone; code != 0 || !strings.Contains(bout.String(), "counts=true") {
		t.Fatalf("B exit=%d status=%s stderr=%s", code, &bout, &berr)
	}
	// Consult a peer's recovery marker: A's failed run must not block B.
	if _, err := os.Stat(filepath.Join(a.f.home, "guard/implement-run.recovery-needed")); err != nil {
		t.Fatalf("A did not leave its recovery marker: %v", err)
	}
	implementCheckRemove(t, b.logs)
	implementCheckRemove(t, filepath.Join(b.fake, "args"))
	b.order, b.checks = nil, 0
	b.run(t, 0)
	b.cleaned(t)
}

func TestImplementRunPeerRecoveryIndependent(t *testing.T) {
	a, b := newImplementRun(t), newImplementRun(t)
	marker := filepath.Join(a.f.home, "guard/implement-run.recovery-needed")
	write(t, marker, "recovery required")
	b.args = append(b.args, "--peer-home", a.f.home)
	// Consult a peer's marker instead of only this run's home marker.
	b.run(t, 0)
	if data, err := os.ReadFile(marker); err != nil || string(data) != "recovery required" {
		t.Fatalf("B changed A's marker: %q %v", data, err)
	}
}
