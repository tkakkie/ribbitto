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
	// The recipe pins the base; runs and sweeps pin the built image recorded on #1697.
	containerBaseImage      = "golang@sha256:6928d94d1142af58816fed5a62338d605a266ccd53a0deaab6b1096f19059261"
	containerVerifyImage    = "sha256:12c5636332bebbfbc6f734bac9c7bb59b3bdd9ac9e4ed373beb180fca8e79e80"
	containerGoVersion      = "1.27.3"
	containerTemplVersion   = "v0.3.1070"
	containerSQLCVersion    = "v1.31.1"
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
	containerModuleCache      = "/var/cache/argonreview/go-mod"
	containerPath             = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"
	containerCopyParent       = "/tmp"
	containerOwnerLabel       = "argonreview.owner"
	containerOwnerValue       = "verify"
	containerNamePattern      = `^argonreview-check-[0-9a-f]{32}$`
	containerTimeLimitVerdict = "NOT VERIFIED: time limit exceeded"
)

// A single-run container has a fresh /tmp. Copy dotfiles too, keeping /work read-only.
// Generator stdout must not contaminate the test2json stream.
const (
	containerCopyScript = `set -eu
mkdir /tmp/work
cp -R /work/. /tmp/work
cd /tmp/work
`
	containerTemplCommand = "/usr/local/bin/templ generate"
	containerSQLCCommand  = "/usr/local/bin/sqlc generate"
	containerCSSCommand   = "/usr/local/bin/tailwindcss -i web/styles/app.css -o web/static/css/app.css --minify"
	containerPrelude      = containerCopyScript + containerTemplCommand + " >&2\n" + containerSQLCCommand + " >&2\n" + containerCSSCommand + " >&2\n" + `exec go "$@"
`
	containerGeneratedScript = containerCopyScript + containerTemplCommand + " >&2 || exit 21\n" + containerSQLCCommand + " >&2 || exit 21\n" + containerCSSCommand + " >&2 || exit 21\n" + `templ fmt -fail . >&2 || exit 22
names=$(gofmt -l .) || exit 24
if [ -n "$names" ]; then
 printf '%s\n' "$names"
 exit 23
fi
status=0
diff -ru /work /tmp/work || status=$?
case "$status" in
 0) exit 0 ;;
 1) exit 25 ;;
 *) exit 26 ;;
esac
`
)

func containerCheckScope(check string) string {
	const limits = "Race detector was not run. Cgo-dependent packages and database/browser tests are CI-only."
	switch check {
	case "test-db":
		return "Race detector was not run. Cgo-dependent packages and browser tests are CI-only. Ran on regenerated sources with PostgreSQL 18 and the trusted measurement-skip allowlist; the generated check covers committed generated files. test-db is merged but not in force until #1700. Whether ./... fits the unchanged container limits is unestablished."
	case "generated":
		return "Checks templ, sqlc and Tailwind output against committed files, plus templ fmt and gofmt, in a copy. Report only: no write-back; nothing written to the worktree. tbls docs/schema, docs/dependencies.md, the API manifest, golangci-lint, docscheck, languagecheck, race detector, database and browser tests stay CI-only."
	case "build", "vet", "test":
		return limits + " Ran on regenerated sources (templ, sqlc, Tailwind); the generated check covers whether the committed generated files are current."
	default:
		return limits
	}
}

// Only local patterns are admitted; ... is allowed only as the final segment.
var containerPattern = regexp.MustCompile(`^(\.|\./\.\.\.|\./[A-Za-z0-9_][A-Za-z0-9_-]*(/[A-Za-z0-9_][A-Za-z0-9_-]*)*(/\.\.\.)?)$`)

func validateContainerCheck(tree, check, pattern string) error {
	if tree == "" || strings.ContainsRune(tree, 0) {
		return errors.New("a worktree path is required")
	}
	if check != "build" && check != "vet" && check != "test" && check != "generated" && check != "test-db" {
		return errors.New("check must be build, vet, test, generated or test-db")
	}
	if check == "generated" && pattern != "./..." {
		return errors.New("generated requires the whole-tree package (./...)")
	}
	if !containerPattern.MatchString(pattern) {
		return errors.New("package pattern is outside the local path allowlist")
	}
	return nil
}

