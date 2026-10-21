package main

import (
	"crypto/sha256"
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
	for _, name := range []string{"status", "out.jsonl", "err.txt", "pre.txt", "post.txt", "at-rest.txt"} {
		f, err := implementRunOpen(filepath.Join(r.logs, name), false)
		if err != nil {
			return 1, err
		}
		logs[name] = f
	}
	code, pre, post := -1, -1, -1
	confirmed, agentAttempted, placement, agyStarted := true, false, false, false
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
		if step("mark-in-progress", r.markRecovery) != nil {
			return 1, reasons
		}
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
				reasons = r.agyOutside(tmp)
			}
			if reasons == nil {
				_ = step("pre-check", func() error {
					pre = r.check(logs["pre.txt"])
					return nil
				})
				if pre != 0 && reasons == nil {
					reasons = errors.New("pre-check failed")
				}
			}
			if reasons == nil {
				select {
				case sig := <-signals:
					code = 128 + int(sig.(syscall.Signal))
					reasons = fmt.Errorf("interrupted by %v", sig)
				default:
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
				code, err = sup.run(r.agy, args, r.environment(tmp), r.tree, logs["out.jsonl"], logs["err.txt"])
				agyStarted = started
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
			// Preserve what the session ran with before any restoration hides it.
			reasons = errors.Join(reasons, r.observe(logs["post.txt"]))
			_ = step("post-check", func() error {
				post = r.check(logs["post.txt"])
				if post != 0 {
					return errors.New("post-check failed")
				}
				return nil
			})
			restored := r.files()[:3]
			restored[0].data, restored[1].data, restored[2].data = r.offSettings, r.offHooks, r.offMCP
			for _, file := range restored {
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
			if agentAttempted {
				agent := r.files()[4].path
				err := step("remove-agent", func() error { return implementRunRemove(agent, false) })
				cleanup = append(cleanup, "remove-agent="+strconv.FormatBool(err == nil))
				err = step("remove-agents", func() error { return implementRunRemove(filepath.Dir(agent), true) })
				cleanup = append(cleanup, "remove-agents="+strconv.FormatBool(err == nil))
			}
		}
		if confirmed {
			_ = step("at-rest-check", func() error {
				code := r.checkState("off", logs["at-rest.txt"])
				syncErr := r.deps.syncLog(logs["at-rest.txt"])
				var checkErr error
				if code != 0 {
					checkErr = errors.New("at-rest check failed; inspect restored settings, hooks, MCP and home")
				}
				if syncErr != nil {
					syncErr = fmt.Errorf("syncing at-rest log: %w", syncErr)
				}
				return errors.Join(checkErr, syncErr)
			})
		}
		if !confirmed {
			reasons = errors.Join(reasons, errors.New("manual recovery needed: session stop unconfirmed; normal completion stopped"))
		}
		if confirmed && code >= 0 && agyStarted {
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
	status := r.status(code, reasons, pre, post, confirmed, cleanup, tmp)
	if err := r.deps.saveStatus(logs["status"], status); err != nil {
		reasons = errors.Join(reasons, fmt.Errorf("log/status save failed: %w", err))
		status = r.status(code, reasons, pre, post, confirmed, cleanup, tmp)
		if err := r.deps.saveStatus(logs["status"], status); err != nil {
			reasons = errors.Join(reasons, fmt.Errorf("log/status retry failed: %w", err))
			status = r.status(code, reasons, pre, post, confirmed, cleanup, tmp)
		}
	}
	_, outputErr := io.WriteString(out, status)
	if outputErr != nil {
		reasons = errors.Join(reasons, fmt.Errorf("status output failed: %w", outputErr))
		status = r.status(code, reasons, pre, post, confirmed, cleanup, tmp)
		if err := r.deps.saveStatus(logs["status"], status); err != nil {
			reasons = errors.Join(reasons, fmt.Errorf("log/status after output failed: %w", err))
		}
	}
	if reasons == nil {
		err := r.deps.step("remove-marker")
		if err == nil {
			err = r.clearRecovery()
		}
		if err == nil {
			// Output must also succeed before committing the final status line.
			_, err = io.WriteString(out, "in-progress-marker=removed\n")
			if err == nil {
				err = implementRunCompleteStatus(logs["status"], r.deps.syncLog)
			}
			if err != nil {
				// CompleteStatus rolls back an unsuccessful append before the
				// refusal marker can reappear, so it cannot claim removal.
				err = errors.Join(err, r.markRecovery())
			}
		}
		if err != nil {
			reasons = fmt.Errorf("in-progress marker completion failed: %w", err)
			status = r.status(code, reasons, pre, post, confirmed, cleanup, tmp)
			reasons = errors.Join(reasons, r.deps.saveStatus(logs["status"], status))
			_, outputErr := io.WriteString(out, status)
			reasons = errors.Join(reasons, outputErr)
		}
	}
	if reasons != nil {
		return max(code, 1), reasons
	}
	return 0, nil
}

func (r *implementRun) status(code int, reasons error, pre, post int, confirmed bool, cleanup []string, tmp string) string {
	status := fmt.Sprintf("exit=%d\ncounts=%t\npre-check=%d\npost-check=%d\nstop-confirmed=%t\nsession-left=%s\n", code, reasons == nil, pre, post, confirmed, map[bool]string{true: "0", false: "unknown"}[confirmed])
	status += "verify=" + r.verify + "\n"
	markerState := "remains"
	if _, err := os.Lstat(r.recoveryMarker()); errors.Is(err, os.ErrNotExist) {
		markerState = "absent"
	} else if err != nil {
		markerState = "unknown"
	}
	status += "in-progress-marker=" + markerState + "\nin-progress-marker-path=" + strconv.Quote(r.recoveryMarker()) + "\n"
	for _, file := range r.files()[:3] {
		status += fmt.Sprintf("generated-%s-sha256=%x\n", file.name, sha256.Sum256(file.data))
	}
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
	if reasons != nil {
		status += "recovery-marker=" + strconv.Quote(r.recoveryMarker()) + "\n"
	}
	if !confirmed {
		for _, path := range []string{tmp, r.files()[4].path, r.files()[0].path, r.files()[1].path, r.files()[2].path, r.files()[3].path, r.brain()} {
			status += "manual-recovery-needed=" + strconv.Quote(path) + "\n"
		}
	} else if reasons != nil {
		for _, path := range []string{tmp, r.files()[4].path, r.files()[0].path, r.files()[1].path, r.files()[2].path, r.brain()} {
			if path != "" {
				status += "recovery-inspect=" + strconv.Quote(path) + "\n"
			}
		}
	}

	return status
}

func implementRunSaveStatus(f *os.File, status string) error {
	_, err := f.WriteAt([]byte(status), 0)
	return errors.Join(err, f.Truncate(int64(len(status))), f.Sync())
}

func (r *implementRun) check(out *os.File) int {
	code := r.checkState(r.verify, out)
	if err := r.deps.syncLog(out); err != nil {
		return 1
	}
	return code
}

func (r *implementRun) observe(out *os.File) error {
	var result error
	for _, file := range r.files()[:3] {
		data, err := implementRead(file.path, 1<<20, false)
		if err != nil {
			_, writeErr := fmt.Fprintf(out, "observed-%s-read-error=%q\n", file.name, err)
			result = errors.Join(result, fmt.Errorf("observing %s: %w", file.name, err), writeErr)
			continue
		}
		_, err = fmt.Fprintf(out, "observed-%s-sha256=%x\n", file.name, sha256.Sum256(data))
		result = errors.Join(result, err)
	}
	return errors.Join(result, r.deps.syncLog(out))
}

func implementRunCompleteStatus(f *os.File, syncLog func(*os.File) error) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	_, writeErr := f.WriteAt([]byte("in-progress-marker=removed\n"), info.Size())
	if err := errors.Join(writeErr, syncLog(f)); err != nil {
		// A failed durable append must not leave a success line when the
		// caller recreates the refusal marker.
		return errors.Join(err, f.Truncate(info.Size()), syncLog(f))
	}
	return nil
}

func (r *implementRun) checkState(state string, out io.Writer) int {
	args := []string{"--home", r.home, "--worktree", r.tree, "--git-file", r.saved, "--shared-settings-sha256", r.hash, "--agy", r.agy, "--verify", state}
	if state == "on" {
		args = append(args, "--trusted-source", r.source)
	}
	return r.deps.check(args, out, out)
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
