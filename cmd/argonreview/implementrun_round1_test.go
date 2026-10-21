package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestImplementRunTrustedPATH(t *testing.T) {
	for _, path := range []string{"", ".", "bin", "./x", "/usr/bin::/bin", "worktree", "absent"} {
		t.Run(path, func(t *testing.T) {
			x := newImplementRun(t)
			want := testAgyParentEnvironment(t)
			for _, name := range []string{"LC_ALL", "LC_COLLATE", "LC_CTYPE", "LC_MESSAGES", "LC_MONETARY", "LC_NUMERIC", "LC_TIME", "LC_ADDRESS", "LC_IDENTIFICATION", "LC_MEASUREMENT", "LC_NAME", "LC_PAPER", "LC_TELEPHONE"} {
				t.Setenv(name, "C")
				want[name] = "C"
			}
			t.Setenv("GITHUB_TOKEN", "dummy-excluded")
			t.Setenv("SSH_AUTH_SOCK", "dummy-excluded")
			// Absolute fixture commands keep a hostile PATH from preventing
			// the environment assertion itself from running.
			write(t, filepath.Join(x.fake, "agy"), "#!/bin/sh\n/usr/bin/env > '"+filepath.Join(x.fake, "env")+"'\nprintf '%s\\n' \"$TMPDIR\" > '"+filepath.Join(x.fake, "tmp")+"'\n/bin/cat '"+filepath.Join(x.fake, "stdout")+"'\n")
			parent := os.Environ()
			var controlled []string
			for _, entry := range parent {
				if !strings.HasPrefix(entry, "PATH=") {
					controlled = append(controlled, entry)
				}
			}
			if path == "worktree" {
				path = "/usr/bin:" + x.f.tree + ":/bin"
			}
			if path != "absent" {
				controlled = append(controlled, "PATH="+path)
			}
			x.deps.environ = func() []string { return controlled }
			x.run(t, 0)
			tmp, err := os.ReadFile(filepath.Join(x.fake, "tmp"))
			if err != nil {
				t.Fatal(err)
			}
			want["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin:" + filepath.Join(x.f.account, ".local/bin")
			want["HOME"], want["TMPDIR"] = x.f.home, strings.TrimSpace(string(tmp))
			assertFakeAgyEnvironment(t, filepath.Join(x.fake, "env"), want)
			x.cleaned(t)
		})
	}
}

func implementRunRefusedUnchanged(t *testing.T, x *implementRunFixture, reason string) {
	t.Helper()
	before := implementRunSnapshot(t, x.f.root)
	var out, stderr bytes.Buffer
	if code := implementRunCommand(x.args, &out, &stderr, x.deps); code == 0 || out.Len() != 0 || !strings.Contains(stderr.String(), reason) {
		t.Fatalf("not refused before writes: code=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
	if !reflect.DeepEqual(before, implementRunSnapshot(t, x.f.root)) {
		t.Fatal("refusal changed home, logs or TMPDIR snapshot")
	}
}

func TestImplementRunExclusiveLock(t *testing.T) {
	x := newImplementRun(t)
	lock, err := implementRunLock(x.f.home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	info, err := lock.Stat()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode: %v %v", info, err)
	}
	flags, err := unix.FcntlInt(lock.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("lock inherited across exec: flags=%d err=%v", flags, err)
	}
	write(t, x.f.tree+".tmp-existing/keep", "unchanged")
	implementRunRefusedUnchanged(t, x, "acquiring implement-run lock")
	if x.checks != 1 {
		t.Fatal("locked run reached more than the untouched at-rest check")
	}
}

func TestImplementRunRecoveryMarkerRefusal(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink", "missing-lock"} {
		t.Run(kind, func(t *testing.T) {
			x := newImplementRun(t)
			lock, err := implementRunLock(x.f.home)
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "missing-lock" {
				implementCheckRemove(t, filepath.Join(x.f.home, "guard/implement-run.lock"))
			}
			marker := filepath.Join(x.f.home, "guard/implement-run.recovery-needed")
			switch kind {
			case "file", "missing-lock":
				write(t, marker, "manual recovery needed")
			case "directory":
				implementCheckMkdir(t, marker)
			case "symlink":
				symlink(t, filepath.Join(x.f.root, "absent"), marker)
			}
			implementRunRefusedUnchanged(t, x, "recovery marker exists")
		})
	}
}

func TestImplementRunRecoveryLifecycle(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "confirmed"}[confirmed], func(t *testing.T) {
			x := newImplementRun(t)
			write(t, filepath.Join(x.fake, "status"), "7")
			if !confirmed {
				x.deps.ops.held = func(int) bool { return false }
				x.deps.ops.scan = func(int) ([]int, error) { return nil, errors.New("cannot scan") }
			}
			status := x.run(t, 7)
			marker := filepath.Join(x.f.home, "guard/implement-run.recovery-needed")
			data, err := os.ReadFile(marker)
			if confirmed {
				if err != nil {
					t.Fatalf("confirmed failure lost marker: %s %v", data, err)
				}
				x.cleaned(t)
				return
			}
			if err != nil || !strings.Contains(string(data), "manual recovery needed") || !strings.Contains(string(data), x.f.tree) || !strings.Contains(status, "recovery-marker=\""+marker+"\"") {
				t.Fatalf("missing marker: %s %v status=%s", data, err, status)
			}
			stamp := strings.Split(strings.Split(string(data), "time=")[1], " ")[0]
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(marker)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("marker mode: %v %v", info, err)
			}
			// Even a new, absent log directory must remain absent on refusal.
			x.set("log-dir", x.logs+"-next")
			implementRunRefusedUnchanged(t, x, "recovery marker exists")
			// The real in-process check accepts both extra guard files.
			var out bytes.Buffer
			if code := implementCheckCommand(x.f.args(), &out, &out, x.f.deps); code != 0 {
				t.Fatalf("guard files fail implement-check: %s", out.String())
			}
		})
	}
}

