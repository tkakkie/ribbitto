package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

func fixture(t *testing.T) (options, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := options{packages: 2, parallel: 3}
	for i, name := range []string{"first", "second"} {
		o.roots[i] = filepath.Join(base, name)
		if err := os.Mkdir(o.roots[i], 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(o.roots[i], "Makefile"), "$(GO_TEST_FLAGS)\n")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"make", "observer"} {
		write(t, filepath.Join(base, role), "#!/bin/sh\nCAPACITY_HELPER="+role+" exec '"+strings.ReplaceAll(executable, "'", "'\\''")+"' -test.run=^TestHelperProcess$ -- \"$@\"\n")
	}
	t.Setenv("PATH", base+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPACITY_EVENT", filepath.Join(base, "handshake"))
	t.Setenv("GOMODCACHE", "inherited-module-cache")
	t.Setenv("GOPATH", "inherited-gopath")
	return o, filepath.Join(base, "observer")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path+".tmp", []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	o, _ := fixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(o.roots[0], alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"valid", []string{o.roots[0], o.roots[1], "2", "3"}, true},
		{"missing", nil, false},
		{"extra", []string{o.roots[0], o.roots[1], "2", "3", "extra"}, false},
		{"same", []string{o.roots[0], alias, "2", "3"}, false},
		{"zero", []string{o.roots[0], o.roots[1], "0", "3"}, false},
		{"negative", []string{o.roots[0], o.roots[1], "2", "-1"}, false},
		{"not integer", []string{o.roots[0], o.roots[1], "x", "3"}, false},
		{"parallel not integer", []string{o.roots[0], o.roots[1], "2", "x"}, false},
		{"missing root", []string{"missing", o.roots[1], "2", "3"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parse(tc.args)
			if (err == nil) != tc.valid {
				t.Fatalf("parse = %v, %v", got, err)
			}
			if tc.valid && got != o {
				t.Fatalf("parse = %+v, want %+v", got, o)
			}
		})
	}
	write(t, filepath.Join(o.roots[1], "Makefile"), "check:\n")
	if _, err := parse([]string{o.roots[0], o.roots[1], "2", "3"}); err == nil {
		t.Fatal("accepted fixed test flags")
	}
}

func TestReport(t *testing.T) {
	o, helper := fixture(t)
	var out bytes.Buffer
	cleanup := systemCleanup()
	// These checks have no descendants; exercise reporting without requiring ps.
	cleanup.scan = func(int) ([]int, error) { return nil, nil }
	code, err := run(context.Background(), o, helper, &out, cleanup)
	if err != nil || code != 1 {
		t.Fatalf("run = %d, %v", code, err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"exit_codes": []any{float64(0), float64(7)}, "too_many_clients_lines": []any{float64(2), float64(2)},
		"pgx_pool_max_conns": float64(pgtest.MaxConns), "packages": float64(2), "parallel": float64(3),
		"count": float64(1), "require_db": float64(1), "baseline": float64(1), "peak_connections": float64(2),
		"max_connections": float64(100), "migration_pool_max_open": float64(0), "admin_connections_per_pgtest_call": float64(1),
	} {
		if !reflect.DeepEqual(report[key], want) {
			t.Errorf("%s = %v, want %v", key, report[key], want)
		}
	}
	if strings.Contains(out.String(), "postgres://") || strings.Contains(out.String(), "credential-canary") {
		t.Fatal("report exposed credentials")
	}
	for _, root := range o.roots {
		logs, err := filepath.Glob(filepath.Join(root, "bin/ai-capacity-*.log"))
		if err != nil || len(logs) != 1 {
			t.Fatalf("logs = %v, %v", logs, err)
		}
		info, err := os.Stat(logs[0])
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("log permissions = %v", info.Mode())
		}
	}
}

func TestUnconfirmedCleanup(t *testing.T) {
	o, helper := fixture(t)
	c := systemCleanup()
	c.scan = func(int) ([]int, error) { return nil, fmt.Errorf("ps failed") }
	if _, err := run(context.Background(), o, helper, io.Discard, c); err == nil || !strings.Contains(err.Error(), "could not confirm") {
		t.Fatalf("run = %v", err)
	}
}

func TestUsage(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestHelperProcess$", "--")
	cmd.Env = append(os.Environ(), "CAPACITY_HELPER=runner")
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || !strings.Contains(string(out), "usage: capacity WT1 WT2 PACKAGES PARALLEL") {
		t.Fatalf("bad arguments = %v, %s", err, out)
	}
}

