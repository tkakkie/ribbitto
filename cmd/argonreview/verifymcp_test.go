package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeVerifier func(context.Context, string, string) (string, error)

func (f fakeVerifier) verify(ctx context.Context, c, p string) (string, error) { return f(ctx, c, p) }

const mcpInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
const mcpCall = `{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"verify","arguments":{"check":"test","package":"./..."}}}`

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int
		Message string
	} `json:"error"`
}

func readMCPResponses(t *testing.T, out string) []mcpResponse {
	t.Helper()
	var responses []mcpResponse
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("non-JSON stdout %q: %v", line, err)
		}
		var r mcpResponse
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		_, hasResult := fields["result"]
		_, hasError := fields["error"]
		if r.JSONRPC != "2.0" || r.ID == nil || hasResult == hasError || len(fields) != 3 {
			t.Fatalf("invalid response shape %s", line)
		}
		responses = append(responses, r)
	}
	return responses
}
func noVerify(t *testing.T) fakeVerifier {
	t.Helper()
	return func(context.Context, string, string) (string, error) {
		t.Error("verification started after refusal")
		return "", nil
	}
}
func serveBytes(t *testing.T, input string) ([]mcpResponse, string) {
	t.Helper()
	var out, log bytes.Buffer
	if code := (verifyMCPServer{verifier: noVerify(t)}).serve(context.Background(), strings.NewReader(input), &out, &log); code != 0 {
		t.Fatalf("server exit %d: %s", code, &log)
	}
	return readMCPResponses(t, out.String()), log.String()
}
func TestVerifyMCPSIGHUP(t *testing.T) {
	ctx, stop := verifyMCPSignals(context.Background())
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("SIGHUP did not cancel the server context")
	}
}

func TestVerifyMCPSIGPIPE(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"write", "serve"} {
		t.Run(mode, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = read.Close()
			defer func() { _ = write.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestVerifyMCPSIGPIPEHelper$")
			cmd.Env = append(os.Environ(), "ARGONREVIEW_PIPE_HELPER="+mode)
			cmd.Stdout = write
			var log bytes.Buffer
			cmd.Stderr = &log
			if err := cmd.Run(); err != nil || !strings.Contains(log.String(), "EPIPE") {
				t.Fatalf("helper: %v, stderr %s", err, &log)
			}
		})
	}
}

func TestVerifyMCPSIGPIPEHelper(t *testing.T) {
	mode := os.Getenv("ARGONREVIEW_PIPE_HELPER")
	if mode == "" {
		return // This test also runs normally, without acting as a subprocess.
	}
	ctx, stop := verifyMCPSignals(context.Background())
	defer stop()
	if mode == "write" {
		if _, err := os.Stdout.Write([]byte("response\n")); !errors.Is(err, syscall.EPIPE) {
			t.Fatalf("write: %v; want EPIPE", err)
		}
	} else {
		var log bytes.Buffer
		code := (verifyMCPServer{verifier: noVerify(t)}).serve(ctx, strings.NewReader(mcpInit+"\n"), os.Stdout, &log)
		if code != 1 || !strings.Contains(log.String(), "writing MCP response:") || !strings.Contains(log.String(), syscall.EPIPE.Error()) {
			t.Fatalf("closed stdout: exit %d, stderr %s", code, &log)
		}
	}
	_, _ = fmt.Fprintln(os.Stderr, "EPIPE")
	stop()
	os.Exit(0) // Avoid the test harness writing its summary to the closed stdout.
}

func TestVerifyMCPPinnedPath(t *testing.T) {
	dir := resolved(t, t.TempDir())
	marker := filepath.Join(dir, "executed")
	fake := filepath.Join(dir, "git")
	write(t, fake, "#!/bin/sh\n/usr/bin/touch \""+marker+"\"\n")
	if err := os.Chmod(fake, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := pinVerifyMCPPath(); err != nil {
		t.Fatal(err)
	}
	path, err := exec.LookPath("git")
	if err != nil || os.Getenv("PATH") != verifyMCPPath || !strings.Contains(":"+verifyMCPPath+":", ":"+filepath.Dir(path)+":") {
		t.Fatalf("PATH %q resolves git to %q: %v", os.Getenv("PATH"), path, err)
	}
	if err := exec.Command("git", "--version").Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untrusted git executed: %v", err)
	}
}

