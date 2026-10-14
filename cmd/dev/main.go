// Command dev runs the Tailwind and templ watchers for make dev. Each owns
// a session because templ moves its server command into another process group.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	tailwind, templ, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: dev -tailwind <binary> -templ <binary>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, tailwind, templ); err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		os.Exit(1)
	}
}

func parse(args []string) (string, string, error) {
	flags := flag.NewFlagSet("dev", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tailwind := flags.String("tailwind", "", "pinned Tailwind binary")
	templ := flags.String("templ", "", "pinned templ binary")
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if flags.NArg() != 0 || *tailwind == "" || *templ == "" {
		return "", "", errors.New("invalid arguments")
	}
	return *tailwind, *templ, nil
}

func run(ctx context.Context, tailwind, templ string) error {
	css := exec.Command(tailwind, "-i", "web/styles/app.css", "-o", "web/static/css/app.css", "--minify", "--watch=always")
	server := exec.Command("./bin/ai-db", "run", templ, "generate", "--watch", "--cmd", "go run ./cmd/ribbitto")
	server.Env = append(os.Environ(), "RIBBITTO_DEV_ASSETS=web/static")
	return supervise(ctx, []*exec.Cmd{css, server})
}

// waitWatcher reaps a watcher; a test replaces it to deliver a result late.
var waitWatcher = func(cmd *exec.Cmd) error { return cmd.Wait() }

// beforeSignals runs once per shutdown, just before its first signal; a test
// uses it to order a late result after that point.
var beforeSignals = func() {}

type watcher struct {
	cmd  *exec.Cmd
	done chan error
	pid  int // Wait releases the process handle; retain the session ID.
}

func supervise(ctx context.Context, commands []*exec.Cmd) (err error) {
	var watchers []watcher
	defer func() { err = errors.Join(err, shutdown(watchers)) }()
	for _, cmd := range commands {
		if ctx.Err() != nil {
			return nil
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("starting %s: %w", cmd.Path, err)
		}
		w := watcher{cmd, make(chan error, 1), cmd.Process.Pid}
		watchers = append(watchers, w)
		go func() {
			w.done <- waitWatcher(cmd)
			close(w.done)
		}()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		exited := false
		for _, w := range watchers {
			select {
			case err := <-w.done:
				if err != nil {
					return fmt.Errorf("watcher %s failed: %w", w.cmd.Path, err)
				}
				exited = true
			default:
			}
		}
		if exited {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func shutdown(watchers []watcher) (err error) {
	// A watcher whose leader had exited before shutdown signalled anything
	// failed on its own, whenever Wait's result arrives; one still running
	// then and ending later ended because of the signals. The state is read
	// from the process table at that instant, so the verdict does not depend
	// on when a result is delivered (Copilot and the maintainer on #1042).
	exited := exitedLeaders(watchers)
	results := make([]error, len(watchers))
	received := make([]bool, len(watchers))
	for i, w := range watchers {
		select {
		case result, ok := <-w.done:
			received[i], exited[i] = true, true
			if ok {
				results[i] = result
			}
		default:
		}
	}
	defer func() {
		for i, w := range watchers {
			if !received[i] {
				results[i] = <-w.done
			}
			if exited[i] && results[i] != nil {
				err = errors.Join(err, fmt.Errorf("watcher %s failed: %w", w.cmd.Path, results[i]))
			}
		}
	}()
	if len(watchers) == 0 {
		return nil
	}
	beforeSignals()
	// Give the server time to drain, then kill stubborn descendants. Scanning
	// again also catches a child forked between the first scan and its signal.
	deadline := time.Now().Add(3 * time.Second)
	for {
		sig := syscall.SIGTERM
		if time.Until(deadline) < 2*time.Second {
			sig = syscall.SIGKILL
		}
		for i, w := range watchers {
			if !exited[i] {
				_ = w.cmd.Process.Signal(sig)
			}
		}
		remaining, err := signalSessions(watchers, sig)
		if err != nil {
			// Still stop owned leaders if ps is denied;
			// report failure because detached descendants cannot be confirmed.
			time.Sleep(time.Second)
			for _, w := range watchers {
				_ = w.cmd.Process.Signal(syscall.SIGKILL)
			}
			return err
		}
		if remaining == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("watcher descendants remain after shutdown")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func signalSessions(watchers []watcher, sig syscall.Signal) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,stat=").Output()
	if err != nil {
		return 0, fmt.Errorf("scanning watcher sessions: %w", err)
	}
	remaining := 0
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		// Zombies cannot run or be signalled; init owns orphan reaping.
		if len(fields) != 2 || strings.HasPrefix(fields[1], "Z") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		for _, w := range watchers {
			if sid, err := unix.Getsid(pid); err == nil && sid == w.pid {
				// A leader's PID may have been reused after Wait. Process.Signal
				// checks its handle; never signal that PID from a stale ps listing.
				if pid != w.pid {
					if sid, err := unix.Getsid(pid); err == nil && sid == w.pid {
						_ = syscall.Kill(pid, sig)
					}
				}
				remaining++
			}
		}
	}
	return remaining, nil
}

// exitedLeaders reports, per watcher, whether its leader had already exited:
// listed as a zombie, not listed, or listed under another session (a reused
// PID). If ps fails, nothing counts as exited here; the cleanup scan reports
// that failure.
func exitedLeaders(watchers []watcher) []bool {
	exited := make([]bool, len(watchers))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,stat=").Output()
	if err != nil {
		return exited
	}
	running := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.HasPrefix(fields[1], "Z") {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil {
			running[pid] = true
		}
	}
	for i, w := range watchers {
		sid, err := unix.Getsid(w.pid)
		exited[i] = !running[w.pid] || err != nil || sid != w.pid
	}
	return exited
}
