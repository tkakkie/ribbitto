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

// supervised runs script under sh with a fresh directory as $1, sending sig
// once the script has created $1/ready, and returns the exit code, how long it
// took, the PIDs the script recorded in $1/pids, all checked dead, and $1.
func supervised(t *testing.T, script string, limit, grace time.Duration, sig os.Signal) (int, time.Duration, []int, string) {
	t.Helper()
	dir := t.TempDir()
	signals := make(chan os.Signal, 1)
	if sig != nil {
		go func() {
			for _, err := os.Stat(filepath.Join(dir, "ready")); err != nil; _, err = os.Stat(filepath.Join(dir, "ready")) {
				time.Sleep(10 * time.Millisecond)
			}
			signals <- sig
		}()
	}
	s := supervisor{limit, grace, signals, systemCleanup()}
	started := time.Now()
	code, err := s.run("/bin/sh", []string{"-c", script, "sh", dir}, os.Environ(), dir, os.Stdout, os.Stderr)
	elapsed := time.Since(started)
	data, readErr := os.ReadFile(filepath.Join(dir, "pids"))
	if err != nil || readErr != nil {
		t.Fatalf("run: %v; pids: %v", err, readErr)
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	for _, pid := range pids {
		if alive(t, pid) {
			t.Errorf("process %d survived", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return code, elapsed, pids, dir
}

// A descendant that ignores TERM, started with exec so $! is its PID.
const stubborn = `( trap "" TERM; exec sleep 30 ) & echo $! >> "$1/pids"; `

func TestSupervisorTimeLimit(t *testing.T) {
	script := `trap 'touch "$1/term"; exit 0' TERM; echo $$ >> "$1/pids"; ` + stubborn + `touch "$1/ready"; while :; do sleep 0.1; done`
	code, elapsed, pids, dir := supervised(t, script, 300*time.Millisecond, 5*time.Second, nil)
	if code != 124 || len(pids) != 2 {
		t.Fatalf("code = %d, pids %v", code, pids)
	}
	// The leader gets TERM first and exits well within the grace period.
	if _, err := os.Stat(filepath.Join(dir, "term")); err != nil || elapsed > 3*time.Second {
		t.Fatalf("elapsed %v, TERM marker: %v", elapsed, err)
	}
}

func TestSupervisorKillsALeaderThatIgnoresTERM(t *testing.T) {
	script := `trap "" TERM; echo $$ >> "$1/pids"; touch "$1/ready"; while :; do sleep 0.1; done`
	code, elapsed, _, _ := supervised(t, script, 300*time.Millisecond, 700*time.Millisecond, nil)
	if code != 124 || elapsed < time.Second {
		t.Fatalf("code = %d after %v; want 124 after the grace period", code, elapsed)
	}
}

func TestSupervisorSignals(t *testing.T) {
	for _, tc := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			script := `echo $$ >> "$1/pids"; ` + stubborn + `touch "$1/ready"; wait`
			if code, _, pids, _ := supervised(t, script, 30*time.Second, time.Second, tc.sig); code != tc.code || len(pids) != 2 {
				t.Fatalf("code = %d, pids %v", code, pids)
			}
		})
	}
}

// Descendants are killed after the leader exits on its own, and its status
// is kept.
func TestSupervisorKillsDescendantsAfterExit(t *testing.T) {
	for _, status := range []int{0, 3} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			script := `sleep 30 & echo $! >> "$1/pids"; ` + stubborn + `exit ` + strconv.Itoa(status)
			if code, _, pids, _ := supervised(t, script, 30*time.Second, time.Second, nil); code != status || len(pids) != 2 {
				t.Fatalf("code = %d, pids %v", code, pids)
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
	_, _, _, dir := supervised(t, `echo $$ > "$1/pids"; cat > "$1/stdin"`, 10*time.Second, time.Second, nil)
	if data, err := os.ReadFile(filepath.Join(dir, "stdin")); err != nil || len(data) != 0 {
		t.Fatalf("agy read %q from stdin, %v", data, err)
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
				killGroup:  func(int) { t.Fatal("killed a reaped leader's group") },
				killMember: func(pid int, _ *child) { killed = append(killed, pid) },
				held:       func(int) bool { return false },
			}
			if got := o.stop(c); got != tc.confirmed || !reflect.DeepEqual(killed, tc.killed) {
				t.Fatalf("stop = %v, killed %v", got, killed)
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
			killGroup:  func(int) { t.Fatal("killed a reaped leader's group") },
			killMember: func(pid int, _ *child) { t.Fatalf("signalled %d", pid) },
			held:       func(int) bool { return fail || scanned },
		}
		if !o.stop(c) {
			t.Fatalf("fail = %v: cleanup of a reused session was not confirmed", fail)
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
	if !systemCleanup().stop(&child{pid: stranger.Process.Pid, exited: true}) {
		t.Fatal("cleanup of a reused session was not confirmed")
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
