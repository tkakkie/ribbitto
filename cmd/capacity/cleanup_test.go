package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestCleanup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scans     [][]int
		fail      bool
		confirmed bool
		killed    []int
	}{
		{"rescans", [][]int{{101}, {102}, {}}, false, true, []int{101, 102}},
		{"reaped leader", [][]int{{}}, false, true, nil},
		{"ps failure after reap", nil, true, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &worker{pid: 41, exited: true}
			var killed []int
			c := cleanupOps{
				scan: func(session int) ([]int, error) {
					if session != 41 {
						t.Fatalf("session = %d", session)
					}
					if tc.fail {
						return nil, fmt.Errorf("ps failed")
					}
					if len(tc.scans) == 0 {
						t.Fatal("too many scans")
					}
					scan := tc.scans[0]
					tc.scans = tc.scans[1:]
					return scan, nil
				},
				killGroup:  func(int) { t.Fatal("killed reaped leader's group") },
				killMember: func(pid int, _ *worker) { killed = append(killed, pid) },
				// Nobody holds the reaped leader's PID, so its orphans are killed.
				held: func(int) bool { return false },
			}
			if got := c.stop(w); got != tc.confirmed || !reflect.DeepEqual(killed, tc.killed) {
				t.Fatalf("stop = %v, killed %v", got, killed)
			}
		})
	}
}

// The kernel never hands out a PID still in use as a session ID, so once the
// reaped leader's PID is held again nothing in that session is ours: neither
// the holder nor another member is signalled or counted (#1053). The PID is
// taken during the first scan, so the check after it is what skips them.
func TestCleanupReusedLeaderPID(t *testing.T) {
	w := &worker{pid: 41, exited: true}
	scans := [][]int{{41, 101}, {41}}
	scanned := false
	var killed []int
	c := cleanupOps{
		scan: func(session int) ([]int, error) {
			if session != w.pid {
				t.Fatalf("session = %d", session)
			}
			if len(scans) == 0 {
				return nil, fmt.Errorf("too many scans")
			}
			scanned = true
			scan := scans[0]
			scans = scans[1:]
			return scan, nil
		},
		killGroup:  func(int) { t.Fatal("killed reaped leader's group") },
		killMember: func(pid int, _ *worker) { killed = append(killed, pid) },
		held:       func(pid int) bool { return pid == w.pid && scanned },
	}
	if confirmed := c.stop(w); !confirmed || len(killed) != 0 || len(scans) != 1 {
		t.Fatalf("confirmed = %v, killed %v, remaining scans = %d", confirmed, killed, len(scans))
	}
}

// The maintainer on #1059: a reaped leader's PID held again confirms the
// cleanup even when the scan fails; with nobody holding it, a failing scan
// leaves the cleanup unconfirmed (TestCleanup's "ps failure after reap").
func TestCleanupReusedLeaderPIDScanFails(t *testing.T) {
	w := &worker{pid: 41, exited: true}
	c := cleanupOps{
		scan:       func(int) ([]int, error) { return nil, fmt.Errorf("ps failed") },
		killGroup:  func(int) { t.Fatal("killed reaped leader's group") },
		killMember: func(pid int, _ *worker) { t.Fatalf("signalled %d", pid) },
		held:       func(pid int) bool { return pid == w.pid },
	}
	if !c.stop(w) {
		t.Fatal("cleanup of a reused session was not confirmed after a failed scan")
	}
}

// #1053: a reaped leader's PID taken by an unrelated session leader with a
// child; the system cleanup leaves both alone.
func TestCleanupSkipsAReusedSession(t *testing.T) {
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
	waitFile(t, file)
	for child == 0 {
		data, _ := os.ReadFile(file)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	w := &worker{pid: stranger.Process.Pid, exited: true}
	if !systemCleanup().stop(w) {
		t.Fatal("cleanup of a reused session was not confirmed")
	}
	if !alive(t, stranger.Process.Pid) || !alive(t, child) {
		t.Fatal("cleanup signalled the unrelated session")
	}
}

// PID 1 belongs to another user unless the tests run as root, so a signal
// check of it fails with EPERM: still a held PID.
func TestPIDHeld(t *testing.T) {
	if !pidHeld(1) || !pidHeld(os.Getpid()) {
		t.Fatal("a running process's PID counted as free")
	}
}

func TestCleanupFallback(t *testing.T) {
	o, _ := fixture(t)
	t.Setenv("CAPACITY_MODE", "signal")
	// A direct sleeper has no detached children, so fallback cannot leak them.
	t.Setenv("CAPACITY_HELPER", "sleep")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.StartProcess(executable, []string{executable, "-test.run=^TestHelperProcess$"}, &os.ProcAttr{Dir: o.roots[0], Env: os.Environ(), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}, Sys: &syscall.SysProcAttr{Setsid: true}})
	if err != nil {
		t.Fatal(err)
	}
	w := &worker{process: process, pid: process.Pid}
	defer func() { _ = process.Kill(); _, _ = process.Wait() }()
	waitFile(t, filepath.Join(o.roots[0], "ready-"+strconv.Itoa(w.pid)))
	c := systemCleanup()
	c.scan = func(int) ([]int, error) { return nil, context.DeadlineExceeded }
	if c.stop(w) || !w.exited {
		t.Fatal("fallback must reap the group but leave cleanup unconfirmed")
	}
}