func TestImplementRunRecoveryMarkerWriteFailure(t *testing.T) {
	x := newImplementRun(t)
	x.deps.step = func(name string) error {
		if name == "mark-in-progress" {
			return errors.New("injected marker creation")
		}
		return nil
	}
	status := x.run(t, 1)
	if !strings.Contains(status, "injected marker creation") || !strings.Contains(status, "counts=false") {
		t.Fatal(status)
	}
	if _, err := os.Stat(filepath.Join(x.fake, "args")); !os.IsNotExist(err) {
		t.Fatal("started without marker")
	}

}

func TestImplementRunLockHeldThroughStatus(t *testing.T) {
	x := newImplementRun(t)
	x.deps.saveStatus = func(f *os.File, status string) error {
		lock, err := implementRunLock(x.f.home)
		if err == nil {
			_ = lock.Close()
			t.Fatal("lock released before status")
		}
		return implementRunSaveStatus(f, status)
	}
	x.run(t, 0)
	lock, err := implementRunLock(x.f.home)
	if err != nil {
		t.Fatalf("normal run did not release lock: %v", err)
	}
	_ = lock.Close()
	x.cleaned(t)
}

func TestImplementRunLockSymlink(t *testing.T) {
	x := newImplementRun(t)
	implementCheckMkdir(t, filepath.Join(x.f.home, "guard"))
	symlink(t, x.f.saved, filepath.Join(x.f.home, "guard/implement-run.lock"))
	implementRunRefusedUnchanged(t, x, "symlink in home")
}

func TestImplementRunAccountHomeOverlap(t *testing.T) {
	for _, kind := range []string{"equal", "nested", "enclosing", "home-alias", "resolved-equal"} {
		t.Run(kind, func(t *testing.T) {
			x := newImplementRun(t)
			reason := "must not overlap calling account home"
			switch kind {
			case "equal":
				x.deps.accountHome = x.f.home
			case "nested":
				x.deps.accountHome = x.f.root
			case "enclosing":
				x.deps.accountHome = filepath.Join(x.f.home, "account")
				implementCheckMkdir(t, x.deps.accountHome)
			case "home-alias":
				alias := filepath.Join(x.f.root, "alias")
				symlink(t, x.f.account, alias)
				x.set("home", alias)
				reason = "directory"
			case "resolved-equal":
				// Only the account path has a symlink component; --home is real
				// and literally disjoint from that alias's parent.
				alias := filepath.Join(x.f.root, "alias")
				symlink(t, x.f.root, alias)
				x.deps.accountHome = filepath.Join(alias, "home")
			}
			implementRunRefusedUnchanged(t, x, reason)
		})
	}
}

