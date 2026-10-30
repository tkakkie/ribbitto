package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type logRecordingVerifier struct {
	legacy   fakeVerifier
	recorded func(context.Context, string, string, string) (string, *containerRecord, error)
}

func (v logRecordingVerifier) verify(ctx context.Context, c, p string) (string, error) {
	return v.legacy.verify(ctx, c, p)
}
func (v logRecordingVerifier) verifyRecorded(ctx context.Context, c, p, r string) (string, *containerRecord, error) {
	return v.recorded(ctx, c, p, r)
}

// Keep stdin open until the response: EOF otherwise legitimately cancels a call.
func logMCPCall(t *testing.T, s verifyMCPServer, ctx context.Context, call string, afterStart func()) (string, bool) {
	t.Helper()
	return logMCPRun(t, ctx, call, afterStart, s.serve)
}

func logMCPRun(t *testing.T, ctx context.Context, call string, afterStart func(), serve func(context.Context, io.Reader, io.Writer, io.Writer) int) (string, bool) {
	t.Helper()
	input, send := io.Pipe()
	output, receive := io.Pipe()
	defer func() { _ = send.Close(); _ = output.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var audit bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- serve(ctx, input, receive, &audit)
		_ = receive.Close()
	}()
	reader := bufio.NewReader(output)
	exchange := func(line string) mcpResponse {
		t.Helper()
		if _, err := io.WriteString(send, line+"\n"); err != nil {
			t.Fatal(err)
		}
		if afterStart != nil && line == call {
			afterStart()
		}
		response, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return readMCPResponses(t, response)[0]
	}
	if response := exchange(mcpInit); response.Error != nil {
		t.Fatal(response.Error)
	}
	response := exchange(call)
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var result struct {
		Content []struct{ Type, Text string }
		IsError bool
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatal("invalid content")
	}
	_ = send.Close()
	code := <-done
	if code != 0 && ctx.Err() == nil {
		t.Fatalf("server exit %d: %s", code, &audit)
	}
	return result.Content[0].Text, result.IsError
}

func assertLogCapture(t *testing.T, store *verifyLogStore, text string, r *containerRecord, want bool) {
	t.Helper()
	count := 0
	if store != nil {
		count = len(store.captures)
	}
	if !want {
		if count != 0 || strings.Contains(text, verifyLogRunPrefix) {
			t.Fatalf("unexpected capture or ID: %d, %q", count, text)
		}
		return
	}
	if count != 1 {
		t.Fatalf("captures: %d, want one", count)
	}
	c := store.captures[0]
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || lines[1] != verifyLogRunPrefix+c.id || !verifyLogRunID(c.id) || c.record != r {
		t.Fatalf("capture/ID mismatch: %q", text)
	}
}

func TestVerifyLogMCPFinalVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, report, first                           string
		err                                           error
		timeout, cancel, noRecord, unsealed, verified bool
	}{
		{name: "failed", report: "FAILED\ndetail", first: "FAILED"},
		{name: "not verified", report: "NOT VERIFIED: skipped\ndetail", first: "NOT VERIFIED: skipped"},
		{name: "verified", report: "verified\ndetail", first: "verified", verified: true},
		{name: "timeout", report: "verified\ndetail", first: "NOT VERIFIED: time limit exceeded", timeout: true},
		{name: "cancelled", report: "verified\ndetail", first: "NOT VERIFIED: cancelled", cancel: true},
		{name: "runner error after success", report: "verified\ndetail", first: "FAILED: the check returned an error", err: errors.New("runner error")},
		{name: "could not run", report: "", first: "NOT VERIFIED: the check could not run", err: errors.New("runner error")},
		{name: "no record", report: "NOT VERIFIED: budget refused", first: "NOT VERIFIED: budget refused", noRecord: true},
		{name: "unsealed", report: "FAILED", first: "FAILED", unsealed: true},
	} {
		for _, logs := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/logs=%v", tc.name, logs), func(t *testing.T) {
				r := verifyLogFixture("failure\n")
				if tc.noRecord {
					r = nil
				} else if tc.unsealed {
					r.sealed = false
				}
				started := make(chan struct{})
				release := make(chan struct{})
				v := logRecordingVerifier{}
				run := func(ctx context.Context) (string, error) {
					close(started)
					<-release
					if tc.timeout || tc.cancel {
						<-ctx.Done()
					}
					return tc.report, tc.err
				}
				v.legacy = func(ctx context.Context, _, _ string) (string, error) { return run(ctx) }
				v.recorded = func(ctx context.Context, _, _, _ string) (string, *containerRecord, error) {
					report, err := run(ctx)
					return report, r, err
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				deadline := make(chan struct{})
				var store *verifyLogStore
				if logs {
					store = &verifyLogStore{}
				}
				text, bad := logMCPCall(t, verifyMCPServer{verifier: v, logs: store, callDeadline: deadline}, ctx, mcpCall, func() {
					<-started
					if store != nil && len(store.captures) != 0 {
						t.Error("capture before final verdict")
					}
					close(release)
					if tc.timeout {
						close(deadline)
					} else if tc.cancel {
						cancel()
					}
				})
				if first, _, _ := strings.Cut(text, "\n"); first != tc.first || bad == tc.verified {
					t.Fatalf("final verdict: %q, bad=%v", text, bad)
				}
				assertLogCapture(t, store, text, r, logs && !tc.verified && !tc.noRecord && !tc.unsealed)
				if logs && r != nil && r.sealed && (tc.timeout || tc.cancel || tc.err != nil) && !r.stderrNotServable {
					t.Fatal("final wrapper failure left stderr servable")
				}
				if !logs {
					want, _ := verifyResult(tc.report, tc.err, tc.timeout, tc.cancel)
					if text != want {
						t.Fatalf("logs-off bytes changed: %q, want %q", text, want)
					}
				}
			})
		}
	}
}

func TestVerifyLogResultBudget(t *testing.T) {
	for _, tc := range []struct {
		name, report  string
		err           error
		wantTruncated bool
	}{
		{"full summary", "FAILED\n" + strings.Repeat("x", summaryErrorBudget-len("FAILED\n")-len(deadlineOmitted)) + deadlineOmitted, errors.New(strings.Repeat("界", verifyResultLimit)), false},
		{"success becomes failed", "verified\n" + strings.Repeat("x", summaryErrorBudget-len("verified\n")-len(deadlineOmitted)) + deadlineOmitted, errors.New(strings.Repeat("x", verifyResultLimit)), false},
		{"backstop", "FAILED\n" + strings.Repeat("界", verifyResultLimit), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &verifyLogStore{}
			r := verifyLogFixture("")
			text, bad := verifyRecordedResult(tc.report, tc.err, false, false, store, r)
			assertLogCapture(t, store, text, r, true)
			if !bad || len(text) > verifyResultLimit || !utf8.ValidString(text) {
				t.Fatalf("invalid bounded result: %d bytes", len(text))
			}
			if strings.Contains(text, "[details truncated]") != tc.wantTruncated {
				t.Fatal("wrapper reserve failed")
			}
			if !tc.wantTruncated && !strings.Contains(text, deadlineOmitted) {
				t.Fatal("summary footer lost")
			}
		})
	}
}
