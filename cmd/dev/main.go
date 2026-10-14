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
	// Taken once, for the verdict only: each scan below decides afresh which
	// leaders are gone (#1052).
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
		// Process.Signal checks its handle, so a reaped leader is never
		// signalled through its reused PID.
		for _, w := range watchers {
			_ = w.cmd.Process.Signal(sig)
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
	reaped := reapedFlags(watchers)
	table, err := readTable()
	if err != nil {
		return 0, fmt.Errorf("scanning watcher sessions: %w", err)
	}
	gone, reused := classify(watchers, reaped, table)
	remaining := 0
	for _, p := range table {
		// Zombies cannot run or be signalled; init owns orphan reaping.
		if p.zombie {
			continue
		}
		for i, w := range watchers {
			// The kernel never hands out a PID still in use as a session ID, so
			// while another process holds a gone leader's PID nothing in that
			// session is ours (Argon on #1042). A gone leader's PID held by our
			// own child (ps) leads no session of ours either.
			if reused[i] || gone[i] && p.pid == w.pid {
				continue
			}
			if sid, err := unix.Getsid(p.pid); err == nil && sid == w.pid {
				// A leader's PID may have been reused after Wait. Process.Signal
				// checks its handle; never signal that PID from a stale ps listing.
				if p.pid != w.pid {
					if sid, err := unix.Getsid(p.pid); err == nil && sid == w.pid {
						_ = syscall.Kill(p.pid, sig)
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
func exitedLeaders(watchers []watcher) ([]bool, error) {
	reaped := reapedFlags(watchers)
	table, err := readTable()
	if err != nil {
		return reaped, fmt.Errorf("reading the process table before shutdown: %w", err)
	}
	gone, _ := classify(watchers, reaped, table)
	return gone, nil
}

// process is one line of the process table.
type process struct {
	pid, ppid int
	zombie    bool
}

// readTable lists every process. Callers read the reaped flags first, so a
// process listed with the PID of a watcher already reaped holds it after the
// reap.
func readTable() ([]process, error) {
	out, err := listProcesses()
	if err != nil {
		return nil, err
	}
	var table []process
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		table = append(table, process{pid, ppid, strings.HasPrefix(fields[2], "Z")})
	}
	return table, nil
}

func reapedFlags(watchers []watcher) []bool {
	reaped := make([]bool, len(watchers))
	for i, w := range watchers {
		reaped[i] = w.reaped.Load()
	}
	return reaped
}

// classify reports, per watcher, whether its leader is gone and whether
// another process now holds its PID. A leader still runs only if it is listed,
// not a zombie, its own session's leader and this process's child: a PID
// reused after the reap, even before the reaped flag is set, belongs to a
// process with another parent, unless a later watcher reused it. Two live
// processes never share a PID, so a watcher whose PID a later watcher holds
// has exited (Codex on #1042). The PID is reused if it is listed once the
// leader was reaped or held by a process that is not our child; until the
// reap, our own child with that PID may be the leader's zombie, and a later
// watcher holding it leads a session that is ours anyway.
func classify(watchers []watcher, reaped []bool, table []process) (gone, reused []bool) {
	holders, self := map[int]process{}, os.Getpid()
	for _, p := range table {
		holders[p.pid] = p
	}
	gone, reused = make([]bool, len(watchers)), make([]bool, len(watchers))
	for i, w := range watchers {
		holder, held := holders[w.pid]
		later := false
		for _, next := range watchers[i+1:] {
			later = later || next.pid == w.pid
		}
		// The flag is read again before Getsid: a leader reaped since the
		// table was read may already have its PID taken, which makes its
		// line stale (Codex and Argon on #1055). A process holding the PID
		// after the reap is not ours.
		now := reaped[i] || w.reaped.Load()
		sid, err := unix.Getsid(w.pid)
		gone[i] = now || !held || holder.zombie || holder.ppid != self || err != nil || sid != w.pid || later
		reused[i] = held && (reaped[i] || holder.ppid != self) || now && err == nil
	}
	return gone, reused
}
