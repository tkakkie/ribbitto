package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type browserRouteVerifier struct {
	ordinary fakeVerifier
	browser  fakeVerifier
}

func (v browserRouteVerifier) verify(ctx context.Context, c, p string) (string, error) {
	return v.ordinary(ctx, c, p)
}
func (v browserRouteVerifier) verifyBrowser(ctx context.Context, p, r string) (string, error) {
	return v.browser(ctx, p, r)
}

// Keep the transport open between calls: EOF would cancel an in-flight call.
func browserMCPSession(t *testing.T, v verifier) func(string) mcpResponse {
	t.Helper()
	input, send := io.Pipe()
	output, receive := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan int, 1)
	var log bytes.Buffer
	go func() {
		code := (verifyMCPServer{verifier: v}).serve(ctx, input, receive, &log)
		_ = receive.Close()
		done <- code
	}()
	decoder := json.NewDecoder(output)
	exchange := func(line string) mcpResponse {
		t.Helper()
		if _, err := io.WriteString(send, line+"\n"); err != nil {
			t.Fatal(err)
		}
		var r mcpResponse
		if err := decoder.Decode(&r); err != nil {
			t.Fatalf("browser MCP response: %v", err)
		}
		return r
	}
	t.Cleanup(func() {
		_ = send.Close()
		defer cancel()
		defer func() { _ = output.Close() }()
		if code := <-done; code != 0 {
			t.Errorf("browser MCP exit %d: %s", code, &log)
		}
	})
	exchange(mcpInit)
	return exchange
}
func browserMCPCall(pattern, run string) string {
	b, _ := json.Marshal(map[string]string{"check": "test-browser", "package": pattern, "run": run})
	return `{"jsonrpc":"2.0","id":"browser","method":"tools/call","params":{"name":"verify","arguments":` + string(b) + `}}`
}
func browserMCPText(t *testing.T, r mcpResponse) (string, bool) {
	t.Helper()
	var result struct {
		Content []struct{ Text string }
		IsError bool
	}
	if r.Error != nil || json.Unmarshal(r.Result, &result) != nil || len(result.Content) != 1 {
		t.Fatalf("browser MCP result: %+v", r)
	}
	return result.Content[0].Text, result.IsError
}
func TestVerifyMCPBrowserRoute(t *testing.T) {
	for _, pkg := range []string{"./internal/web", "./cmd/ribbitto"} {
		t.Run(pkg, func(t *testing.T) {
			called := false
			v := browserRouteVerifier{ordinary: func(context.Context, string, string) (string, error) {
				t.Error("browser must not reach ordinary verifier")
				return "", nil
			}, browser: func(_ context.Context, p, r string) (string, error) {
				called = true
				if p != pkg || r != "TestTopicPostBrowser" {
					t.Errorf("browser route exact package and run: %q %q", p, r)
				}
				return "verified\n", nil
			}}
			call := browserMCPSession(t, v)
			text, bad := browserMCPText(t, call(browserMCPCall(pkg, "TestTopicPostBrowser")))
			if !called || bad || text != "verified\n" {
				t.Fatalf("browser route must reach verifyBrowser: called=%v text=%q bad=%v", called, text, bad)
			}
		})
	}
	t.Run("ordinary", func(t *testing.T) {
		called := false
		v := browserRouteVerifier{ordinary: func(_ context.Context, c, p string) (string, error) {
			called = true
			if c != "test" || p != "./..." {
				t.Errorf("ordinary route unchanged: %q %q", c, p)
			}
			return "verified\n", nil
		}, browser: func(context.Context, string, string) (string, error) {
			t.Error("ordinary calls must never pass run to browser entry")
			return "", nil
		}}
		call := browserMCPSession(t, v)
		_, bad := browserMCPText(t, call(mcpCall))
		a, err := parseVerifyCall([]byte(`{"name":"verify","arguments":{"check":"test","package":"./..."}}`))
		if !called || bad || err != nil || a.run != "" {
			t.Fatal("ordinary calls must have no run and use ordinary entry")
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		call := browserMCPSession(t, noVerify(t))
		text, bad := browserMCPText(t, call(browserMCPCall("./internal/web", browserTestRun)))
		if text != browserPaused+"\n" || !bad {
			t.Fatalf("unsupported browser verifier must fail closed paused: %q %v", text, bad)
		}
	})
	t.Run("tools-list", func(t *testing.T) {
		call := browserMCPSession(t, noVerify(t))
		r := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		var result struct {
			Tools []struct {
				Description string
				InputSchema map[string]any
			}
		}
		if r.Error != nil || json.Unmarshal(r.Result, &result) != nil || len(result.Tools) != 1 {
			t.Fatalf("browser tools/list: %+v", r)
		}
		got, _ := json.Marshal(result.Tools[0].InputSchema)
		want, _ := json.Marshal(verifyInputSchema())
		if !bytes.Equal(got, want) || !strings.Contains(result.Tools[0].Description, "test-browser") {
			t.Fatal("tools/list must publish browser schema and description")
		}
	})
}
func TestVerifyMCPBrowserAdmission(t *testing.T) {
	for _, kind := range []string{"no-flag", "off", "missing", "malformed", "symlink", "gate-expired", "gate-missing", "gate-malformed"} {
		t.Run(kind, func(t *testing.T) {
			v := browserRunFixture(t)
			want := browserPaused
			switch kind {
			case "no-flag":
				v.browserMode = ""
			case "off":
				browserWrite(t, v.browserMode, "off", 0o400)
			case "missing":
				browserRemove(t, v.browserMode)
			case "malformed":
				browserWrite(t, v.browserMode, "ON", 0o400)
			case "symlink":
				browserRemove(t, v.browserMode)
				if err := os.Symlink(v.browserSecurity.path, v.browserMode); err != nil {
					t.Fatal(err)
				}
			case "gate-expired":
				now := v.browserSecurity.now()
				v.browserSecurity.now = func() time.Time { return now.Add(browserSecurityAge) }
				want = browserOverdue
			case "gate-missing":
				browserRemove(t, v.browserSecurity.path)
				want = browserOverdue
			case "gate-malformed":
				browserWrite(t, v.browserSecurity.path, "bad", 0o600)
				want = browserOverdue
			}
			v.budget = fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
				t.Error("MCP refusal must not reserve slots")
				return budgetReservation{}, budgetStateUnknown
			})
			v.runner = fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Error("MCP refusal must not call runner or Docker")
				return 0, nil
			}}
			call := browserMCPSession(t, v)
			text, bad := browserMCPText(t, call(browserMCPCall("./internal/web", browserTestRun)))
			if !bad || !strings.HasPrefix(text, want+"\n") {
				t.Fatalf("MCP admission %s must refuse: %q want=%q", kind, text, want)
			}
			assertNoContainerCopy(t, v.copyParent)
		})
	}
}
func TestVerifyMCPBrowserRechecksFiles(t *testing.T) {
	for _, kind := range []string{"on-to-off", "off-to-on", "gate-expires", "gate-refreshed"} {
		t.Run(kind, func(t *testing.T) {
			v := browserRunFixture(t)
			now := v.browserSecurity.now()
			b := v.browserSecurity
			parent, err := b.parent()
			if err != nil {
				t.Fatal(err)
			}
			entry, err := b.readAt(parent)
			closeFD(parent)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "off-to-on" {
				browserWrite(t, v.browserMode, "off", 0o400)
			}
			if kind == "gate-refreshed" {
				entry.completed = now.Add(-browserSecurityAge)
				browserWrite(t, b.path, string(entry.bytes()), 0o600)
			}
			runs := 0
			runner := v.runner
			v.runner = fakeContainerRunner{func(ctx context.Context, args, env []string, out, log io.Writer) (int, error) {
				if args[0] == "run" {
					runs++
				}
				return runner.run(ctx, args, env, out, log)
			}}
			call := browserMCPSession(t, v)
			for i := range 2 {
				want := "verified"
				if kind == "on-to-off" && i == 1 || kind == "off-to-on" && i == 0 {
					want = browserPaused
				}
				if kind == "gate-expires" && i == 1 || kind == "gate-refreshed" && i == 0 {
					want = browserOverdue
				}
				before := runs
				text, bad := browserMCPText(t, call(browserMCPCall("./internal/web", browserTestRun)))
				expectedRuns := 0
				if want == "verified" {
					expectedRuns = 1
				}
				if !strings.HasPrefix(text, want+"\n") || bad != (want != "verified") || runs-before != expectedRuns {
					t.Fatalf("same server must reread %s call %d: text=%q runs=%d", kind, i, text, runs-before)
				}
				if i == 0 {
					switch kind {
					case "on-to-off":
						browserWrite(t, v.browserMode, "off", 0o400)
					case "off-to-on":
						browserWrite(t, v.browserMode, "on", 0o400)
					case "gate-expires":
						entry.completed = now.Add(-browserSecurityAge)
						browserWrite(t, b.path, string(entry.bytes()), 0o600)
					case "gate-refreshed":
						entry.completed = now.Add(-time.Hour)
						browserWrite(t, b.path, string(entry.bytes()), 0o600)
					}
				}
			}
		})
	}
}