// Docker receives neither the caller's context/configuration nor credentials.
// Only the conventional local socket is used; alternate daemons need a code
// change. The pinned image is preloaded; --pull=never keeps this path offline.
func dockerEnvironment() []string {
	return []string{"PATH=" + containerPath, "HOME=/nonexistent", "DOCKER_CONFIG=/nonexistent", "DOCKER_HOST=unix:///var/run/docker.sock"}
}

func containerArguments(copyDir, name, check, pattern string) []string {
	// Explicit removal below replaces --rm: verification waits for Docker to
	// acknowledge removal, and interruption cannot race automatic removal.
	args := []string{"run", "--pull=never", "--name", name,
		"--network", "none", "--label", containerOwnerLabel + "=" + containerOwnerValue, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user", containerUser,
		// Both tmpfs mounts allow execution: Go executes test binaries from its
		// temporary build/cache directories. nosuid/nodev still apply.
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777",
		"--tmpfs", "/home/check:rw,exec,nosuid,nodev,size=1g,uid=1000,gid=1000,mode=0700",
		"--mount", "type=bind,src=" + containerModuleCache + ",dst=/go/pkg/mod,readonly",
		"--mount", "type=bind,src=" + copyDir + ",dst=/work,readonly",
		"-w", "/work", "-e", "GOPROXY=off", "-e", "GOTOOLCHAIN=local", "-e", "GOWORK=off", "-e", "GOFLAGS=-mod=readonly", "-e", "CGO_ENABLED=0",
		"-e", "HOME=/home/check", "-e", "GOCACHE=/home/check/cache", "-e", "GOTMPDIR=/tmp", "-e", "GOMODCACHE=/go/pkg/mod", "-e", "GOPATH=/go"}
	if check == "test-db" {
		args = append(args, "-e", containerTestDatabaseURL, "-e", containerRequireDB)
	}
	args = append(args, "--memory", containerMemory, "--memory-swap", containerMemory, "--pids-limit", containerPIDs, "--cpus", containerCPUs,
		containerVerifyImage, "sh", "-c")
	if check == "generated" {
		return append(args, containerGeneratedScript)
	}
	if check == "test-db" {
		return append(args, containerTestDBScript, "sh", pattern)
	}
	if check == "writeback" {
		return append(args, containerGenerateScript)
	}
	args = append(args, containerPrelude, "sh", check)
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

type dockerRunner struct {
	ownGroup bool
	path     string
}

func dockerExecutablePaths(searchPath string) []string {
	if searchPath == "" {
		searchPath = containerPath
	}
	var paths []string
	for _, dir := range filepath.SplitList(searchPath) {
		paths = append(paths, filepath.Join(dir, "docker"))
	}
	return paths
}

func (r dockerRunner) run(ctx context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
	// Look up only in trusted directories, never the caller's PATH or cwd.
	var binary string
	for _, candidate := range dockerExecutablePaths(r.path) {
		if path, err := exec.LookPath(candidate); err == nil {
			binary = path
			break
		}
	}
	if binary == "" {
		return -1, errors.New("docker is absent from the fixed executable path")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = "/", env, stdout, stderr
	// The MCP server must kill descendants too when its context ends; a
	// surviving docker child must not keep a killed server's call alive.
	if r.ownGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	}
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
		_, _ = fmt.Fprintln(stderr, "usage: argonreview container-check <worktree> <build|vet|test|generated|test-db> <package-pattern>")
		return 2
	}
	info, ok := debug.ReadBuildInfo()
	_, err := trusted(info, ok, func() (string, error) { return sourceRevision(os.Getenv("RIBBITTO_ARGON_SOURCE"), git) })
	return startContainerCheck(args, stdout, stderr, err, sweepContainers, dockerRunner{}, containerCopyParent)
}

