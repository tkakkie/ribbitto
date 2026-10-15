package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// result is what supervised observed; every PID the script recorded in
// $1/pids has been checked dead.
type result struct {
	code    int
	err     error
	elapsed time.Duration
	pids    []int
	dir     string
}

// supervised runs script under sh with a fresh directory as $1 and the given
// operations.
func supervised(t *testing.T, script string, limit, grace time.Duration, ops cleanupOps) result {
	t.Helper()
	r := result{dir: t.TempDir()}
	started := time.Now()
	r.code, r.err = supervisor{limit, grace, nil, ops}.run("/bin/sh", []string{"-c", script, "sh", r.dir}, os.Environ(), r.dir, os.Stdout, os.Stderr)
	r.elapsed = time.Since(started)
	r.pids = recorded(t, r.dir)
	return r
}

// recorded reads $1/pids once it is complete and checks that none of the
// PIDs is alive.
func recorded(t *testing.T, dir string) []int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
		if alive(t, pid) {
			t.Errorf("process %d survived", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return pids
}

// Descendants that ignore TERM, started with exec so $! is their PID: one in
// the leader's group, one in its own group in the same session.
const stubborn = `( trap "" TERM; exec sleep 30 ) & echo $! >> "$1/pids"; ` +
	`perl -e '$SIG{TERM} = "IGNORE"; setpgrp(0, 0); exec "sleep", "30"' & echo $! >> "$1/pids"; `

func TestSupervisorTimeLimit(t *testing.T) {
	script := `trap 'touch "$1/term"; exit 0' TERM; echo $$ >> "$1/pids"; ` + stubborn + `touch "$1/ready"; while :; do sleep 0.1; done`
	r := supervised(t, script, 300*time.Millisecond, 5*time.Second, systemCleanup())
	if r.code != 124 || r.err != nil || len(r.pids) != 3 {
		t.Fatalf("code = %d, err = %v, pids %v", r.code, r.err, r.pids)
	}
	// The leader gets TERM first and exits well within the grace period.
	if _, err := os.Stat(filepath.Join(r.dir, "term")); err != nil || r.elapsed > 3*time.Second {
		t.Fatalf("elapsed %v, TERM marker: %v", r.elapsed, err)
	}
}

func TestSupervisorKillsALeaderThatIgnoresTERM(t *testing.T) {
	script := `trap "" TERM; echo $$ >> "$1/pids"; touch "$1/ready"; while :; do sleep 0.1; done`
	r := supervised(t, script, 300*time.Millisecond, 700*time.Millisecond, systemCleanup())
	if r.code != 124 || r.err != nil || r.elapsed < time.Second {
		t.Fatalf("code = %d, err = %v after %v; want 124 after the grace period", r.code, r.err, r.elapsed)
	}
}

// INT and TERM sent to the launcher process itself reach the supervisor
// through withSignals; signalHelper is that launcher.
func TestSupervisorSignals(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(executable, "-test.run=^TestSignalHelper$")
			cmd.Env = append(os.Environ(), "ARGONREVIEW_SIGNAL_HELPER="+dir)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			waitFor(t, filepath.Join(dir, "ready"))
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); cmd.ProcessState.ExitCode() != tc.code {
				t.Fatalf("exit %v, want %d", err, tc.code)
			}
			if pids := recorded(t, dir); len(pids) != 3 {
				t.Fatalf("pids %v", pids)
			}
		})
	}
}

func TestSignalHelper(t *testing.T) {
	dir := os.Getenv("ARGONREVIEW_SIGNAL_HELPER")
	if dir == "" {
		t.Skip("run by TestSupervisorSignals")
	}
	script := `echo $$ >> "$1/pids"; ` + stubborn + `touch "$1/ready"; wait`
	code, err := withSignals(func(signals <-chan os.Signal) (int, error) {
		return supervisor{30 * time.Second, time.Second, signals, systemCleanup()}.run("/bin/sh", []string{"-c", script, "sh", dir}, os.Environ(), dir, os.Stdout, os.Stderr)
	})
	if err != nil {
		code = 1
	}
	os.Exit(code)
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", path)
		}
	}
}

// Descendants are killed after the leader exits on its own, and its status
// is kept.
func TestSupervisorKillsDescendantsAfterExit(t *testing.T) {
	for _, status := range []int{0, 3} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			r := supervised(t, `sleep 30 & echo $! >> "$1/pids"; `+stubborn+`exit `+strconv.Itoa(status), 30*time.Second, time.Second, systemCleanup())
			if r.code != status || r.err != nil || len(r.pids) != 3 {
				t.Fatalf("code = %d, err = %v, pids %v", r.code, r.err, r.pids)
			}
		})
	}
}

// agy gets /dev/null, never this process's stdin, so it cannot wait on it.
func TestSupervisorClosesStdin(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.WriteString("inherited\n")
	_ = writer.Close()
	stdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = stdin; _ = reader.Close() })
	r := supervised(t, `echo $$ > "$1/pids"; cat > "$1/stdin"`, 10*time.Second, time.Second, systemCleanup())
	if data, err := os.ReadFile(filepath.Join(r.dir, "stdin")); r.err != nil || err != nil || len(data) != 0 {
		t.Fatalf("agy read %q from stdin, %v, %v", data, err, r.err)
	}
}

