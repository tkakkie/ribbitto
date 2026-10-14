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
	"sync/atomic"
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

// deliver hands a reaped watcher's result to supervise; a test replaces it
// to deliver a result late.
var deliver = func(w watcher, result error) { w.done <- result }

// listProcesses returns "pid ppid stat" lines for every process; a test
// replaces it to fail.
var listProcesses = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,stat=").Output()
	return string(out), err
}

// afterReap runs between Wait reaping a watcher and the reaped flag being
// set; a test pauses there to reuse the PID in that interval.
var afterReap = func(watcher) {}

// beforeSignals runs once per shutdown, just before its first signal; a test
// uses it to order a late result after that point.
var beforeSignals = func() {}

type watcher struct {
	cmd  *exec.Cmd
	done chan error
	pid  int // Wait releases the process handle; retain the session ID.
	// reaped is set as soon as Wait returns, before the result is delivered,
	// so shutdown knows the leader is gone even if its PID is reused.
	reaped *atomic.Bool
}

// start runs cmd in its own session and reaps it in the background.
func start(cmd *exec.Cmd) (watcher, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return watcher{}, fmt.Errorf("starting %s: %w", cmd.Path, err)
	}
	w := watcher{cmd, make(chan error, 1), cmd.Process.Pid, &atomic.Bool{}}
	go func() {
		result := cmd.Wait()
		afterReap(w)
		w.reaped.Store(true)
		deliver(w, result)
		close(w.done)
	}()
	return w, nil
}

func supervise(ctx context.Context, commands []*exec.Cmd) (err error) {
	var watchers []watcher
	defer func() { err = errors.Join(err, shutdown(watchers)) }()
	for _, cmd := range commands {
		if ctx.Err() != nil {
			return nil
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		w, err := start(cmd)
		if err != nil {
			return err
		}
		watchers = append(watchers, w)
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
	if len(watchers) == 0 {
		return nil
	}
	// A watcher failed on its own if it exited with a non-zero status at any
	// time, or ended in any error before shutdown's first signal; ending by a
	// signal after that is shutdown's doing. Neither test depends on when a
	// result is delivered or on how close an exit came to the signals (the
	// maintainer and Codex on #1042). It relies on the watchers ending by the
	// signal or with status 0 when stopped: Tailwind dies of SIGTERM and SIGINT,
	// templ exits 0 (measured with the pinned versions).
	exited, scanErr := exitedLeaders(watchers)
	defer func() {
		for i, w := range watchers {
			if result := <-w.done; result != nil && (exited[i] || failedOnItsOwn(result)) {
				err = errors.Join(err, fmt.Errorf("watcher %s failed: %w", w.cmd.Path, result))
			}
		}
		// Without the table no running watcher can be told from a failed one
		// whose result is late, so the run cannot count as clean.
		err = errors.Join(err, scanErr)
	}()
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
		remaining, err := signalSessions(watchers, exited, sig)
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

func signalSessions(watchers []watcher, exited []bool, sig syscall.Signal) (int, error) {
	out, err := listProcesses()
	if err != nil {
		return 0, fmt.Errorf("scanning watcher sessions: %w", err)
	}
	remaining := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// Zombies cannot run or be signalled; init owns orphan reaping.
		if len(fields) != 3 || strings.HasPrefix(fields[2], "Z") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		for i, w := range watchers {
			// Once the leader is reaped, a process with its PID is a new
			// session leader, not ours: neither signalled nor waited for.
			if exited[i] && pid == w.pid {
				continue
			}
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

// failedOnItsOwn reports whether a watcher exited by itself with a non-zero
// status, which shutdown's signals never cause.
func failedOnItsOwn(result error) bool {
	var exit *exec.ExitError
	return errors.As(result, &exit) && exit.Exited() && exit.ExitCode() != 0
}

// exitedLeaders reports, per watcher, whether its leader had already exited.
// A leader still runs only if it is listed, not a zombie, its own session's
// leader and this process's child: a PID reused after the reap, even before
// the reaped flag is set, belongs to a process with another parent, unless a
// later watcher reused it. Two live processes never share a PID, so a
// watcher whose PID a later watcher holds has exited (Codex on #1042).
func exitedLeaders(watchers []watcher) ([]bool, error) {
	exited := make([]bool, len(watchers))
	out, err := listProcesses()
	if err != nil {
		for i, w := range watchers {
			exited[i] = w.reaped.Load()
		}
		return exited, fmt.Errorf("reading the process table before shutdown: %w", err)
	}
	ours, self := map[int]bool{}, os.Getpid()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || strings.HasPrefix(fields[2], "Z") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if ppid, err := strconv.Atoi(fields[1]); err == nil && ppid == self {
			ours[pid] = true
		}
	}
	for i, w := range watchers {
		sid, err := unix.Getsid(w.pid)
		exited[i] = w.reaped.Load() || !ours[w.pid] || err != nil || sid != w.pid
		for _, later := range watchers[i+1:] {
			exited[i] = exited[i] || later.pid == w.pid
		}
	}
	return exited, nil
}
