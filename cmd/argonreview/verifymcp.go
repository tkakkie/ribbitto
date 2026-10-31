package main

// verify-mcp is trusted control code outside agy's sandbox. Startup fixes its
// source and worktree; the wire exposes only the runner's validated check arguments.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	verifyLineLimit     = 64 << 10
	verifyResultLimit   = 7000
	verifyCallLimit     = containerTimeout + 2*containerCleanupTimeout + time.Minute
	verifyShutdownGrace = 2*containerCleanupTimeout + 5*time.Second
	verifyMCPPath       = "/usr/bin:/bin"
	verifyMCPUsage      = "usage: argonreview verify-mcp --trusted-source <abs path> --worktree <abs path> [--browser-mode <abs path>] [--logs]"
)

type verifier interface {
	verify(context.Context, string, string) (string, error)
}
type browserVerifier interface {
	verifyBrowser(context.Context, string, string) (string, error)
}

// recordingVerifier keeps pre-record refusals and preparation errors capture-free.
// The server alone may retain a sealed record after deciding its final verdict.
type recordingVerifier interface {
	verifyRecorded(context.Context, string, string, string) (string, *containerRecord, bool, error)
}

type containerVerifier struct {
	browserMode     string
	browserSecurity browserSecurity
	budget          containerBudget
	worktree        string
	runner          containerRunner
	copyParent      string
	timeout         time.Duration
	logs            bool
}

func (v containerVerifier) verify(ctx context.Context, check, pattern string) (string, error) {
	summary, _, _, err := v.verifyRecorded(ctx, check, pattern, "")
	return summary, err
}

func (v containerVerifier) verifyRecorded(ctx context.Context, check, pattern, run string) (string, *containerRecord, bool, error) {
	if err := validateContainerCheck(v.worktree, check, pattern, run); err != nil {
		return "", nil, false, err
	}
	result := containerExecution{timeout: v.timeout, budget: v.budget, browserRun: run, logs: v.logs}
	if check == verifyBrowserCheck {
		result.admit = v.browserAdmission
	}
	_, err := runContainerExecution(ctx, v.worktree, check, pattern, v.runner, v.copyParent, &result)
	return result.summary, result.record, result.runnerFailed, err
}

func newContainerVerifier(worktree, browserMode string) containerVerifier {
	runner := dockerRunner{ownGroup: true}
	return containerVerifier{browserMode: browserMode, budget: newContainerBudget(runner), worktree: worktree, runner: runner, copyParent: containerCopyParent, timeout: containerTimeout, browserSecurity: newBrowserSecurity()}
}

func verifyMCPCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	environ := os.Environ()
	if err := pinVerifyMCPPath(); err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview verify-mcp: pinning PATH: %v\n", err)
		return 1
	}
	ctx, stop := verifyMCPSignals(context.Background())
	defer stop()
	return startVerifyMCP(ctx, args, environ, func(source string) error {
		info, ok := debug.ReadBuildInfo()
		return checkTrustedSource(source, info, ok, git)
	}, sweepContainers, nil, stdin, stdout, stderr)
}

// Match containerGit's executable search path, including the stage-1 Git check.
func pinVerifyMCPPath() error {
	return os.Setenv("PATH", verifyMCPPath)
}

func verifyMCPSignals(parent context.Context) (context.Context, context.CancelFunc) {
	// Catch SIGPIPE so broken stdio reaches the write-error cleanup path.
	// Do not Ignore it: ignored signals survive exec into Docker and Git.
	pipe := make(chan os.Signal, 1)
	signal.Notify(pipe, syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	return ctx, func() {
		stop()
		signal.Stop(pipe)
	}
}

func checkTrustedSource(source string, info *debug.BuildInfo, ok bool, gitFn func(string, ...string) (string, error)) error {
	_, err := trusted(info, ok, func() (string, error) { return sourceRevision(source, gitFn) })
	if err != nil {
		return fmt.Errorf("checking trusted build: %w", err)
	}
	return nil
}

func verifyRealDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be absolute and clean")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolving real directory: %w", err)
	}
	if real != path {
		return errors.New("path contains a symlink")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("examining directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	return nil
}

// Compare identities along both ancestor chains: case aliases on macOS can
// pass EvalSymlinks unchanged and evade a textual path-prefix comparison.
func verifyDisjointDirectories(source, tree string) error {
	for _, pair := range [][2]string{{source, tree}, {tree, source}} {
		other, err := os.Stat(pair[1])
		if err != nil {
			return fmt.Errorf("examining overlap root: %w", err)
		}
		for current := pair[0]; ; current = filepath.Dir(current) {
			info, err := os.Stat(current)
			if err != nil {
				return fmt.Errorf("examining overlap ancestor: %w", err)
			}
			if os.SameFile(info, other) {
				return errors.New("worktree and trusted source overlap")
			}
			if filepath.Dir(current) == current {
				break
			}
		}
	}
	return nil
}

func verifyEnvironment(environ []string) error {
	seen := map[string]bool{}
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			return errors.New("environment entry without an equals sign")
		}
		if seen[name] {
			return fmt.Errorf("environment variable %q repeats", name)
		}
		seen[name] = true
		switch name {
		// Accept only inputs this path uses: the runner supplies its own
		// Docker settings and copies under /tmp, so allowing caller Docker
		// variables or TMPDIR would promise controls that have no effect.
		case "PATH":
		default:
			return fmt.Errorf("environment variable %q is forbidden", name)
		}
	}
	return nil
}