// A failed scan leaves the cleanup unconfirmed, so run fails even after a
// status-0 exit; an unreaped leader is still killed and reaped.
func TestSupervisorReportsAFailedScan(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		code         int
	}{
		{"time limit", `echo $$ > "$1/pids"; exec sleep 30`, 124},
		{"status 0", `echo $$ > "$1/pids"`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := systemCleanup()
			ops.scan = func(int) ([]int, error) { return nil, fmt.Errorf("ps failed") }
			if r := supervised(t, tc.script, 200*time.Millisecond, 200*time.Millisecond, ops); r.code != tc.code || r.err == nil {
				t.Fatalf("code = %d, err = %v", r.code, r.err)
			}
		})
	}
}

// Once waiting fails, the leader's PID is not proven ours: nothing signals
// its group or scans its session, and run fails (Codex on #1089).
func TestSupervisorStopsSignallingWhenWaitingFails(t *testing.T) {
	ops := systemCleanup()
	ops.wait = func(int, *syscall.WaitStatus) (int, error) { return 0, syscall.ECHILD }
	ops.signalGroup = func(pid int, _ syscall.Signal) { t.Errorf("signalled group %d", pid) }
	ops.scan = func(pid int) ([]int, error) { t.Errorf("scanned session %d", pid); return nil, nil }
	dir := t.TempDir()
	code, err := supervisor{10 * time.Second, time.Second, nil, ops}.run("/bin/sh", []string{"-c", `echo $$ > "$1/pids"; exec sleep 30`, "sh", dir}, os.Environ(), dir, os.Stdout, os.Stderr)
	if code != 1 || err == nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	// The leader is really still this process's unreaped child.
	waitFor(t, filepath.Join(dir, "pids"))
	data, _ := os.ReadFile(filepath.Join(dir, "pids"))
	if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_, _ = syscall.Wait4(pid, nil, 0, nil)
	}
}

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
			c := &child{pid: 41, exited: true}
			var killed []int
			o := cleanupOps{
				scan: func(int) ([]int, error) {
					if tc.fail {
						return nil, fmt.Errorf("ps failed")
					}
					scan := tc.scans[0]
					tc.scans = tc.scans[1:]
					return scan, nil
				},
				signalGroup: func(int, syscall.Signal) { t.Fatal("signalled a reaped leader's group") },
				killMember:  func(pid int, _ *child) { killed = append(killed, pid) },
				held:        func(int) bool { return false },
			}
			if err := o.stop(c); (err == nil) != tc.confirmed || !reflect.DeepEqual(killed, tc.killed) {
				t.Fatalf("stop = %v, killed %v", err, killed)
			}
		})
	}
}

// Once another process holds the reaped leader's PID, nothing in that
// session is ours: nothing is signalled, even when it is taken during a scan
// or the scan fails (#1053, the maintainer on #1059).
func TestCleanupSkipsAReusedLeaderPID(t *testing.T) {
	for _, fail := range []bool{false, true} {
		c := &child{pid: 41, exited: true}
		scanned := false
		o := cleanupOps{
			scan: func(int) ([]int, error) {
				if fail {
					return nil, fmt.Errorf("ps failed")
				}
				scanned = true
				return []int{41, 101}, nil
			},
			signalGroup: func(int, syscall.Signal) { t.Fatal("signalled a reaped leader's group") },
			killMember:  func(pid int, _ *child) { t.Fatalf("signalled %d", pid) },
			held:        func(int) bool { return fail || scanned },
		}
		if err := o.stop(c); err != nil {
			t.Fatalf("fail = %v: cleanup of a reused session was not confirmed: %v", fail, err)
		}
	}
}

// A reaped leader's PID taken by an unrelated session leader with a child:
// the system cleanup leaves both alone.
func TestCleanupLeavesAReusedSessionAlone(t *testing.T) {
	dir := t.TempDir()
	stranger := exec.Command("/bin/sh", "-c", `sleep 30 & echo $! > "$1/child"; wait`, "sh", dir)
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-stranger.Process.Pid, syscall.SIGKILL); _ = stranger.Wait() })
	var data []byte
	for len(data) == 0 || data[len(data)-1] != '\n' {
		time.Sleep(10 * time.Millisecond)
		data, _ = os.ReadFile(filepath.Join(dir, "child"))
	}
	sleeper, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if err := systemCleanup().stop(&child{pid: stranger.Process.Pid, exited: true}); err != nil {
		t.Fatalf("cleanup of a reused session was not confirmed: %v", err)
	}
	if !alive(t, stranger.Process.Pid) || !alive(t, sleeper) {
		t.Fatal("cleanup signalled the unrelated session")
	}
}

func TestSessionMembers(t *testing.T) {
	listing := fmt.Sprintf(" 41 Ss\n42 Z+\n43 R\n44 S\n45 S\n%d S\n", os.Getpid())
	got := members(listing, 41, func(pid int) (int, error) {
		switch pid {
		case 44:
			return 44, nil
		case 45:
			return 0, syscall.ESRCH
		}
		return 41, nil
	})
	if !reflect.DeepEqual(got, []int{41, 43}) {
		t.Fatalf("members = %v", got)
	}
}

func alive(t *testing.T, pid int) bool {
	t.Helper()
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil && len(out) == 0 {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return false
		}
		t.Fatalf("ps unavailable: %v", err)
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.HasPrefix(state, "Z")
}