func startContainerCheck(args []string, stdout, stderr io.Writer, err error, sweep func(io.Writer) error, runner containerRunner, copyParent string) int {
	code := 1
	if err == nil {
		err = sweep(stderr)
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var report string
		report, err = runContainerCheck(ctx, args[0], args[1], args[2], runner, copyParent)
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
	result := containerExecution{timeout: containerTimeout}
	return runContainerExecution(parent, tree, check, pattern, runner, copyParent, &result)
}

func runContainerSummary(parent context.Context, tree, check, pattern string, runner containerRunner, copyParent string, timeout time.Duration) (string, error) {
	if err := validateContainerCheck(tree, check, pattern); err != nil {
		return "", err
	}
	result := containerExecution{timeout: timeout}
	_, err := runContainerExecution(parent, tree, check, pattern, runner, copyParent, &result)
	return result.summary, err
}

type containerExecution struct {
	timeout time.Duration
	output  containerOutput
	code    int
	summary string
}

// Both commands share source copying, flags, limits and confirmed cleanup.
// Callers validate their own interface before reaching this private executor.
func runContainerExecution(parent context.Context, tree, check, pattern string, runner containerRunner, copyParent string, result *containerExecution) (report string, err error) {
	timeout := result.timeout
	if timeout == 0 {
		timeout = containerTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
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
	lock, lockErr := lockContainerCopy(dir)
	// Register before removal: the lock must outlive source cleanup.
	if lockErr == nil {
		defer func() { _ = lock.Close() }()
	}
	in := summaryInput{check: check, pattern: pattern, scope: containerCheckScope(check)}
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			in.scope = containerCheckScope("")
			report = fmt.Sprintf("FAILED: source cleanup failed\n%s check result could not be confirmed. %s\n", check, containerCheckScope(""))
			err = errors.Join(err, fmt.Errorf("removing source copy: %w", removeErr))
		}
		report, err = result.finishReport(ctx, report, err, in)
	}()
	if lockErr != nil {
		return "", fmt.Errorf("locking source copy: %w", lockErr)
	}
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
	data, err = os.ReadFile(filepath.Join(copyDir, "tools", "go.mod"))
	if err != nil {
		return "", fmt.Errorf("the verify image needs a rebuild: reading copied tools/go.mod: %w", err)
	}
	if err := checkContainerToolVersions(data); err != nil {
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
	output := &result.output
	output.cancel = cancel
	code, runErr := runner.run(ctx, containerArguments(copyDir, name, check, pattern), dockerEnvironment(), output.stdoutWriter(), output.stderrWriter())
	// Record completion before cleanup: a later deadline cannot undo a finished
	// container, while an earlier interruption makes its result unknowable.
	interrupted := ctx.Err() != nil
	completed := runErr == nil && !interrupted
	result.code = code
	return finishContainerRun(ctx, runner, name, check, code, runErr, completed, interrupted, output, &in)
}

// finishReport runs after source removal, so late interruption and cleanup
// failures govern both presentations of the same captured execution.
func (result *containerExecution) finishReport(ctx context.Context, report string, err error, in summaryInput) (string, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = errors.Join(err, fmt.Errorf("container check interrupted: %w", ctxErr))
		if strings.HasPrefix(report, "verified\n") {
			report = interruptedContainerVerdict(ctxErr) + strings.TrimPrefix(report, "verified")
		}
	}
	if in.check != "writeback" && report != "" {
		// Cleanup can change the first line, but test counts/allowed skips
		// still come from the original verdict, never the raw report.
		first, _, _ := strings.Cut(report, "\n")
		original, _, _ := strings.Cut(in.verdict, "\n")
		if first != original {
			in.verdict = first
		}
		in.runnerFailed = in.runnerFailed || err != nil
		in.facts = parseSummaryFacts(in.check, result.output.stdout.Bytes(), result.output.stderr.Bytes(), result.output.over)
		result.summary = buildContainerSummary(in)
	}
	return report, err
}

