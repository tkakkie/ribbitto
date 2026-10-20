package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type procInfo struct {
	pid, ppid, uid int
	start, command string
}
type processTable interface {
	list() ([]procInfo, error)
	read(int) (procInfo, bool, error)
	signal(int, syscall.Signal) error
}
type psTable struct {
	run func(...string) ([]procInfo, error)
}

func parseSweepProcesses(data string) ([]procInfo, error) {
	var result []procInfo
	for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		if p, ok := parseSweepProcessHeader(line); ok {
			result = append(result, p)
			continue
		}
		if len(result) == 0 {
			if strings.TrimSpace(line) == "" {
				continue
			}
			return nil, errors.New("invalid process listing")
		}
		// ps prints literal newlines in argv. Only the fixed prefix can start a
		// row; row-looking argv is checked again by single-PID reads before use.
		result[len(result)-1].command += "\n" + line
	}
	return result, nil
}

func parseSweepProcessHeader(line string) (procInfo, bool) {
	// Preserve command whitespace for identity checks between reads and signals.
	fields := make([]string, 0, 8)
	rest := line
	for range 8 {
		rest = strings.TrimLeft(rest, " \t")
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			return procInfo{}, false
		}
		fields = append(fields, rest[:end])
		rest = rest[end:]
	}
	pid, e1 := strconv.Atoi(fields[0])
	ppid, e2 := strconv.Atoi(fields[1])
	uid, e3 := strconv.Atoi(fields[2])
	start := strings.Join(fields[3:], " ")
	if _, err := time.Parse("Mon Jan 2 15:04:05 2006", start); err != nil || e1 != nil || e2 != nil || e3 != nil || pid <= 0 || ppid < 0 || uid < 0 || strings.TrimSpace(rest) == "" {
		return procInfo{}, false
	}
	return procInfo{pid, ppid, uid, start, strings.TrimLeft(rest, " \t")}, true
}
func sweepPS(args ...string) ([]procInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", args...)
	cmd.Env = dockerEnvironment()
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	out := containerOutput{cancel: cancel}
	cmd.Stdout = out.stdoutWriter()
	err := cmd.Run()
	// ps -p returns 1 with empty output when the process has gone away.
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && out.stdout.Len() == 0 && len(args) > 0 && args[0] == "-p" {
		return nil, nil
	}
	if err != nil || out.over || ctx.Err() != nil {
		return nil, errors.New("process listing failed")
	}
	return parseSweepProcesses(out.stdout.String())
}
func (p psTable) query(args ...string) ([]procInfo, error) {
	if p.run != nil {
		return p.run(args...)
	}
	return sweepPS(args...)
}
func (p psTable) list() ([]procInfo, error) {
	return p.query("-axww", "-o", "pid=,ppid=,uid=,lstart=,command=")
}
func (p psTable) read(pid int) (procInfo, bool, error) {
	procs, err := p.query("-p", strconv.Itoa(pid), "-ww", "-o", "pid=,ppid=,uid=,lstart=,command=")
	if err != nil {
		return procInfo{}, false, err
	}
	if len(procs) == 0 {
		return procInfo{}, false, nil
	}
	if len(procs) != 1 || procs[0].pid != pid {
		return procInfo{}, false, errors.New("invalid process identity")
	}
	return procs[0], true, nil
}
func (psTable) signal(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

func dockerClientName(command string) string {
	first, rest, ok := strings.Cut(command, " ")
	if !ok || !slices.Contains(dockerExecutablePaths(""), first) {
		return ""
	}
	rest, ok = strings.CutPrefix(rest, "run --pull=never --name ")
	if !ok {
		return ""
	}
	name, rest, ok := strings.Cut(rest, " ")
	if !ok || !sweepContainerName.MatchString(name) || !strings.HasPrefix(rest, "--network none ") {
		return ""
	}
	return name
}
func (s *containerSweep) orphan(p procInfo) bool {
	return p.pid > 0 && p.ppid == 1 && p.uid == s.uid() && dockerClientName(p.command) != ""
}
func (s *containerSweep) sameClient(p procInfo) (bool, error) {
	now, exists, err := s.processes.read(p.pid)
	return exists && now.pid == p.pid && s.orphan(now) && now.start == p.start && now.command == p.command, err
}
func (s *containerSweep) terminate(p procInfo) error {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		same, err := s.sameClient(p)
		if err != nil || !same {
			return err
		}
		if err := s.processes.signal(p.pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("signalling client: %w", err)
		}
		deadline := s.now().Add(5 * time.Second)
		for s.now().Before(deadline) {
			same, err := s.sameClient(p)
			if err != nil || !same {
				return err
			}
			s.sleep(100 * time.Millisecond)
		}
	}
	same, err := s.sameClient(p)
	if err != nil {
		return err
	}
	if same {
		return errors.New("client survived")
	}
	return nil
}
func (s *containerSweep) clients() {
	procs, err := s.processes.list()
	if err != nil {
		s.fail("listing clients")
		return
	}
	for _, p := range procs {
		if !s.orphan(p) {
			continue
		}
		s.line("leftover client %d", p.pid)
		if s.dry {
			s.fail("detecting leftover client")
			continue
		}
		if err := s.terminate(p); err != nil {
			s.unreclaimed("client", strconv.Itoa(p.pid))
			continue
		}
		s.line("reclaimed client %d", p.pid)
	}
}
