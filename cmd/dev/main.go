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
			w.done <- cmd.Wait()
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
	// Copilot on #1042: preserve failures delivered before shutdown signals;
	// later results may be caused by those signals.
	var delivered error
	for _, w := range watchers {
		select {
		case result := <-w.done:
			if result != nil {
				delivered = errors.Join(delivered, fmt.Errorf("watcher %s failed: %w", w.cmd.Path, result))
			}
		default:
		}
	}
	defer func() {
		err = errors.Join(err, delivered)
		for _, w := range watchers {
			<-w.done
		}
	}()
	if len(watchers) == 0 {
		return nil
	}
	// Give the server time to drain, then kill stubborn descendants. Scanning
	// again also catches a child forked between the first scan and its signal.
	deadline := time.Now().Add(3 * time.Second)
	for {
		sig := syscall.SIGTERM
		if time.Until(deadline) < 2*time.Second {
			sig = syscall.SIGKILL
		}
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