// Cleanup uses independent contexts, so a canceled run cannot suppress either
// operation. Explicit removal also confirms cleanup after a normal exit; an
// unavailable daemon or unconfirmed removal must fail closed.
func finishContainerRun(ctx context.Context, runner containerRunner, name, check string, code int, runErr error, completed, interrupted bool, output *containerOutput, in *summaryInput) (string, error) {
	err := errors.Join(runErr, ctx.Err())
	cleanupFailed := false
	for _, args := range [][]string{{"stop", "--time", "2", name}, {"rm", "-f", name}} {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
		status, cleanupErr := runner.run(cleanupCtx, args, dockerEnvironment(), io.Discard, io.Discard)
		cancel()
		// Removal still runs after a failed stop, and a successful force-removal
		// suffices even when stop could not reach the container.
		if args[0] == "rm" && (status != 0 || cleanupErr != nil) {
			cleanupFailed = true
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
	if check == "writeback" {
		return "", err
	}
	in.runnerFailed = runErr != nil || err != nil || code == 125
	verdict, stat, stdout := containerVerdict(check, code, err, output)
	deadline := errors.Is(ctx.Err(), context.DeadlineExceeded) && !output.over && !cleanupFailed
	if deadline {
		// One interruption rule governs host and MCP: an unfinished run or a
		// completed pass loses verification; other completed verdicts stay pinned.
		// Read the run context, since joined errors include independent cleanup.
		if completed {
			verdict, stat, stdout = containerVerdict(check, code, nil, output)
		}
		// A runner error returned with a healthy context keeps FAILED even if
		// cleanup outlives the deadline; only interruption during run is unknown.
		if interrupted || completed && strings.HasPrefix(verdict, "verified") {
			verdict = containerTimeLimitVerdict
		}
	}
	if output.over {
		verdict = "NOT VERIFIED: output limit exceeded (output cut off)"
	}
	in.verdict = verdict
	if check == "generated" {
		if !output.over && err == nil {
			switch code {
			case generatedDiffer:
				in.stat, in.hasStat = parseGeneratedStat(output.stdout.String())
				in.diff = output.stdout.String()
			case generatedGofmtNames:
				in.gofmtNames = strings.Split(strings.TrimSuffix(output.stdout.String(), "\n"), "\n")
			}
		}
		return fmt.Sprintf("%s\n%s%s\n--- stdout\n%s\n--- stderr\n%s\n", verdict, stat, containerCheckScope(check), stdout, output.stderr.Bytes()), err
	}
	return fmt.Sprintf("%s\n%s\n--- stdout\n%s\n--- stderr\n%s\n", verdict, containerCheckScope(check), output.stdout.Bytes(), output.stderr.Bytes()), err
}

// interruptedContainerVerdict is also applied after source removal: no context
// error may leave a pass verified, and only the run's deadline gets this label.
func interruptedContainerVerdict(ctxErr error) string {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return containerTimeLimitVerdict
	}
	return "FAILED"
}

func containerVerdict(check string, code int, err error, output *containerOutput) (string, string, string) {
	switch check {
	case "generated":
		return generatedVerdict(code, err, output.stdout.String(), output.over)
	case "test":
		return testVerdict(code, err, output.stdout.Bytes(), output.over), "", ""
	case "test-db":
		return testVerdictWithAllowlist(code, err, output.stdout.Bytes(), output.over, measurementSkips()), "", ""
	default:
		return commandVerdict(code, err, output.over), "", ""
	}
}

func checkContainerToolVersions(data []byte) error {
	versions := map[string]string{"github.com/a-h/templ": containerTemplVersion, "github.com/sqlc-dev/sqlc": containerSQLCVersion}
	seen := map[string]bool{}
	open := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line, _, _ = strings.Cut(line, "//")
		line = strings.TrimSpace(line)
		// Refuse syntax this deliberately small parser cannot interpret safely.
		if strings.ContainsAny(line, "\"`") {
			return errors.New("the verify image needs a rebuild: copied tools/go.mod has quoted syntax")
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.ContainsAny(line, "()") {
			switch {
			case line == ")" && open != "":
				open = ""
				continue
			case len(fields) == 2 && fields[1] == "(" && !strings.ContainsAny(fields[0], "()") && line == fields[0]+" (":
				if open != "" {
					return errors.New("the verify image needs a rebuild: copied tools/go.mod has a nested block")
				}
				open = fields[0]
				continue
			default:
				return errors.New("the verify image needs a rebuild: copied tools/go.mod has unsupported parentheses")
			}
		}
		directive, args := open, fields
		if open == "" {
			directive, args = fields[0], fields[1:]
		}
		if len(args) == 0 {
			continue
		}
		switch directive {
		case "replace":
			if _, ok := versions[args[0]]; ok {
				return fmt.Errorf("the verify image needs a rebuild: copied tools/go.mod must not replace %s", args[0])
			}
		case "require":
			if version, ok := versions[args[0]]; ok {
				if seen[args[0]] || len(args) != 2 || args[1] != version {
					return fmt.Errorf("the verify image needs a rebuild: copied tools/go.mod must require exactly %s %s once", args[0], version)
				}
				seen[args[0]] = true
			}
		}
	}
	if open != "" {
		return fmt.Errorf("the verify image needs a rebuild: copied tools/go.mod has an unterminated %s block", open)
	}
	if len(seen) != len(versions) {
		return errors.New("the verify image needs a rebuild: copied tools/go.mod must require templ and sqlc")
	}
	return nil
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