func startVerifyMCP(ctx context.Context, args, environ []string, trust func(string) error, sweep func(io.Writer) error, v verifier, stdin io.Reader, stdout, stderr io.Writer) int {
	logs := len(args) > 0 && args[len(args)-1] == "--logs"
	if logs {
		args = args[:len(args)-1]
	}
	for _, arg := range args {
		if arg == "--logs" {
			_, _ = fmt.Fprintln(stderr, verifyMCPUsage)
			return 2
		}
	}
	if (len(args) != 4 && len(args) != 6) || args[0] != "--trusted-source" || args[2] != "--worktree" {
		_, _ = fmt.Fprintln(stderr, verifyMCPUsage)
		return 2
	}
	browserMode := ""
	if len(args) == 6 {
		if args[4] != "--browser-mode" || browserModePath(args[5]) != nil {
			_, _ = fmt.Fprintln(stderr, verifyMCPUsage)
			return 2
		}
		browserMode = args[5]
	}
	err := verifyEnvironment(environ)
	if err == nil {
		err = verifyRealDirectory(args[1])
	}
	if err == nil {
		err = verifyRealDirectory(args[3])
	}
	if err == nil {
		err = verifyDisjointDirectories(args[1], args[3])
	}
	if err == nil {
		err = trust(args[1])
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview verify-mcp: %v\n", err)
		return 1
	}
	if err := sweep(stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview verify-mcp: %v\n", err)
		return 1
	}
	var store *verifyLogStore
	if logs {
		store = &verifyLogStore{}
	}
	if v == nil {
		real := newContainerVerifier(args[3], browserMode)
		real.logs = logs
		v = real
	}
	return (verifyMCPServer{verifier: v, logs: store, checkWorktree: func() error {
		// Recheck because the worker can replace paths after startup. This
		// narrows, but cannot eliminate, a swap before the runner's copy;
		// the runner's own checks still apply.
		if err := verifyRealDirectory(args[3]); err != nil {
			return err
		}
		return verifyDisjointDirectories(args[1], args[3])
	}}).serve(ctx, stdin, stdout, stderr)
}

func verifyResult(report string, runErr error, timedOut, cancelled bool) (string, bool) {
	return verifyRecordedResult(report, runErr, timedOut, cancelled, false, nil, nil)
}

const verifyLogRunPrefix = "Log run: "
const verifyLogRunLineBytes = len(verifyLogRunPrefix) + verifyLogIDBytes*2 + 1

