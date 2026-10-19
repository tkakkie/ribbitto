package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// containerGoImage is official golang:1.27.2. Change this digest and
	// containerGoVersion together so the copy cannot request another toolchain.
	containerGoImage        = "golang@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8"
	containerGoVersion      = "1.27.2"
	containerTimeout        = 10 * time.Minute
	containerCleanupTimeout = 20 * time.Second
	containerMemory         = "2g"
	containerPIDs           = "256"
	containerCPUs           = "2"
	containerOutputLimit    = 4 << 20
	containerFileLimit      = 20000
	containerCopyLimit      = 128 << 20
	containerUser           = "1000:1000"
	// Provision this cache from trusted host module downloads. A fixed path
	// avoids GOPATH, GOENV, HOME or a worktree selecting an arbitrary host mount.
	containerModuleCache = "/var/cache/argonreview/go-mod"
	containerPath        = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"
	containerScope       = "Race detector was not run. Cgo-dependent packages and database/browser tests are CI-only."
)

// Only local patterns are admitted; ... is allowed only as the final segment.
var containerPattern = regexp.MustCompile(`^(\.|\./\.\.\.|\./[A-Za-z0-9_][A-Za-z0-9_-]*(/[A-Za-z0-9_][A-Za-z0-9_-]*)*(/\.\.\.)?)$`)

func validateContainerCheck(tree, check, pattern string) error {
	if tree == "" || strings.ContainsRune(tree, 0) {
		return errors.New("a worktree path is required")
	}
	if check != "build" && check != "vet" && check != "test" {
		return errors.New("check must be build, vet or test")
	}
	if !containerPattern.MatchString(pattern) {
		return errors.New("package pattern is outside the local path allowlist")
	}
	return nil
}

// Docker receives neither the caller's context/configuration nor credentials.
// Only the conventional local socket is used; alternate daemons need a code
// change. The public digest is preloaded; --pull=never keeps this path offline.
func dockerEnvironment() []string {
	return []string{"PATH=" + containerPath, "HOME=/nonexistent", "DOCKER_CONFIG=/nonexistent", "DOCKER_HOST=unix:///var/run/docker.sock"}
}

func containerArguments(copyDir, name, check, pattern string) []string {
	// Explicit removal below replaces --rm: verification waits for Docker to
	// acknowledge removal, and interruption cannot race automatic removal.
	args := []string{"run", "--pull=never", "--name", name,
		"--network", "none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user", containerUser,
		// Both tmpfs mounts allow execution: Go executes test binaries from its
		// temporary build/cache directories. nosuid/nodev still apply.
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777",
		"--tmpfs", "/home/check:rw,exec,nosuid,nodev,size=1g,uid=1000,gid=1000,mode=0700",
		"--mount", "type=bind,src=" + containerModuleCache + ",dst=/go/pkg/mod,readonly",
		"--mount", "type=bind,src=" + copyDir + ",dst=/work,readonly",
		"-w", "/work", "-e", "GOPROXY=off", "-e", "GOTOOLCHAIN=local", "-e", "GOWORK=off", "-e", "GOFLAGS=-mod=readonly", "-e", "CGO_ENABLED=0",
		"-e", "HOME=/home/check", "-e", "GOCACHE=/home/check/cache", "-e", "GOTMPDIR=/tmp", "-e", "GOMODCACHE=/go/pkg/mod", "-e", "GOPATH=/go",
		"--memory", containerMemory, "--memory-swap", containerMemory, "--pids-limit", containerPIDs, "--cpus", containerCPUs,
		containerGoImage, "go", check}
	if check == "build" {
		args = append(args, "-o", "/dev/null")
	}
	if check == "test" {
		args = append(args, "-count=1", "-json")
	}
	return append(args, pattern)
}

type containerRunner interface {
	run(context.Context, []string, []string, io.Writer, io.Writer) (int, error)
}

type dockerRunner struct{}

func (dockerRunner) run(ctx context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
	// Look up only in trusted directories, never the caller's PATH or cwd.
	var binary string
	for _, dir := range filepath.SplitList(containerPath) {
		if path, err := exec.LookPath(filepath.Join(dir, "docker")); err == nil {
			binary = path
			break
		}
	}
	if binary == "" {
		return -1, errors.New("docker is absent from the fixed executable path")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = "/", env, stdout, stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("running docker: %w", err)
	}
	return 0, nil
}

func containerCheckCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || validateContainerCheck(args[0], args[1], args[2]) != nil {
		_, _ = fmt.Fprintln(stderr, "usage: argonreview container-check <worktree> <build|vet|test> <package-pattern>")
		return 2
	}
	info, ok := debug.ReadBuildInfo()
	_, err := trusted(info, ok, func() (string, error) { return sourceRevision(os.Getenv("RIBBITTO_ARGON_SOURCE"), git) })
	code := 1
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var report string
		report, err = runContainerCheck(ctx, args[0], args[1], args[2], dockerRunner{}, "/tmp")
		// Cleanup has completed before writing to a potentially closed pipe.
		if _, writeErr := io.WriteString(stdout, report); writeErr != nil {
			err = errors.Join(err, fmt.Errorf("writing container report: %w", writeErr))
		}
		if err == nil && strings.HasPrefix(report, "verified\n") {
			code = 0
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview: %v\n", err)
	}
	return code
}

