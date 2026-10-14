// Command capacity measures two uncached required-database checks. The observer
// connects through bin/ai-db before checks start; credentials stay in private logs.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

type options struct {
	roots              [2]string
	packages, parallel int
}

func main() {
	options, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: capacity WT1 WT2 PACKAGES PARALLEL")
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	received := make(chan os.Signal, 1)
	go func() { received <- <-signals; cancel() }()
	code, err := run(ctx, options, "bin/ai-db", os.Stdout, systemCleanup())
	if err != nil {
		// Neither command errors nor log contents are safe to print here.
		fmt.Fprintln(os.Stderr, "capacity run failed; check arguments, admin configuration and PostgreSQL availability")
		code = 1
	}
	if ctx.Err() != nil {
		code = 128 + int((<-received).(syscall.Signal))
	}
	os.Exit(code)
}

func parse(args []string) (options, error) {
	var o options
	if len(args) != 4 {
		return o, fmt.Errorf("invalid arguments")
	}
	for i := range o.roots {
		root, err := filepath.EvalSymlinks(args[i])
		if err != nil {
			return o, fmt.Errorf("resolving worktree: %w", err)
		}
		o.roots[i], err = filepath.Abs(root)
		if err != nil {
			return o, fmt.Errorf("resolving worktree: %w", err)
		}
		makefile, err := os.ReadFile(filepath.Join(o.roots[i], "Makefile"))
		if err != nil || !strings.Contains(string(makefile), "$(GO_TEST_FLAGS)") {
			return o, fmt.Errorf("worktrees need configurable test flags")
		}
	}
	var err error
	o.packages, err = strconv.Atoi(args[2])
	if err != nil {
		return o, fmt.Errorf("parsing packages: %w", err)
	}
	o.parallel, err = strconv.Atoi(args[3])
	if err != nil {
		return o, fmt.Errorf("parsing parallel: %w", err)
	}
	if o.roots[0] == o.roots[1] || o.packages <= 0 || o.parallel <= 0 {
		return o, fmt.Errorf("invalid capacity arguments")
	}
	return o, nil
}

type observer struct {
	stdin io.WriteCloser
	lines chan string
	done  chan struct{}
	err   error
}

func startObserver(ctx context.Context, helper string) (*observer, error) {
	cmd := exec.CommandContext(ctx, helper, "--observe")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening observer input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("opening observer output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("starting observer: %w", err)
	}
	o := &observer{stdin, make(chan string, 2), make(chan struct{}), nil}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			o.lines <- scanner.Text()
		}
		err := scanner.Err()
		waitErr := cmd.Wait()
		if err == nil {
			err = waitErr
		}
		o.err = err
		close(o.done)
		close(o.lines)
	}()
	return o, nil
}

func run(ctx context.Context, o options, helper string, out io.Writer, cleanup cleanupOps) (code int, err error) {
	// Keep the observer slot through check cleanup, even on interruption.
	observerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer, err := startObserver(observerCtx, helper)
	if err != nil {
		return 1, err
	}
	var workers []*worker
	var logs []string
	stopped := map[int]bool{}
	stop := func(w *worker) {
		if !stopped[w.pid] {
			stopped[w.pid] = true
			if !cleanup.stop(w) {
				err = fmt.Errorf("could not confirm that every check process stopped")
			}
		}
	}
	defer func() {
		for _, w := range workers {
			stop(w)
		}
		_ = observer.stdin.Close()
		cancel()
		// Wait even on cancellation, so no observer is left behind.
		<-observer.done
	}()
	report := map[string]any{}
	select {
	case line := <-observer.lines:
		if err := json.Unmarshal([]byte(line), &report); err != nil {
			return 1, fmt.Errorf("reading observer handshake: %w", err)
		}
	case <-ctx.Done():
		return 1, ctx.Err()
	}
	// This handshake reserves the observer's slot before either check starts.
	for _, key := range []string{"baseline", "max_connections"} {
		if value, ok := report[key].(float64); !ok || value <= 0 {
			return 1, fmt.Errorf("invalid observer handshake")
		}
	}
	report["packages"], report["parallel"] = o.packages, o.parallel
	report["count"], report["require_db"] = 1, 1
	// Plain pgx would send pool_max_conns in the admin URL to PostgreSQL.
	report["pgx_pool_max_conns"] = pgtest.MaxConns
	report["migration_pool_max_open"], report["admin_connections_per_pgtest_call"] = 0, 1
	for _, root := range o.roots {
		w, log, startErr := startCheck(root, o.packages, o.parallel)
		if startErr != nil {
			return 1, startErr
		}
		workers, logs = append(workers, w), append(logs, log)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		running := false
		for _, w := range workers {
			if pollErr := w.poll(); pollErr != nil {
				return 1, pollErr
			}
			if w.exited {
				stop(w)
			} else {
				running = true
			}
		}
		if !running {
			break
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-observer.done:
			return 1, fmt.Errorf("capacity observer failed")
		case <-ticker.C:
		}
	}
	if err != nil {
		return 1, err
	}
	_ = observer.stdin.Close()
	select {
	case line := <-observer.lines:
		peak, parseErr := strconv.Atoi(line)
		if parseErr != nil {
			return 1, fmt.Errorf("reading peak: %w", parseErr)
		}
		report["peak_connections"] = peak
	case <-ctx.Done():
		return 1, ctx.Err()
	}
	<-observer.done
	if observer.err != nil {
		return 1, fmt.Errorf("capacity observer failed: %w", observer.err)
	}
	codes, counts := []int{}, []int{}
	for i, w := range workers {
		codes = append(codes, w.exitCode)
		if w.exitCode != 0 {
			code = 1
		}
		count, countErr := matchingLines(logs[i])
		if countErr != nil {
			return 1, countErr
		}
		counts = append(counts, count)
	}
	report["exit_codes"], report["too_many_clients_lines"] = codes, counts
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return 1, fmt.Errorf("writing report: %w", err)
	}
	return code, nil
}

func matchingLines(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening log: %w", err)
	}
	defer func() { _ = file.Close() }()
	// ReadString has no line-size limit; compiler errors and test output can be long.
	reader, count := bufio.NewReader(file), 0
	for {
		line, err := reader.ReadString('\n')
		if strings.Contains(line, "53300") || strings.Contains(strings.ToLower(line), "too many clients") {
			count++
		}
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return 0, fmt.Errorf("reading log: %w", err)
		}
	}
}