func verifyRecordedResult(report string, runErr error, timedOut, cancelled, runnerFailed bool, store *verifyLogStore, record *containerRecord) (string, bool) {
	// The runner has an earlier deadline of its own, including source copying.
	timedOut = timedOut || errors.Is(runErr, context.DeadlineExceeded)
	cancelled = !timedOut && (cancelled || errors.Is(runErr, context.Canceled))
	first, _, _ := strings.Cut(report, "\n")
	recognized := first == "FAILED" || strings.HasPrefix(first, "FAILED:") || first == "NOT VERIFIED" || strings.HasPrefix(first, "NOT VERIFIED:")
	initialBytes := len(report)
	bad := true
	switch {
	case recognized:
	case timedOut:
		report = "NOT VERIFIED: time limit exceeded\n" + report
	case cancelled:
		report = "NOT VERIFIED: cancelled\n" + report
	case first == "verified" && runErr != nil:
		report = "FAILED: the check returned an error\n" + report
	case first == "verified":
		bad = false
	default:
		report = "NOT VERIFIED: the check could not run\n" + report
	}
	// Capture only after every wrapper override; summary-time success can still
	// become FAILED here. A nil store leaves logs-off runs entirely unretained.
	prefixBytes := len(report) - initialBytes
	logBytes := 0
	if store != nil && bad {
		first, rest, newline := strings.Cut(report, "\n")
		// Cleanup and wrapper failures can arrive after record finalization.
		// Deny captured stderr before publishing, leaving execution alone.
		if record != nil && (timedOut || cancelled || runErr != nil || runnerFailed || summaryRunnerFailure(first)) {
			record.mu.Lock()
			if record.sealed {
				record.stderrNotServable = true
			}
			record.mu.Unlock()
		}
		if capture := store.capture(first, record); capture != nil {
			report = first + "\n" + verifyLogRunPrefix + capture.id + "\n"
			if newline {
				report += rest
			}
			logBytes = verifyLogRunLineBytes
		}
	}
	if runErr != nil {
		// The prefix and diagnostic share the summary core's fixed reserve.
		detail := strings.ToValidUTF8(runErr.Error(), "�")
		report += "\n" + cutGeneratedText(detail, summaryReserve-prefixBytes-logBytes-1)
	}
	report = strings.ToValidUTF8(report, "�")
	if len(report) > verifyResultLimit {
		const suffix = "\n[details truncated]\n"
		end := verifyResultLimit - len(suffix)
		for end > 0 && !utf8.RuneStart(report[end]) {
			end--
		}
		report = report[:end] + suffix
	}
	return report, bad
}

type verifyLine struct {
	data     []byte
	oversize bool
	err      error
}