func runContainerCheck(parent context.Context, tree, check, pattern string, runner containerRunner, copyParent string) (report string, err error) {
	if err := validateContainerCheck(tree, check, pattern); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, containerTimeout)
	defer cancel()
	tree, err = filepath.Abs(tree)
	if err != nil {
		return "", fmt.Errorf("resolving worktree: %w", err)
	}
	// The command fixes copyParent to /tmp rather than caller-controlled
	// TMPDIR; tests use private parents so concurrent runs cannot interfere.
	// The private parent stays 0700; only its tree is accessible to containerUser.
	dir, err := os.MkdirTemp(copyParent, "argonreview-container-*")
	if err != nil {
		return "", fmt.Errorf("creating source copy: %w", err)
	}
	defer func() {

		if removeErr := os.RemoveAll(dir); removeErr != nil {
			report = "FAILED: source cleanup failed\n" + containerScope + "\n"
			err = errors.Join(err, fmt.Errorf("removing source copy: %w", removeErr))
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(err, fmt.Errorf("container check interrupted: %w", ctxErr))
			if strings.HasPrefix(report, "verified\n") {
				report = "FAILED" + strings.TrimPrefix(report, "verified")
			}
		}
	}()
	copyDir := filepath.Join(dir, "tree")
	if err := copyContainerSource(ctx, tree, copyDir, listContainerFiles, nil); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(copyDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("reading copied go.mod: %w", err)
	}
	if err := checkContainerGoVersion(data); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("preparing container: %w", err)
	}
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("naming container: %w", err)
	}
	name := fmt.Sprintf("argonreview-check-%x", suffix)
	output := &containerOutput{cancel: cancel}
	code, runErr := runner.run(ctx, containerArguments(copyDir, name, check, pattern), dockerEnvironment(), output.stdoutWriter(), output.stderrWriter())
	return finishContainerRun(ctx, runner, name, check, code, runErr, output)
}

// Cleanup uses independent contexts, so a canceled run cannot suppress either
// operation. Explicit removal also confirms cleanup after a normal exit; an
// unavailable daemon or unconfirmed removal must fail closed.
func finishContainerRun(ctx context.Context, runner containerRunner, name, check string, code int, runErr error, output *containerOutput) (string, error) {
	err := errors.Join(runErr, ctx.Err())
	for _, args := range [][]string{{"stop", "--time", "2", name}, {"rm", "-f", name}} {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
		status, cleanupErr := runner.run(cleanupCtx, args, dockerEnvironment(), io.Discard, io.Discard)
		cancel()
		// Removal still runs after a failed stop, and a successful force-removal
		// suffices even when stop could not reach the container.
		if args[0] == "rm" && (status != 0 || cleanupErr != nil) {
			if cleanupErr == nil {
				cleanupErr = fmt.Errorf("docker rm exited with status %d", status)
			}
			if code == 125 || runErr != nil {
				err = errors.Join(err, fmt.Errorf("container cleanup unconfirmed (docker run failed; container may never have existed): %w", cleanupErr))
			} else {
				err = errors.Join(err, fmt.Errorf("container removal failed: %w", cleanupErr))
			}
		}
	}
	err = errors.Join(err, ctx.Err())
	verdict := commandVerdict(code, err, output.over)
	if check == "test" {
		verdict = testVerdict(code, err, output.stdout.Bytes(), output.over)
	}
	if output.over {
		verdict = "NOT VERIFIED: output limit exceeded (output cut off)"
	}
	return fmt.Sprintf("%s\n%s\n--- stdout\n%s\n--- stderr\n%s\n", verdict, containerScope, output.stdout.Bytes(), output.stderr.Bytes()), err
}

func checkContainerGoVersion(data []byte) error {
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "go" {
			continue
		}
		if found || len(fields) < 2 || fields[1] != containerGoVersion || len(fields) > 2 && !strings.HasPrefix(fields[2], "//") {
			return fmt.Errorf("copied go.mod must have exactly go %s", containerGoVersion)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("copied go.mod must have go %s", containerGoVersion)
	}
	return nil
}

// One budget covers both streams. exec can write them concurrently; excess
// output is discarded and cancels the run without allocating beyond the cap.
type containerOutput struct {
	mu             sync.Mutex
	stdout, stderr bytes.Buffer
	over           bool
	cancel         context.CancelFunc
}

type containerStream struct {
	output *containerOutput
	stderr bool
}

func (o *containerOutput) stdoutWriter() containerStream { return containerStream{output: o} }
func (o *containerOutput) stderrWriter() containerStream {
	return containerStream{output: o, stderr: true}
}

func (s containerStream) Write(p []byte) (int, error) {
	o := s.output
	o.mu.Lock()
	defer o.mu.Unlock()
	size := min(len(p), containerOutputLimit-o.stdout.Len()-o.stderr.Len())
	buf := &o.stdout
	if s.stderr {
		buf = &o.stderr
	}
	_, _ = buf.Write(p[:size])
	if size < len(p) {
		o.over = true
		o.cancel()
	}
	return len(p), nil
}
