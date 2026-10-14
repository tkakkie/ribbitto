package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
		{"templ exits", nil, "templ", false},
		{"css fails", nil, "css", true},
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
			roles := []string{"css", "css-child", "css-child-child", "templ", "templ-child", "templ-child-child"}
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
	if role == "supervisor" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		commands := []*exec.Cmd{helper("css", root), helper("templ", root)}
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
	if !strings.HasSuffix(role, "-child-child") {
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