func TestVerifyMCPWorktreeRechecked(t *testing.T) {
	for _, change := range []string{"symlink", "parent symlink", "missing", "file", "overlap"} {
		t.Run(change, func(t *testing.T) {
			source, base := resolved(t, t.TempDir()), resolved(t, t.TempDir())
			tree := filepath.Join(base, "parent", "tree")
			mkdir(t, tree)
			in, send := io.Pipe()
			out, recv := io.Pipe()
			defer func() { _ = send.Close(); _ = out.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			v := fakeVerifier(func(context.Context, string, string) (string, error) {
				calls.Add(1)
				return "verified", nil
			})
			var log bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- startVerifyMCP(ctx, []string{"--trusted-source", source, "--worktree", tree}, nil, func(string) error { return nil }, noSweep, v, in, recv, &log)
				_ = recv.Close()
			}()
			reader := bufio.NewReader(out)
			exchange := func(line string) mcpResponse {
				t.Helper()
				if _, err := io.WriteString(send, line+"\n"); err != nil {
					t.Fatal(err)
				}
				response, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				return readMCPResponses(t, response)[0]
			}
			if exchange(mcpInit).Error != nil || exchange(mcpCall).Error != nil || calls.Load() != 1 {
				t.Fatal("initial call failed")
			}
			if change == "overlap" {
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				symlink(t, tree, source)
			} else {
				if err := os.Remove(tree); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "symlink":
					symlink(t, resolved(t, t.TempDir()), tree)
				case "parent symlink":
					if err := os.Remove(filepath.Dir(tree)); err != nil {
						t.Fatal(err)
					}
					other := resolved(t, t.TempDir())
					mkdir(t, filepath.Join(other, "tree"))
					symlink(t, other, filepath.Dir(tree))
				case "file":
					write(t, tree, "not a directory")
				}
			}
			if r := exchange(mcpCall); r.Error == nil || r.Error.Code != -32602 || calls.Load() != 1 {
				t.Fatalf("changed tree accepted: %+v, calls %d", r, calls.Load())
			}
			_ = send.Close()
			if code := <-done; code != 0 || !strings.Contains(log.String(), "checking worktree:") {
				t.Fatalf("exit %d, stderr %s", code, &log)
			}
		})
	}
}

func TestVerifyMCPProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		code       int
		id         string
	}{
		{"malformed", `{`, -32700, "null"}, {"empty", "", -32700, "null"}, {"batch", `[]`, -32600, "null"},
		{"scalar", `true`, -32600, "null"}, {"missing method", `{"jsonrpc":"2.0","id":2}`, -32600, "null"},
		{"response result", `{"jsonrpc":"2.0","id":2,"result":{}}`, -32600, "null"}, {"response error", `{"jsonrpc":"2.0","id":2,"error":{}}`, -32600, "null"},
		{"method type", `{"jsonrpc":"2.0","id":2,"method":1}`, -32600, "null"},
		{"version", `{"jsonrpc":"1.0","id":2,"method":"ping"}`, -32600, "null"},
		{"version type", `{"jsonrpc":2,"id":2,"method":"ping"}`, -32600, "null"},
		{"missing version", `{"id":2,"method":"ping"}`, -32600, "null"},
		{"unknown key", `{"jsonrpc":"2.0","id":2,"method":"ping","extra":true}`, -32600, "null"},
		{"duplicate envelope", `{"jsonrpc":"2.0","id":2,"method":"ping","method":"tools/call"}`, -32600, "null"},
		{"escaped duplicate", `{"jsonrpc":"2.0","id":2,"method":"ping","m\u0065thod":"ping"}`, -32600, "null"},
		{"duplicate params", `{"jsonrpc":"2.0","id":2,"method":"ping","params":{"a":1,"a":2}}`, -32600, "null"},
		{"nested duplicate", `{"jsonrpc":"2.0","id":2,"method":"ping","params":{"x":[{"a":1,"a":2}]}}`, -32600, "null"},
		{"case key", `{"jsonrpc":"2.0","id":2,"Method":"ping"}`, -32600, "null"},
		{"null params", `{"jsonrpc":"2.0","id":2,"method":"ping","params":null}`, -32600, "null"},
		{"array params", `{"jsonrpc":"2.0","id":2,"method":"ping","params":[]}`, -32600, "null"},
		{"string params", `{"jsonrpc":"2.0","id":2,"method":"ping","params":"x"}`, -32600, "null"},
		{"unknown method", `{"jsonrpc":"2.0","id":2,"method":"shell"}`, -32601, "2"},
		{"list before init", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, -32600, "2"},
		{"call before init", mcpCall, -32600, `"call"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs, _ := serveBytes(t, tc.line+"\n")
			if len(rs) != 1 || rs[0].Error == nil || rs[0].Error.Code != tc.code || string(rs[0].ID) != tc.id {
				t.Fatalf("responses %+v", rs)
			}
		})
	}
	for _, id := range []string{`null`, `{}`, `true`, `1.5`, `[]`, `1e2`, `123456789012345678901`, `"` + strings.Repeat("x", 129) + `"`} {
		t.Run("id="+id, func(t *testing.T) {
			rs, _ := serveBytes(t, `{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`+"\n")
			if len(rs) != 1 || rs[0].Error == nil || rs[0].Error.Code != -32600 || string(rs[0].ID) != "null" {
				t.Fatalf("responses %+v", rs)
			}
		})
	}
}
func TestVerifyMCPInitializationAndMethods(t *testing.T) {
	for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18", "future"} {
		line := strings.ReplaceAll(mcpInit, "2025-06-18", version)
		rs, log := serveBytes(t, `{"jsonrpc":"2.0","id":-23,"method":"ping"}`+"\n"+line+"\n"+`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+`{"jsonrpc":"2.0","method":"unknown"}`+"\n"+`{"jsonrpc":"2.0","id":"list","method":"tools/list"}`+"\n"+mcpInit)
		if len(rs) != 4 || string(rs[0].Result) != "{}" || string(rs[0].ID) != "-23" || rs[3].Error == nil || !strings.Contains(log, "unknown") {
			t.Fatalf("responses %+v, log %q", rs, log)
		}
		var init struct {
			ProtocolVersion string
			Capabilities    map[string]json.RawMessage
			ServerInfo      struct{ Name, Version string }
		}
		if err := json.Unmarshal(rs[1].Result, &init); err != nil {
			t.Fatal(err)
		}
		want := version
		if version == "future" {
			want = "2025-06-18"
		}
		if init.ProtocolVersion != want || string(init.Capabilities["tools"]) != "{}" || len(init.Capabilities) != 1 || init.ServerInfo.Name == "" || init.ServerInfo.Version == "" {
			t.Fatalf("initialize: %s", rs[1].Result)
		}
		var list struct {
			Tools []struct {
				Name        string
				Description string
				InputSchema json.RawMessage
			}
		}
		if err := json.Unmarshal(rs[2].Result, &list); err != nil {
			t.Fatal(err)
		}
		wantSchema, _ := json.Marshal(verifyInputSchema())
		if len(list.Tools) != 1 || list.Tools[0].Name != "verify" || list.Tools[0].Description != "Run build, vet, test, generated or test-db in the verification container." || !bytes.Equal(list.Tools[0].InputSchema, wantSchema) {
			t.Fatalf("tools: %s", rs[2].Result)
		}
	}
	for _, params := range []string{`{}`, `{"protocolVersion":null}`, `{"protocolVersion":1}`, `{"protocolVersion":true}`} {
		rs, _ := serveBytes(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+params+`}`)
		if len(rs) != 1 || rs[0].Error == nil || rs[0].Error.Code != -32602 {
			t.Fatalf("params %s: %+v", params, rs)
		}
	}
	rs, _ := serveBytes(t, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if rs[0].Error == nil || rs[0].Error.Code != -32602 {
		t.Fatal("missing initialize params accepted")
	}
	for _, params := range []string{``, `,"params":{}`, `,"params":{"_meta":{}}`} {
		rs, _ := serveBytes(t, mcpInit+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/list"`+params+`}`)
		if len(rs) != 2 || rs[1].Error != nil {
			t.Fatalf("list params %s", params)
		}
	}
	for _, params := range []string{`{"extra":1}`, `{"_meta":null}`, `{"_meta":"x"}`} {
		rs, _ := serveBytes(t, mcpInit+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":`+params+`}`)
		if rs[1].Error == nil || rs[1].Error.Code != -32602 {
			t.Fatal("list params accepted")
		}
	}
	// Decoded keys are case sensitive: these two distinct metadata keys are allowed.
	rs, _ = serveBytes(t, mcpInit+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"a":1,"A":2}}}`)
	if rs[1].Error != nil {
		t.Fatal("case-distinct keys refused")
	}
}
func TestVerifyMCPCallRefusals(t *testing.T) {
	for _, params := range []string{`{}`, `{"name":"verify"}`, `{"arguments":{}}`, `{"name":1,"arguments":{}}`, `{"name":"shell","arguments":{"check":"test","package":"."}}`, `{"name":"verify","arguments":null}`, `{"name":"verify","arguments":[]}`, `{"name":"verify","arguments":{"check":"test","package":".","extra":"x"}}`, `{"name":"verify","arguments":{"check":"test","package":"."},"extra":1}`, `{"name":"verify","arguments":{"check":"test","package":"."},"_meta":null}`, `{"name":"verify","arguments":{"check":"test","package":"."},"_meta":1}`} {
		rs, _ := serveBytes(t, mcpInit+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+params+`}`)
		if len(rs) != 2 || rs[1].Error == nil || rs[1].Error.Code != -32602 {
			t.Fatalf("accepted %s: %+v", params, rs)
		}
	}
	rs, _ := serveBytes(t, mcpInit+"\n"+strings.Replace(mcpCall, `"id":"call",`, "", 1))
	if len(rs) != 1 {
		t.Fatal("answered call notification")
	}
}
func TestVerifyResult(t *testing.T) {
	for _, tc := range []struct {
		name, report        string
		err                 error
		timedOut, cancelled bool
		first               string
		bad                 bool
	}{
		{"verified", "verified\ndetails", nil, false, false, "verified", false}, {"failed", "FAILED\ndetails", nil, false, false, "FAILED", true}, {"failure reason", "FAILED: tests\ndetails", nil, false, false, "FAILED: tests", true},
		{"bare not verified", "NOT VERIFIED\ndetails", nil, false, false, "NOT VERIFIED", true},
		{"not verified", "NOT VERIFIED: skipped\ndetails", nil, false, false, "NOT VERIFIED: skipped", true},
		{"verified plus error", "verified\ndetails", errors.New("removal failed"), false, false, "FAILED: the check returned an error", true},
		{"error only", "", errors.New("run failed"), false, false, "NOT VERIFIED: the check could not run", true},
		{"empty", "", nil, false, false, "NOT VERIFIED: the check could not run", true},
		{"garbage", "garbage\ndetails", nil, false, false, "NOT VERIFIED: the check could not run", true},
		{"lookalike", "verified-ish", nil, false, false, "NOT VERIFIED: the check could not run", true}, {"leading space", " verified", nil, false, false, "NOT VERIFIED: the check could not run", true},
		{"expired", "verified\ndetails", nil, true, false, "NOT VERIFIED: time limit exceeded", true},
		{"deadline failed", "FAILED: timeout\ndetails", context.DeadlineExceeded, true, false, "FAILED: timeout", true},
		{"deadline unknown", "garbage", context.DeadlineExceeded, true, false, "NOT VERIFIED: time limit exceeded", true},
		{"runner deadline", "", fmt.Errorf("runner: %w", context.DeadlineExceeded), false, false, "NOT VERIFIED: time limit exceeded", true},
		{"cancelled verified", "verified\ndetails", nil, false, true, "NOT VERIFIED: cancelled", true},
		{"cancelled garbage", "garbage\ndetails", nil, false, true, "NOT VERIFIED: cancelled", true},
		{"cancelled error", "verified\ndetails", fmt.Errorf("runner: %w", context.Canceled), false, false, "NOT VERIFIED: cancelled", true},
		{"timeout wins over cancelled", "verified\ndetails", context.Canceled, true, true, "NOT VERIFIED: time limit exceeded", true},
		{"runner deadline while cancelled", "verified\ndetails", context.DeadlineExceeded, false, true, "NOT VERIFIED: time limit exceeded", true},
		{"cancelled failed", "FAILED: tests\ndetails", context.Canceled, false, true, "FAILED: tests", true},
		{"cancelled not verified", "NOT VERIFIED: skipped\ndetails", context.Canceled, false, true, "NOT VERIFIED: skipped", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, bad := verifyResult(tc.report, tc.err, tc.timedOut, tc.cancelled)
			first, _, _ := strings.Cut(text, "\n")
			if first != tc.first || bad != tc.bad {
				t.Fatalf("%q, %v", text, bad)
			}
			if strings.Contains(tc.report, "details") && !strings.Contains(text, "details") {
				t.Fatal("details lost")
			}
			if tc.err != nil && !strings.Contains(text, tc.err.Error()) {
				t.Fatal("error detail lost")
			}
		})
	}
	text, bad := verifyResult("verified\n"+strings.Repeat("日", verifyResultLimit), nil, false, false)
	if bad || len(text) > 7000 || !utf8.ValidString(text) || !strings.HasPrefix(text, "verified\n") || !strings.Contains(text, "truncated") {
		t.Fatalf("cap: %d %v", len(text), bad)
	}
}

func TestVerifyMCPEnvironmentNoLeak(t *testing.T) {
	const secret = "environment-secret-sentinel"
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"malformed PATH", []string{"PATH:/private/" + secret}, "environment entry without an equals sign"},
		{"malformed arbitrary", []string{"arbitrary-" + secret}, "environment entry without an equals sign"},
		{"duplicate", []string{"PATH=" + secret + "=first", "PATH=" + secret + "=second"}, `environment variable "PATH" repeats`},
		{"forbidden", []string{"UNKNOWN=" + secret + "=value"}, `environment variable "UNKNOWN" is forbidden`},
		{"DOCKER_HOST", []string{"DOCKER_HOST=" + secret + "=value"}, `environment variable "DOCKER_HOST" is forbidden`},
		{"DOCKER_CONFIG", []string{"DOCKER_CONFIG=" + secret + "=value"}, `environment variable "DOCKER_CONFIG" is forbidden`},
		{"TMPDIR", []string{"TMPDIR=" + secret + "=value"}, `environment variable "TMPDIR" is forbidden`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyEnvironment(tc.env)
			if err == nil {
				t.Fatal("environment accepted")
			}
			if strings.Contains(err.Error(), secret) || err.Error() != tc.want {
				t.Errorf("unsafe environment error: %q", err)
			}
			var out, log bytes.Buffer
			code := startVerifyMCP(context.Background(), []string{"--trusted-source", "/source", "--worktree", "/tree"}, tc.env, func(string) error {
				t.Error("trust check ran after environment refusal")
				return nil
			}, noSweep, noVerify(t), strings.NewReader(mcpInit), &out, &log)
			if code != 1 || out.Len() != 0 || log.String() != "argonreview verify-mcp: "+tc.want+"\n" || strings.Contains(out.String()+log.String(), secret) {
				t.Fatalf("exit %d stdout %q stderr %q", code, &out, &log)
			}
		})
	}
}

func TestVerifyMCPStartup(t *testing.T) {
	source := resolved(t, t.TempDir())
	tree := resolved(t, t.TempDir())
	file := filepath.Join(source, "file")
	write(t, file, "x")
	link := filepath.Join(resolved(t, t.TempDir()), "link")
	symlink(t, source, link)
	args := func(s, w string) []string { return []string{"--trusted-source", s, "--worktree", w} }
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeSource, err := filepath.Rel(cwd, source)
	if err != nil {
		t.Fatal(err)
	}
	relativeTree, err := filepath.Rel(cwd, tree)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(source, "child")
	mkdir(t, child)
	good := args(source, tree)
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"missing args", nil, 2},
		{"empty source", args("", tree), 1}, {"empty tree", args(source, ""), 1},
		{"unknown flag", []string{"--source", source, "--worktree", tree}, 2},
		{"worktree equals", []string{"--trusted-source", source, "--worktree=" + tree}, 2}, {"missing worktree", good[:2], 2}, {"extra", append(append([]string{}, good...), "x"), 2},
		{"equals", []string{"--trusted-source=" + source, "--worktree", tree}, 2}, {"wrong order", []string{"--worktree", tree, "--trusted-source", source}, 2},
		{"missing path", args(source+"/absent", tree), 1}, {"relative source", args(relativeSource, tree), 1}, {"unclean source", args(source+"/../"+filepath.Base(source), tree), 1}, {"source symlink", args(link, tree), 1}, {"source file", args(file, tree), 1},
		{"relative tree", args(source, relativeTree), 1}, {"unclean tree", args(source, tree+"/"), 1}, {"tree symlink", args(tree, link), 1}, {"tree file", args(tree, file), 1}, {"missing tree", args(source, tree+"/absent"), 1},
		{"equal", args(source, source), 1}, {"inside source", args(source, child), 1}, {"containing source", args(source, filepath.Dir(source)), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, log bytes.Buffer
			code := startVerifyMCP(context.Background(), tc.args, nil, func(string) error { return nil }, noSweep, noVerify(t), strings.NewReader(mcpInit), &out, &log)
			if code != tc.code || out.Len() != 0 || log.Len() == 0 {
				t.Fatalf("%d stdout %q stderr %q", code, &out, &log)
			}
		})
	}
	for _, name := range []string{"HOME", "GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "GIT_ASKPASS", "GEMINI_API_KEY", "GEMINI_OTHER", "RIBBITTO_ARGON_SOURCE", "DOCKER_HOST", "DOCKER_CONFIG", "TMPDIR", "UNKNOWN"} {
		t.Run(name, func(t *testing.T) {
			var out, log bytes.Buffer
			value := "/dedicated-home/private-sentinel"
			code := startVerifyMCP(context.Background(), good, []string{name + "=" + value}, func(string) error { return nil }, noSweep, noVerify(t), strings.NewReader(mcpInit), &out, &log)
			if code != 1 || out.Len() != 0 || !strings.Contains(log.String(), name) || strings.Contains(log.String(), value) {
				t.Fatalf("%d %q %q", code, &out, &log)
			}
		})
	}
	for _, env := range [][]string{{"PATH"}, {"PATH=x", "PATH=y"}} {
		var out, log bytes.Buffer
		if code := startVerifyMCP(context.Background(), good, env, func(string) error { return nil }, noSweep, noVerify(t), strings.NewReader(mcpInit), &out, &log); code != 1 || out.Len() != 0 {
			t.Fatal("invalid env accepted")
		}
	}
	var out, log bytes.Buffer
	if code := startVerifyMCP(context.Background(), good, []string{"PATH=/usr/bin:/bin"}, func(s string) error {
		if s != source {
			t.Error("source changed")
		}
		return nil
	}, noSweep, noVerify(t), strings.NewReader(mcpInit), &out, &log); code != 0 || len(readMCPResponses(t, out.String())) != 1 {
		t.Fatalf("allowed env: %d %q", code, &log)
	}
}
func TestVerifyMCPTrustFailures(t *testing.T) {
	source := resolved(t, t.TempDir())
	tree := resolved(t, t.TempDir())
	for _, name := range []string{"not checkout", "unresolvable main", "revision differs", "unstamped", "no build info", "modified", "missing modified", "wrong origin"} {
		t.Run(name, func(t *testing.T) {
			info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "false"}}}
			ok := true
			switch name {
			case "unstamped":
				info.Settings = info.Settings[1:]
			case "no build info":
				ok = false
			case "modified":
				info.Settings[1].Value = "true"
			case "missing modified":
				info.Settings = info.Settings[:1]
			}
			gitFn := func(_ string, args ...string) (string, error) {
				if name == "not checkout" {
					return "", errors.New("not checkout")
				}
				if args[0] == "config" {
					if name == "wrong origin" {
						return "https://example.invalid/fork", nil
					}
					return ribbittoURL, nil
				}
				if name == "unresolvable main" {
					return "", errors.New("missing origin/main")
				}
				if name == "revision differs" {
					return "def", nil
				}
				return "abc", nil
			}
			var out, log bytes.Buffer
			code := startVerifyMCP(context.Background(), []string{"--trusted-source", source, "--worktree", tree}, nil, func(s string) error { return checkTrustedSource(s, info, ok, gitFn) }, noSweep, noVerify(t), strings.NewReader(mcpInit), &out, &log)
			if code != 1 || out.Len() != 0 || log.Len() == 0 {
				t.Fatalf("%d %q %q", code, &out, &log)
			}
		})
	}
	// Production wrapper must check the actual unstamped test binary before initialize.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	var out, log bytes.Buffer
	if code := verifyMCPCommand([]string{"--trusted-source", source, "--worktree", tree}, strings.NewReader(mcpInit), &out, &log); code != 1 || out.Len() != 0 || !strings.Contains(log.String(), "build") {
		t.Fatalf("real wrapper: %d %q %q", code, &out, &log)
	}
}

func TestVerifyMCPOversizedLine(t *testing.T) {
	// Allocate input before measuring: the server must discard without copying it.
	input := strings.Repeat("x", 32<<20) + "\n" + `{"jsonrpc":"2.0","id":"after","method":"ping"}` + "\n"
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rs, _ := serveBytes(t, input)
	runtime.ReadMemStats(&after)
	if len(rs) != 2 || rs[0].Error == nil || rs[0].Error.Code != -32600 || string(rs[1].ID) != `"after"` {
		t.Fatalf("responses %+v", rs)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("oversized line allocated %d", allocated)
	}
}

// Pipes keep EOF independent of request completion and allow checking busy/ping while running.
func TestVerifyMCPCallAndBusy(t *testing.T) {
	in, send := io.Pipe()
	out, recv := io.Pipe()
	var log bytes.Buffer
	started := make(chan struct{})
	release := make(chan struct{})
	var calls, checks atomic.Int32
	var invalidWorktree atomic.Bool
	checkWorktree := func() error {
		checks.Add(1)
		if invalidWorktree.Load() {
			return errors.New("worktree changed")
		}
		return nil
	}
	verifier := fakeVerifier(func(ctx context.Context, c, p string) (string, error) {
		calls.Add(1)
		if c != "test" || p != "./..." {
			t.Error("arguments changed")
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "verified\nprivate-detail", nil
	})
	done := make(chan int, 1)
	go func() {
		done <- (verifyMCPServer{verifier: verifier, checkWorktree: checkWorktree}).serve(context.Background(), in, recv, &log)
		_ = recv.Close()
	}()
	defer func() { _ = send.Close(); _ = out.Close() }()
	reader := bufio.NewReader(out)
	exchange := func(line string) mcpResponse {
		t.Helper()
		_, _ = io.WriteString(send, line+"\n")
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return readMCPResponses(t, line)[0]
	}
	if exchange(mcpInit).Error != nil {
		t.Fatal("init failed")
	}
	// No notifications/initialized is needed; protocol metadata is ignored.
	call := strings.Replace(mcpCall, `"name":"verify",`, `"_meta":{"progressToken":1},"name":"verify",`, 1)
	_, _ = io.WriteString(send, call+"\n")
	<-started
	if r := exchange(mcpCall); r.Error == nil || !strings.Contains(r.Error.Message, "busy") {
		t.Fatal("second call accepted")
	}
	if r := exchange(`{"jsonrpc":"2.0","id":3,"method":"ping"}`); string(r.Result) != "{}" {
		t.Fatal("ping blocked")
	}
	invalidWorktree.Store(true)
	if r := exchange(strings.Replace(mcpCall, `"check":"test"`, `"check":"shell"`, 1)); r.Error == nil || r.Error.Code != -32602 || checks.Load() != 2 {
		t.Fatal("worktree checked before argument validation")
	}
	if r := exchange(mcpCall); r.Error == nil || r.Error.Code != -32602 || checks.Load() != 3 {
		t.Fatal("busy checked before worktree validation")
	}
	close(release)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	r := readMCPResponses(t, line)[0]
	var result struct {
		Content []struct{ Type, Text string }
		IsError bool
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatal(err)
	}
	if string(r.ID) != `"call"` || result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != "verified\nprivate-detail" {
		t.Fatalf("result %s", r.Result)
	}
	_ = send.Close()
	if code := <-done; code != 0 || calls.Load() != 1 {
		t.Fatalf("code %d, calls %d", code, calls.Load())
	}
	if !strings.Contains(log.String(), `test ./...: verified`) || strings.Contains(log.String(), "private-detail") {
		t.Fatalf("log %q", &log)
	}
}

func TestVerifyMCPProductionEnvironment(t *testing.T) {
	tree := containerFixture(t)
	parent := t.TempDir()
	for _, name := range []string{"HOME", "GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "GIT_ASKPASS", "GEMINI_API_KEY", "DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_CONFIG", "UNKNOWN"} {
		t.Setenv(name, "forbidden-sentinel-"+name)
	}
	var calls []string
	runner := fakeContainerRunner{func(_ context.Context, args, env []string, stdout, _ io.Writer) (int, error) {
		calls = append(calls, args[0])
		if !reflect.DeepEqual(env, dockerEnvironment()) || strings.Contains(strings.Join(env, "\n"), "forbidden-sentinel") {
			t.Error("environment leaked")
		}
		if args[0] == "run" {
			_, _ = io.WriteString(stdout, containerEvents("p", "TestOne", "pass"))
		}
		return 0, nil
	}}
	in, send := io.Pipe()
	out, recv := io.Pipe()
	var log bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- (verifyMCPServer{verifier: containerVerifier{tree, runner, parent}}).serve(context.Background(), in, recv, &log)
		_ = recv.Close()
	}()
	reader := bufio.NewReader(out)
	_, _ = io.WriteString(send, mcpInit+"\n"+mcpCall+"\n")
	for range 2 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		rs := readMCPResponses(t, line)
		if rs[0].Error != nil {
			t.Fatal("call failed")
		}
	}
	_ = send.Close()
	if <-done != 0 {
		t.Fatal("server failed")
	}
	_ = out.Close()
	if !reflect.DeepEqual(calls, []string{"run", "stop", "rm"}) {
		t.Fatalf("calls %v", calls)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("private source copy left behind")
	}
}

func slowDocker(t *testing.T) string {
	t.Helper()
	dir := resolved(t, t.TempDir())
	path := filepath.Join(dir, "docker")
	write(t, path, "#!/bin/sh\ndir=${0%/*}\ncase \"$1\" in run) sleep 600 & echo $! > \"$dir/pid.tmp\"; mv \"$dir/pid.tmp\" \"$dir/pid\"; wait;; *) exit 0;; esac\n")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		data, _ := os.ReadFile(filepath.Join(dir, "pid"))
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return dir
}
func deadDockerChild(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if alive(t, pid) {
		t.Fatalf("grandchild %d survived return", pid)
	}
}
func TestVerifyDockerGroupKill(t *testing.T) {
	dir := slowDocker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, _ = (dockerRunner{ownGroup: true, path: dir}).run(ctx, []string{"run"}, []string{"PATH=/usr/bin:/bin"}, io.Discard, io.Discard)
		close(done)
	}()
	waitFor(t, filepath.Join(dir, "pid"))
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("docker did not finish")
	}
	deadDockerChild(t, dir)
}
func TestVerifyMCPShutdown(t *testing.T) {
	for _, reason := range []string{"deadline", "EOF", "signal"} {
		t.Run(reason, func(t *testing.T) {
			dir := slowDocker(t)
			in, send := io.Pipe()
			var out, log bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer func() { _ = send.Close() }()
			var finished atomic.Bool
			verifierDone := make(chan struct{})
			verifier := fakeVerifier(func(ctx context.Context, _, _ string) (string, error) {
				_, err := (dockerRunner{ownGroup: true, path: dir}).run(ctx, []string{"run"}, []string{"PATH=/usr/bin:/bin"}, io.Discard, io.Discard)
				finished.Store(true)
				close(verifierDone)
				return "verified\ndetails", err
			})
			deadline := make(chan struct{})
			done := make(chan int, 1)
			go func() {
				done <- (verifyMCPServer{verifier: verifier, callLimit: time.Minute, callDeadline: deadline}).serve(ctx, in, &out, &log)
			}()
			_, _ = io.WriteString(send, mcpInit+"\n"+mcpCall+"\n")
			waitFor(t, filepath.Join(dir, "pid"))
			switch reason {
			case "EOF":
				_ = send.Close()
			case "signal":
				cancel()
			case "deadline":
				// Expire only after Docker has recorded its child, then wait
				// for cancellation to finish before sending EOF.
				close(deadline)
				select {
				case <-verifierDone:
				case <-time.After(10 * time.Second):
					t.Fatal("deadline did not stop verifier")
				}
				_ = send.Close()
			}
			select {
			case code := <-done:
				if reason == "signal" && code != 1 || reason != "signal" && code != 0 {
					t.Fatalf("code %d: %s", code, &log)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not finish")
			}
			if !finished.Load() {
				t.Fatal("server returned before verifier")
			}
			assertVerifyShutdownResult(t, out.String(), reason)
			deadDockerChild(t, dir)
		})
	}
}

// Check the wire result without depending on host process inspection.
func assertVerifyShutdownResult(t *testing.T, out, reason string) {
	t.Helper()
	rs := readMCPResponses(t, out)
	if len(rs) != 2 || rs[1].Error != nil {
		t.Fatalf("responses %s", out)
	}
	var result struct {
		IsError bool
		Content []struct{ Type, Text string }
	}
	if err := json.Unmarshal(rs[1].Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("result %s", rs[1].Result)
	}
	text := result.Content[0].Text
	first, _, _ := strings.Cut(text, "\n")
	want := "NOT VERIFIED: cancelled"
	if reason == "deadline" || strings.HasPrefix(reason, "call limit") {
		want = "NOT VERIFIED: time limit exceeded"
	} else if strings.Contains(text, "time limit") {
		t.Errorf("cancellation reported a time limit: %q", text)
	}
	if first != want || strings.HasPrefix(first, "verified") {
		t.Fatalf("first line %q, want %q", first, want)
	}
}

func TestVerifyMCPShutdownFakeVerifier(t *testing.T) {
	for _, reason := range []string{"EOF", "signal", "deadline", "call limit", "call limit before deadline branch"} {
		t.Run(reason, func(t *testing.T) {
			in, send := io.Pipe()
			defer func() { _ = send.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, verifierDone := make(chan struct{}), make(chan struct{})
			v := fakeVerifier(func(ctx context.Context, _, _ string) (string, error) {
				close(started)
				<-ctx.Done()
				close(verifierDone)
				return "verified\ndetails", nil
			})
			s := verifyMCPServer{verifier: v, callLimit: time.Minute}
			deadline := make(chan struct{})
			if reason == "deadline" || reason == "call limit before deadline branch" {
				s.callDeadline = deadline
			}
			if strings.HasPrefix(reason, "call limit") {
				s.callLimit = 100 * time.Millisecond
			}
			var out, log bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- s.serve(ctx, in, &out, &log) }()
			_, _ = io.WriteString(send, mcpInit+"\n"+mcpCall+"\n")
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("verifier did not start")
			}
			switch reason {
			case "EOF":
				_ = send.Close()
			case "signal":
				cancel()
			case "deadline":
				close(deadline)
			}
			select {
			case <-verifierDone:
			case <-time.After(5 * time.Second):
				t.Fatal("verifier did not stop")
			}
			if reason != "signal" {
				_ = send.Close()
			}
			select {
			case code := <-done:
				if reason == "signal" && code != 1 || reason != "signal" && code != 0 {
					t.Fatalf("exit %d: %s", code, &log)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not finish")
			}
			assertVerifyShutdownResult(t, out.String(), reason)
		})
	}
}

func TestVerifyMCPShutdownGrace(t *testing.T) {
	in, send := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { _ = send.Close() }()
	started, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() { close(release); <-exited }()
	v := fakeVerifier(func(context.Context, string, string) (string, error) {
		close(started)
		defer close(exited)
		<-release
		return "verified", nil
	})
	var out, log bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- (verifyMCPServer{verifier: v, grace: time.Second}).serve(ctx, in, &out, &log)
	}()
	_, _ = io.WriteString(send, mcpInit+"\n"+mcpCall+"\n")
	<-started
	cancel()
	if code := <-done; code != 1 || !strings.Contains(log.String(), "grace") {
		t.Fatalf("grace: %d %s", code, &log)
	}
	readMCPResponses(t, out.String())
}

func TestVerifyMCPWriteErrorCancelsCall(t *testing.T) {
	in, send := io.Pipe()
	defer func() { _ = send.Close() }()
	started := make(chan struct{})
	var finished atomic.Bool
	v := fakeVerifier(func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		finished.Store(true)
		return "", ctx.Err()
	})
	var log bytes.Buffer
	writer := &failMCPWriter{}
	done := make(chan int, 1)
	go func() { done <- (verifyMCPServer{verifier: v}).serve(context.Background(), in, writer, &log) }()
	_, _ = io.WriteString(send, mcpInit+"\n"+mcpCall+"\n")
	<-started
	_, _ = io.WriteString(send, `{"jsonrpc":"2.0","id":3,"method":"ping"}`+"\n")
	select {
	case code := <-done:
		if code != 1 || !finished.Load() {
			t.Fatalf("exit %d finished %v", code, finished.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write error did not stop server")
	}
}

type failMCPWriter struct{ writes int }

func (w *failMCPWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		return len(p), nil
	}
	return 0, errors.New("closed stdout")
}

func TestVerifyMCPCaseAliasOverlap(t *testing.T) {
	base := resolved(t, t.TempDir())
	source, alias := filepath.Join(base, "source"), filepath.Join(base, "SOURCE")
	mkdir(t, filepath.Join(source, "child"))
	mkdir(t, filepath.Join(alias, "child"))
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	// Case-sensitive filesystems have separate trees; case-insensitive ones
	// have aliases that EvalSymlinks does not canonicalize. Neither skips.
	overlap := os.SameFile(sourceInfo, aliasInfo)
	for _, paths := range [][2]string{{source, alias}, {source, filepath.Join(alias, "child")}, {filepath.Join(source, "child"), alias}} {
		var out, log bytes.Buffer
		code := startVerifyMCP(context.Background(), []string{"--trusted-source", paths[0], "--worktree", paths[1]}, nil, func(string) error { return nil }, noSweep, noVerify(t), strings.NewReader(mcpInit), &out, &log)
		if overlap {
			if code != 1 || out.Len() != 0 || !strings.Contains(log.String(), "overlap") {
				t.Fatalf("aliased overlap accepted: %v code %d stdout %q stderr %q", paths, code, &out, &log)
			}
		} else if code != 0 || len(readMCPResponses(t, out.String())) != 1 {
			t.Fatalf("distinct directories refused: %v code %d stderr %q", paths, code, &log)
		}
	}
}