func TestMatchingLines(t *testing.T) {
	for _, tc := range []struct {
		text  string
		count int
	}{
		{"", 0}, {"53300 too many clients\nTOO MANY CLIENTS\n", 2},
		{strings.Repeat("x", 100000) + "53300", 1}, {"ordinary\n", 0},
	} {
		path := filepath.Join(t.TempDir(), "log")
		write(t, path, tc.text)
		if got, err := matchingLines(path); err != nil || got != tc.count {
			t.Fatalf("count = %d, %v; want %d", got, err, tc.count)
		}
	}
}

// TestHelperProcess makes test children independent of Python and real make check.
func TestHelperProcess(t *testing.T) {
	role := os.Getenv("CAPACITY_HELPER")
	if role == "" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if role == "runner" {
		os.Args = append([]string{"capacity"}, args...)
		main()
	}
	if role == "observer" {
		if !reflect.DeepEqual(args, []string{"--observe"}) {
			os.Exit(9)
		}
		// A delayed handshake catches a runner that starts checks before reading it.
		time.Sleep(100 * time.Millisecond)
		for _, root := range []string{"first", "second"} {
			if _, err := os.Stat(filepath.Join(filepath.Dir(os.Getenv("CAPACITY_EVENT")), root, "bin")); err == nil {
				t.Fatal("check started before observer handshake")
			}
		}
		write(t, os.Getenv("CAPACITY_EVENT"), "connected")
		fmt.Println(`{"max_connections":100,"baseline":1}`)
		_, _ = io.Copy(io.Discard, os.Stdin)
		fmt.Println(2)
		os.Exit(0)
	}
	if role == "sleep" || role == "detached" {
		if role == "detached" {
			child := spawn(t, "sleep", false)
			write(t, "spawned", strconv.Itoa(child.Process.Pid))
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		write(t, "ready-"+strconv.Itoa(os.Getpid()), "ready")
		time.Sleep(120 * time.Second)
		os.Exit(0)
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(os.Getenv("CAPACITY_EVENT")); err != nil {
		t.Fatal("check preceded handshake")
	}
	if !reflect.DeepEqual(args, []string{"check", "GO_TEST_FLAGS=-count=1 -p 2 -parallel 3"}) {
		t.Fatalf("args = %v", args)
	}
	for key, want := range map[string]string{
		"GOMODCACHE": "inherited-module-cache", "GOPATH": "inherited-gopath", "RIBBITTO_REQUIRE_DB": "1",
		"GOCACHE": filepath.Join(root, "bin/.cache/go-build"), "GOLANGCI_LINT_CACHE": filepath.Join(root, "bin/.cache/lint"),
	} {
		if os.Getenv(key) != want {
			t.Fatalf("%s = %q", key, os.Getenv(key))
		}
	}
	switch os.Getenv("CAPACITY_MODE") {
	case "descendants", "signal":
		child, detached := spawn(t, "sleep", false), spawn(t, "detached", true)
		waitFile(t, "spawned")
		data, err := os.ReadFile("spawned")
		if err != nil {
			t.Fatal(err)
		}
		write(t, "pids", fmt.Sprintf("%d %d %d %s", os.Getpid(), child.Process.Pid, detached.Process.Pid, data))
		if os.Getenv("CAPACITY_MODE") == "signal" {
			time.Sleep(120 * time.Second)
		}
	case "early":
		fast := os.Getenv("CAPACITY_FAST")
		if root == fast {
			child := spawn(t, "sleep", false)
			write(t, "left", strconv.Itoa(child.Process.Pid))
		} else {
			waitFile(t, filepath.Join(fast, "left"))
			data, err := os.ReadFile(filepath.Join(fast, "left"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(20 * time.Second)
			for alive(t, pid) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if alive(t, pid) {
				t.Fatal("finished check cleaned up too late")
			}
			write(t, "verdict", "early")
		}
		os.Exit(0)
	}
	fmt.Println("private postgres://credential-canary@example/db")
	fmt.Println("SQLSTATE 53300: too many clients\ntoo many clients")
	if filepath.Base(root) == "second" {
		os.Exit(7)
	}
	os.Exit(0)
}

func spawn(t *testing.T, role string, detached bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "CAPACITY_HELPER="+role)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: detached}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	write(t, "child-"+strconv.Itoa(cmd.Process.Pid), strconv.Itoa(cmd.Process.Pid))
	return cmd
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
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