func TestImplementRunAgyValidation(t *testing.T) {
	for _, kind := range []string{"worktree", "home", "logs", "non-executable", "leaf-link", "component-link", "relative", "default"} {
		t.Run(kind, func(t *testing.T) {
			x := newImplementRun(t)
			agy := filepath.Join(x.fake, "agy")
			reason := "agy"
			switch kind {
			case "worktree", "home", "logs":
				root := map[string]string{"worktree": x.f.tree, "home": x.f.home, "logs": x.logs}[kind]
				agy = filepath.Join(root, "agy")
				write(t, agy, "#!/bin/sh\nexit 0\n")
				if err := os.Chmod(agy, 0o755); err != nil {
					t.Fatal(err)
				}
				reason = "Argon-writable"
			case "non-executable":
				if err := os.Chmod(agy, 0o600); err != nil {
					t.Fatal(err)
				}
				reason = "executable"
			case "leaf-link":
				link := filepath.Join(x.f.root, "agy-link")
				symlink(t, agy, link)
				agy = link
			case "component-link":
				link := filepath.Join(x.f.root, "alias")
				symlink(t, x.fake, link)
				agy = filepath.Join(link, "agy")
			case "relative":
				agy, reason = "fake/agy", "absolute and clean"
			case "default":
				agy = filepath.Join(x.f.account, ".local/bin/agy")
				write(t, agy, "non-executable default")
				x.args = x.args[:len(x.args)-4]
				reason = "executable"
			}
			if kind != "default" {
				x.set("agy", agy)
			}
			implementRunRefusedUnchanged(t, x, reason)
		})
	}
}

