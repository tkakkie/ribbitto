package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (r *implementRun) execute(out io.Writer) (int, error) {
	if err := os.Mkdir(r.logs, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return 1, err
	}
	// Recheck emptiness immediately before exclusive creation. Keep descriptors
	// open through judgement/status: no later pathname read accepts a swap.
	if err := implementRealDirectory(r.logs, os.Lstat); err != nil {
		return 1, err
	}
	entries, err := os.ReadDir(r.logs)
	if err != nil || len(entries) != 0 {
		return 1, errors.New("log directory is no longer empty")
	}
	logs := make(map[string]*os.File)
	defer func() {
		for _, f := range logs {
			_ = f.Close()
		}
	}()
	for _, name := range []string{"status", "out.jsonl", "err.txt", "pre.txt", "post.txt"} {
		f, err := implementRunOpen(filepath.Join(r.logs, name), false)
		if err != nil {
			return 1, err
		}
		logs[name] = f
	}
	code, pre, post := -1, -1, -1
	confirmed, agentAttempted, placement := true, false, false
	var reasons error
	var cleanup []string
	step := func(name string, action func() error) error {
		err := r.deps.step(name)
		if err == nil {
			err = action()
		}
		if err != nil {
			reasons = errors.Join(reasons, fmt.Errorf("%s: %w", name, err))
		}
		return err
	}
	var tmp string
	var signals <-chan os.Signal
	_, _ = r.deps.withSignals(func(caught <-chan os.Signal) (int, error) {
		signals = caught
		tmp, err = r.deps.mkdirTemp(filepath.Dir(r.tree), filepath.Base(r.tree)+".tmp-*")
		if err != nil {
			reasons = fmt.Errorf("creating TMPDIR: %w", err)
		} else {
			placement = true
			for _, file := range r.files() {
				if file.name == "agent" {
					agentAttempted = true
				}
				if step("place-"+file.name, func() error { return implementRunWrite(file.path, file.data) }) != nil {
					break
				}
			}
			if reasons == nil {
				_ = step("place-brain", func() error { return implementRunEmpty(r.brain()) })
			}
			if reasons == nil {
				pre = r.check(logs["pre.txt"])
				if pre != 0 {
					reasons = errors.New("pre-check failed")
				}
			}
			if reasons == nil {
				printLimit := r.limit - 2*time.Minute
				if printLimit <= 0 {
					printLimit = r.limit / 2
				}
				args := []string{"-p", r.prompt, "--model", r.model, "--agent", "argon-implementer", "--sandbox", "--print-timeout", printLimit.String(), "--output-format", "stream-json"}
				ops, started := r.deps.ops, false
				wait := ops.wait
				ops.wait = func(pid int, status *syscall.WaitStatus) (int, error) {
					started = true
					return wait(pid, status)
				}
				sup := supervisor{r.limit, 5 * time.Second, signals, ops}
				code, err = sup.run(r.agy, args, agyEnvironment(os.Environ(), r.home, tmp), r.tree, logs["out.jsonl"], logs["err.txt"])
				reasons = errors.Join(reasons, err)
				if !started {
					code = -1
				}
				// Any supervisor error after start is conservatively unconfirmed;
				// starting failure alone has no session that could use these paths.
				confirmed = !started || err == nil
			}
		}
		if confirmed && placement {
			for _, file := range r.files()[:2] {
				name := "restore-" + file.name
				err := step(name, func() error { return implementRunWrite(file.path, file.data) })
				cleanup = append(cleanup, name+"="+strconv.FormatBool(err == nil))
			}
			for _, task := range []struct {
				name string
				run  func() error
			}{
				{"empty-brain", func() error { return implementRunEmpty(r.brain()) }},
				{"remove-tmp", func() error { return errors.Join(implementRunEmpty(tmp), implementRunRemove(tmp, true)) }},
			} {
				err := step(task.name, task.run)
				cleanup = append(cleanup, task.name+"="+strconv.FormatBool(err == nil))
			}
			post = r.check(logs["post.txt"])
			if post != 0 {
				reasons = errors.Join(reasons, errors.New("post-check failed"))
			}
			if agentAttempted {
				agent := r.files()[4].path
				err := step("remove-agent", func() error { return implementRunRemove(agent, false) })
				cleanup = append(cleanup, "remove-agent="+strconv.FormatBool(err == nil))
				err = step("remove-agents", func() error { return implementRunRemove(filepath.Dir(agent), true) })
				cleanup = append(cleanup, "remove-agents="+strconv.FormatBool(err == nil))
			}
		}
		if !confirmed {
			reasons = errors.Join(reasons, errors.New("manual recovery needed: session stop unconfirmed; normal completion stopped"))
		}
		if confirmed && code >= 0 {
			transcript, readErr := implementRunReadLog(logs["out.jsonl"])
			diagnostics, stderrErr := implementRunReadLog(logs["err.txt"])
			_, judgeErr := judge(code, transcript, diagnostics)
			reasons = errors.Join(reasons, readErr, stderrErr, judgeErr)
		}
		return code, reasons
	})
	// withSignals has stopped catching before the final drain and status/output
	// write, just like review.finish. A blocked output must not swallow INT/TERM.
	select {
	case sig := <-signals:
		code = 128 + int(sig.(syscall.Signal))
		reasons = errors.Join(reasons, fmt.Errorf("interrupted by %v", sig))
	default:
	}
	status := fmt.Sprintf("exit=%d\ncounts=%t\npre-check=%d\npost-check=%d\nstop-confirmed=%t\nsession-left=%s\n", code, reasons == nil, pre, post, confirmed, map[bool]string{true: "0", false: "unknown"}[confirmed])
	for _, line := range cleanup {
		status += "cleanup-" + line + "\n"
	}
	if reasons != nil {
		// Quoting keeps newline-containing paths/errors in one key=value line.
		for _, reason := range strings.Split(reasons.Error(), "\n") {
			status += "reason=" + strconv.Quote(reason) + "\n"
		}
		status += "recovery=" + strconv.Quote("inspect cleanup results; retry checks before another run") + "\n"
	}
	if !confirmed {
		for _, path := range []string{tmp, r.files()[4].path, r.files()[0].path, r.files()[1].path, r.files()[2].path, r.files()[3].path, r.brain()} {
			status += "manual-recovery-needed=" + strconv.Quote(path) + "\n"
		}
	} else if reasons != nil {
		for _, path := range []string{tmp, r.files()[4].path, r.files()[0].path, r.files()[1].path, r.brain()} {
			if path != "" {
				status += "recovery-inspect=" + strconv.Quote(path) + "\n"
			}
		}
	}
	reasons = errors.Join(reasons, implementRunSaveStatus(logs["status"], status))
	_, outputErr := io.WriteString(out, status)
	reasons = errors.Join(reasons, outputErr)
	if outputErr != nil {
		status = strings.Replace(status, "counts=true\n", "counts=false\n", 1)
		status += "reason=" + strconv.Quote("status output failed: "+outputErr.Error()) + "\n"
		reasons = errors.Join(reasons, implementRunSaveStatus(logs["status"], status))
	}
	if reasons != nil {
		return max(code, 1), reasons
	}
	return 0, nil
}

func implementRunSaveStatus(f *os.File, status string) error {
	_, err := f.WriteAt([]byte(status), 0)
	return errors.Join(err, f.Truncate(int64(len(status))), f.Sync())
}

func (r *implementRun) check(out *os.File) int {
	args := []string{"--home", r.home, "--worktree", r.tree, "--git-file", r.saved, "--shared-settings-sha256", r.hash, "--agy", r.agy}
	code := r.deps.check(args, out, out)
	if err := out.Sync(); err != nil {
		return 1
	}
	return code
}

func implementRunReadLog(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// Like the checks, refuse excessive output rather than allocate unbounded
	// memory while judging an untrusted transcript.
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if len(data) > 16<<20 {
		return nil, errors.New("agy log exceeds 16 MiB judgement cap")
	}
	return data, err
}