// ReadSlice bounds retained memory even for a client that never sends a newline.
// A canceled server must also release a reader blocked on sending an event.
func readVerifyLines(stdin io.Reader, events chan<- verifyLine, stop <-chan struct{}) {
	reader := bufio.NewReaderSize(stdin, 4096)
	send := func(event verifyLine) bool {
		select {
		case events <- event:
			return true
		case <-stop:
			return false
		}
	}
	var line []byte
	over := false
	for {
		fragment, err := reader.ReadSlice('\n')
		size := len(fragment)
		if size > 0 && fragment[size-1] == '\n' {
			size--
		}
		if !over {
			if len(line)+size > verifyLineLimit {
				over = true
				line = nil
			} else {
				line = append(line, fragment[:size]...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(fragment) > 0 || err == nil || over || len(line) > 0 {
			if !send(verifyLine{data: line, oversize: over}) {
				return
			}
		}
		line = nil
		over = false
		if err != nil {
			send(verifyLine{err: err})
			return
		}
	}
}

type verifyRequest struct {
	id     json.RawMessage
	method string
	params json.RawMessage
}
type verifyRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type verifyResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *verifyRPCError `json:"error,omitempty"`
}

func parseVerifyRequest(line []byte) (verifyRequest, *verifyRPCError) {
	invalid := func(code int, message string) (verifyRequest, *verifyRPCError) {
		return verifyRequest{}, &verifyRPCError{code, message}
	}
	if !json.Valid(line) {
		return invalid(-32700, "parse error")
	}
	fields, err := verifyObject(line)
	if err != nil {
		return invalid(-32600, "invalid request")
	}
	for key := range fields {
		switch key {
		case "jsonrpc", "id", "method", "params":
		default:
			return invalid(-32600, "unknown envelope key")
		}
	}
	var version, method string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" || json.Unmarshal(fields["method"], &method) != nil || method == "" {
		return invalid(-32600, "invalid request")
	}
	id := fields["id"]
	if id != nil && !validVerifyID(id) {
		return invalid(-32600, "invalid id")
	}
	if params, ok := fields["params"]; ok {
		if _, err := verifyObject(params); err != nil {
			return invalid(-32600, "params must be an object")
		}
	}
	return verifyRequest{id, method, fields["params"]}, nil
}

func validVerifyID(raw []byte) bool {
	var text string
	if len(raw) > 0 && raw[0] == '"' {
		return json.Unmarshal(raw, &text) == nil && len(text) <= 128
	}
	if len(raw) == 0 || len(raw) > 20 {
		return false
	}
	digits := raw
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if len(digits) == 0 || len(digits) > 1 && digits[0] == '0' {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func verifyMetadata(fields map[string]json.RawMessage) bool {
	if raw, ok := fields["_meta"]; ok {
		_, err := verifyObject(raw)
		return err == nil
	}
	return true
}
func parseVerifyCall(raw []byte) (verifyArguments, error) {
	fields, err := verifyObject(raw)
	if err != nil {
		return verifyArguments{}, fmt.Errorf("reading call params: %w", err)
	}
	for key := range fields {
		if key != "name" && key != "arguments" && key != "_meta" {
			return verifyArguments{}, errors.New("unknown call parameter")
		}
	}
	var name string
	if json.Unmarshal(fields["name"], &name) != nil || name != verifyToolName || !verifyMetadata(fields) {
		return verifyArguments{}, errors.New("invalid tool name or metadata")
	}
	return parseVerifyArguments(fields["arguments"])
}

type verifyMCPServer struct {
	logs             *verifyLogStore
	verifier         verifier
	checkWorktree    func() error
	callLimit, grace time.Duration
	// An injected deadline lets lifecycle tests expire a call after readiness.
	callDeadline <-chan struct{}
}
type verifyCompletion struct {
	report       string
	record       *containerRecord
	runnerFailed bool
	err          error
}
type verifyFlight struct {
	id       json.RawMessage
	args     verifyArguments
	ctx      context.Context
	cancel   context.CancelFunc
	timedOut bool
}

func (s verifyMCPServer) serve(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	limit, grace := s.callLimit, s.grace
	if limit <= 0 {
		limit = verifyCallLimit
	}
	if grace <= 0 {
		grace = verifyShutdownGrace
	}
	events := make(chan verifyLine)
	stop := make(chan struct{})
	defer close(stop)
	if closer, ok := stdin.(io.ReadCloser); ok {
		defer func() { _ = closer.Close() }()
	}
	go readVerifyLines(stdin, events, stop)
	encoder := json.NewEncoder(stdout)
	initialized := false
	// Only this loop writes responses, including completion and shutdown results.
	var flight *verifyFlight
	var done <-chan verifyCompletion
	var deadline <-chan struct{}
	var graceEnd <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	code := 0
	exiting := false
	outputFailed := false
	contextDone := ctx.Done()
	beginGrace := func() {
		if timer == nil {
			timer = time.NewTimer(grace)
			graceEnd = timer.C
		}
	}
	shutdown := func(status int) {
		exiting = true
		code = max(code, status)
		events = nil
		contextDone = nil
		if flight != nil {
			flight.cancel()
			deadline = nil
			beginGrace()
		}
	}
	respond := func(id json.RawMessage, result any, rpcErr *verifyRPCError) {
		if outputFailed {
			return
		}
		if id == nil {
			id = json.RawMessage("null")
		}
		if err := encoder.Encode(verifyResponse{"2.0", id, result, rpcErr}); err != nil {
			_, _ = fmt.Fprintf(stderr, "writing MCP response: %v\n", err)
			outputFailed = true
			shutdown(1)
		}
	}
	writeRaw := func(line []byte) {
		if outputFailed {
			return
		}
		n, err := stdout.Write(line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "writing MCP response: %v\n", err)
			outputFailed = true
			shutdown(1)
		}
	}
	rpcError := func(id json.RawMessage, number int, message string) {
		respond(id, nil, &verifyRPCError{number, message})
	}
	for {
		if exiting && flight == nil {
			return code
		}
		select {
		case <-contextDone:
			shutdown(1)
		case <-deadline:
			// Parent cancellation can make both select cases ready.
			flight.timedOut = ctx.Err() == nil
			deadline = nil
			flight.cancel()
			beginGrace()
		case <-graceEnd:
			_, _ = fmt.Fprintln(stderr, "verify-mcp: verifier did not stop within shutdown grace")
			return 1
		case result := <-done:
			timedOut := flight.timedOut || errors.Is(flight.ctx.Err(), context.DeadlineExceeded)
			cancelled := !timedOut && (flight.ctx.Err() != nil || errors.Is(result.err, context.Canceled))
			text, bad := verifyRecordedResult(result.report, result.err, timedOut, cancelled, result.runnerFailed, s.logs, result.record)
			first, _, _ := strings.Cut(text, "\n")
			_, _ = fmt.Fprintf(stderr, "verify %s %s: %s\n", flight.args.check, flight.args.pattern, first)
			id := flight.id
			flight.cancel()
			flight = nil
			done = nil
			deadline = nil
			if timer != nil {
				timer.Stop()
				timer = nil
				graceEnd = nil
			}
			respond(id, map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": bad}, nil)
		case line := <-events:
			if line.err != nil {
				status := 0
				if !errors.Is(line.err, io.EOF) {
					_, _ = fmt.Fprintf(stderr, "reading MCP input: %v\n", line.err)
					status = 1
				}
				shutdown(status)
				continue
			}
			if line.oversize {
				rpcError(nil, -32600, "incoming line exceeds limit")
				continue
			}
			req, rpcErr := parseVerifyRequest(line.data)
			if rpcErr != nil && s.logs != nil {
				if recovered, ok := verifyLogRecoverRequest(line.data); ok {
					req, rpcErr = recovered, nil
				}
			}
			if rpcErr != nil {
				respond(nil, nil, rpcErr)
				continue
			}
			if req.id == nil {
				switch req.method {
				case "notifications/initialized":
					_, _ = fmt.Fprintln(stderr, "verify-mcp: client initialized")
				case "tools/call":
					_, _ = fmt.Fprintln(stderr, "verify-mcp: ignored tools/call notification")
				default:
					_, _ = fmt.Fprintf(stderr, "verify-mcp: ignored notification %q\n", req.method)
				}
				continue
			}
			switch req.method {
			case "ping":
				respond(req.id, map[string]any{}, nil)
			case "initialize":
				if initialized {
					rpcError(req.id, -32600, "already initialized")
					continue
				}
				fields, err := verifyObject(req.params)
				var version string
				if err != nil || json.Unmarshal(fields["protocolVersion"], &version) != nil || bytes.Equal(fields["protocolVersion"], []byte("null")) {
					rpcError(req.id, -32602, "initialize requires string protocolVersion")
					continue
				}
				switch version {
				case "2024-11-05", "2025-03-26", "2025-06-18":
				default:
					version = "2025-06-18"
				}
				initialized = true
				respond(req.id, map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "argonreview", "version": "1"}}, nil)
			case "tools/list":
				if !initialized {
					rpcError(req.id, -32600, "initialize required")
					continue
				}
				fields := map[string]json.RawMessage{}
				var err error
				if req.params != nil {
					fields, err = verifyObject(req.params)
				}
				if err != nil || !verifyMetadata(fields) || len(fields) > 1 || len(fields) == 1 && fields["_meta"] == nil {
					rpcError(req.id, -32602, "invalid tools/list params")
					continue
				}
				tools := []map[string]any{{"name": verifyToolName, "description": "Run build, vet, test, generated, test-db or test-browser in the verification container.", "inputSchema": verifyInputSchema()}}
				if s.logs != nil {
					tools = append(tools, verifyLogTool())
				}
				respond(req.id, map[string]any{"tools": tools}, nil)
			case "tools/call":
				if !initialized {
					rpcError(req.id, -32600, "initialize required")
					continue
				}
				if s.logs != nil && verifyLogCallNamed(req.params) {
					reply, audit := s.logs.verifyLogCall(req.params, req.id)
					_, _ = io.WriteString(stderr, audit)
					writeRaw(reply.line)
					continue
				}
				args, err := parseVerifyCall(req.params)
				if err != nil {
					rpcError(req.id, -32602, "invalid verify arguments")
					continue
				}
				if s.checkWorktree != nil {
					if err := s.checkWorktree(); err != nil {
						_, _ = fmt.Fprintf(stderr, "verify-mcp: checking worktree: %v\n", err)
						rpcError(req.id, -32602, "invalid worktree")
						continue
					}
				}
				if flight != nil {
					rpcError(req.id, -32600, "busy")
					continue
				}
				callCtx, cancel := context.WithTimeout(ctx, limit)
				completed := make(chan verifyCompletion, 1)
				flight = &verifyFlight{id: req.id, args: args, ctx: callCtx, cancel: cancel}
				done = completed
				deadline = callCtx.Done()
				if s.callDeadline != nil {
					deadline = s.callDeadline
				}
				go func() {
					var report string
					var err error
					var record *containerRecord
					var runnerFailed bool
					if v, ok := s.verifier.(recordingVerifier); ok && s.logs != nil {
						report, record, runnerFailed, err = v.verifyRecorded(callCtx, args.check, args.pattern, args.run)
					} else if args.check == verifyBrowserCheck {
						report = browserPaused + "\n"
						if v, ok := s.verifier.(browserVerifier); ok {
							report, err = v.verifyBrowser(callCtx, args.pattern, args.run)
						}
					} else {
						report, err = s.verifier.verify(callCtx, args.check, args.pattern)
					}
					completed <- verifyCompletion{report: report, record: record, runnerFailed: runnerFailed, err: err}
				}()
			default:
				rpcError(req.id, -32601, "method not found")
			}
		}
	}
}