func TestSessionMembers(t *testing.T) {
	listing := fmt.Sprintf(" 41 Ss\n42 Z+\n43 R\n44 S\n45 S\n%d S\n", os.Getpid())
	got := members(listing, 41, func(pid int) (int, error) {
		if pid == 45 {
			return 0, syscall.ESRCH
		}
		if pid == 44 {
			return 44, nil
		}
		return 41, nil
	})
	if !reflect.DeepEqual(got, []int{41, 43}) {
		t.Fatalf("members = %v", got)
	}
}

func TestDescendantCleanup(t *testing.T) {
	for _, mode := range []string{"descendants", "early-first", "early-second"} {
		t.Run(mode, func(t *testing.T) {
			o, helper := fixture(t)
			if mode == "descendants" {
				t.Setenv("CAPACITY_MODE", mode)
			} else {
				t.Setenv("CAPACITY_MODE", "early")
				fast := 0
				if mode == "early-second" {
					fast = 1
				}
				t.Setenv("CAPACITY_FAST", o.roots[fast])
			}
			defer removeChildren(t, o)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			if _, err := run(ctx, o, helper, io.Discard, systemCleanup()); err != nil {
				t.Fatal(err)
			}
			if mode == "descendants" {
				for _, pid := range recordedPIDs(t, o) {
					if alive(t, pid) {
						t.Errorf("descendant %d survived", pid)
					}
				}
			} else {
				for _, root := range o.roots {
					if data, err := os.ReadFile(filepath.Join(root, "verdict")); err == nil && string(data) == "early" {
						return
					}
				}
				t.Fatal("no early cleanup verdict")
			}
		})
	}
}

func TestSignalCleanup(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			o, helper := fixture(t)
			t.Setenv("CAPACITY_MODE", "signal")
			base := filepath.Dir(helper)
			if err := os.Mkdir(filepath.Join(base, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(helper, filepath.Join(base, "bin/ai-db")); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHelperProcess$", "--", o.roots[0], o.roots[1], "2", "3")
			cmd.Dir, cmd.Env = base, append(os.Environ(), "CAPACITY_HELPER=runner")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); removeChildren(t, o) }()
			for _, root := range o.roots {
				waitFile(t, filepath.Join(root, "pids"))
			}
			pids := recordedPIDs(t, o)
			if len(pids) != 8 {
				t.Fatalf("pids = %v", pids)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 128+int(sig) {
				t.Fatalf("signal exit = %v; %s", err, stderr.String())
			}
			for _, pid := range pids {
				if alive(t, pid) {
					t.Errorf("process %d survived %s", pid, sig)
				}
			}
		})
	}
}

func recordedPIDs(t *testing.T, o options) []int {
	t.Helper()
	var pids []int
	seen := map[int]bool{}
	for _, root := range o.roots {
		children, err := filepath.Glob(filepath.Join(root, "child-*"))
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{filepath.Join(root, "pids"), filepath.Join(root, "left"), filepath.Join(root, "spawned")}
		for _, path := range append(paths, children...) {
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(value)
				if err != nil {
					t.Fatal(err)
				}
				if seen[pid] {
					continue
				}
				seen[pid] = true
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func removeChildren(t *testing.T, o options) {
	t.Helper()
	for _, pid := range recordedPIDs(t, o) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestObserverSampling(t *testing.T) {
	pgtest.AdminURL(t)
	helper, err := filepath.Abs("../../bin/ai-db")
	if err != nil {
		t.Fatal(err)
	}
	// Other packages' tests share the server and open and close connections
	// meanwhile, so one attempt can see the second observer offset by a
	// connection closing elsewhere. The assertion stays exact; a broken
	// sampler fails every attempt.
	var last string
	for attempt := 0; attempt < 5; attempt++ {
		peak, baseline, err := sampleWithSecondObserver(helper)
		if err != nil {
			t.Fatal(err)
		}
		if peak >= baseline+1 {
			return
		}
		last = fmt.Sprintf("peak = %d, baseline %d", peak, baseline)
	}
	t.Fatalf("the observer never sampled the second connection in 5 attempts; last %s", last)
}

func sampleWithSecondObserver(helper string) (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := startObserver(ctx, helper)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = first.stdin.Close(); cancel(); <-first.done }()
	var baseline struct {
		Baseline int `json:"baseline"`
	}
	if err := json.Unmarshal([]byte(<-first.lines), &baseline); err != nil || baseline.Baseline <= 0 {
		return 0, 0, fmt.Errorf("baseline = %+v, %v", baseline, err)
	}
	second, err := startObserver(ctx, helper)
	if err != nil {
		return 0, 0, err
	}
	if line := <-second.lines; line == "" {
		return 0, 0, fmt.Errorf("second observer did not connect")
	}
	time.Sleep(500 * time.Millisecond)
	_ = second.stdin.Close()
	<-second.done
	_ = first.stdin.Close()
	peak, err := strconv.Atoi(<-first.lines)
	<-first.done
	if err != nil || first.err != nil || second.err != nil {
		return 0, 0, fmt.Errorf("parse %v, observers %v / %v", err, first.err, second.err)
	}
	return peak, baseline.Baseline, nil
}