func TestImplementRunAgyInTMPDIR(t *testing.T) {
	x := newImplementRun(t)
	write(t, filepath.Join(x.fake, "agy"), "#!/bin/sh\n: > '"+filepath.Join(x.f.root, "launched")+"'\ncat '"+filepath.Join(x.fake, "stdout")+"'\n")
	x.deps.mkdirTemp = func(string, string) (string, error) { return x.fake, nil }
	status := x.run(t, 1)
	if !strings.Contains(status, "Argon-writable") || !strings.Contains(status, "cleanup-remove-tmp=true") {
		t.Fatal(status)
	}
	if _, err := os.Lstat(x.fake); !os.IsNotExist(err) {
		t.Fatalf("TMPDIR not cleaned: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(x.f.root, "launched")); !os.IsNotExist(err) {
		t.Fatal("agy launched from TMPDIR")
	}
	x.cleaned(t)
}

func TestImplementRunExistingMCP(t *testing.T) {
	for _, kind := range []string{"nonempty", "empty", "absent"} {
		t.Run(kind, func(t *testing.T) {
			x := newImplementRun(t)
			path := filepath.Join(x.f.home, ".gemini/config/mcp_config.json")
			switch kind {
			case "nonempty":
				write(t, path, "{\"mcpServers\":{\"verify\":{}}}\n")
				implementRunRefusedUnchanged(t, x, "existing MCP configuration")
				return
			case "absent":
				implementCheckRemove(t, path)
				implementRunRefusedUnchanged(t, x, "initial at-rest check failed")
				return
			}
			x.run(t, 0)
			x.cleaned(t)
		})
	}
}

func TestImplementRunStatusSaveFailure(t *testing.T) {
	for _, kind := range []string{"save-once", "save-always", "save-and-print", "save-and-print-always", "success"} {
		t.Run(kind, func(t *testing.T) {
			x := newImplementRun(t)
			calls := 0
			var attempts []string
			x.deps.saveStatus = func(f *os.File, status string) error {
				calls++
				attempts = append(attempts, status)
				if kind != "success" && (calls == 1 || strings.HasSuffix(kind, "always")) {
					return errors.New("injected status save")
				}
				return implementRunSaveStatus(f, status)
			}
			var out, stderr bytes.Buffer
			var writer io.Writer = &out
			if strings.HasPrefix(kind, "save-and-print") {
				writer = implementRunFailWriter{}
			}
			code := implementRunCommand(x.args, writer, &stderr, x.deps)
			if kind == "success" {
				data, err := os.ReadFile(filepath.Join(x.logs, "status"))
				settings, _ := os.ReadFile(filepath.Join(x.f.home, "implementer/settings.json"))
				hooks, _ := os.ReadFile(filepath.Join(x.f.home, "implementer/hooks.json"))
				want := "exit=0\ncounts=true\npre-check=0\npost-check=0\nstop-confirmed=true\nsession-left=0\n" +
					fmt.Sprintf("verify=off\ngenerated-settings-sha256=%x\ngenerated-hooks-sha256=%x\ngenerated-mcp-sha256=%x\n", sha256.Sum256(settings), sha256.Sum256(hooks), sha256.Sum256(nil)) +
					"cleanup-restore-settings=true\ncleanup-restore-hooks=true\ncleanup-empty-brain=true\ncleanup-remove-tmp=true\ncleanup-remove-agent=true\ncleanup-remove-agents=true\n"
				if code != 0 || calls != 1 || err != nil || string(data) != want || out.String() != want {
					t.Fatalf("success changed: code=%d calls=%d out=%s saved=%s err=%v", code, calls, out.String(), data, err)
				}
				return
			}
			if code != 1 || calls < 2 || !strings.Contains(attempts[1], "counts=false") || !strings.Contains(attempts[1], "log/status save failed") {
				t.Fatalf("no rebuilt status: code=%d attempts=%q stderr=%s", code, attempts, stderr.String())
			}
			if kind == "save-and-print-always" {
				if calls != 3 || !strings.Contains(stderr.String(), "status output failed") || !strings.Contains(stderr.String(), "log/status after output failed") || !strings.Contains(attempts[2], "counts=false") || !strings.Contains(attempts[2], "status output failed") {
					t.Fatalf("lost persistent combined failure: attempts=%q stderr=%s", attempts, stderr.String())
				}
			} else if kind == "save-and-print" {
				data, err := os.ReadFile(filepath.Join(x.logs, "status"))
				if err != nil || calls != 3 || !strings.Contains(string(data), "status output failed") || !strings.Contains(string(data), "log/status save failed") || !strings.Contains(string(data), "counts=false") {
					t.Fatalf("lost combined failure: %s %v", data, err)
				}
			} else if !strings.Contains(out.String(), "counts=false") || !strings.Contains(out.String(), "log/status save failed") {
				t.Fatalf("printed stale status: %s", out.String())
			}
			x.cleaned(t)
		})
	}
}

func TestImplementRunSignalBeforeLaunch(t *testing.T) {
	for _, phase := range []string{"placement", "pre-check"} {
		t.Run(phase, func(t *testing.T) {
			x := newImplementRun(t)
			signals := make(chan os.Signal, 1)
			x.deps.withSignals = func(run func(<-chan os.Signal) (int, error)) (int, error) { return run(signals) }
			if phase == "pre-check" {
				check := x.deps.check
				x.deps.check = func(args []string, out, stderr io.Writer) int {
					code := check(args, out, stderr)
					if x.checks == 2 {
						signals <- syscall.SIGTERM
					}
					return code
				}
			} else {
				step := x.deps.step
				x.deps.step = func(name string) error {
					if name == "place-hooks" {
						signals <- syscall.SIGTERM
					}
					return step(name)
				}
			}
			var out, stderr bytes.Buffer
			code := implementRunCommand(x.args, &out, &stderr, x.deps)
			if code != 143 || !strings.Contains(out.String(), "counts=false") || strings.Count(out.String(), "interrupted by terminated") != 1 || strings.Contains(out.String(), "stream-json") || x.checks != 4 {
				t.Fatalf("signal lost or repeated: code=%d checks=%d out=%s stderr=%s", code, x.checks, out.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(x.fake, "args")); !os.IsNotExist(err) {
				t.Fatal("agy launched despite pending signal")
			}
			x.cleaned(t)
		})
	}
}
