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
)

func TestVerifyResultBranchOrder(t *testing.T) {
	// Recognized verdicts precede timeout/cancellation: reversing that order
	// would relabel real failures merely because final processing ran late.
	for _, first := range []string{"FAILED", "FAILED: tests", "NOT VERIFIED", "NOT VERIFIED: 1 skipped", "unrecognized", "verified", ""} {
		for _, flags := range []struct {
			name                string
			timedOut, cancelled bool
		}{
			{"neither", false, false}, {"timeout", true, false},
			{"cancelled", false, true}, {"both", true, true},
		} {
			for _, failure := range []struct {
				name string
				err  error
			}{
				{"nil", nil}, {"deadline", fmt.Errorf("run: %w", context.DeadlineExceeded)},
				{"cancel", fmt.Errorf("run: %w", context.Canceled)}, {"other", errors.New("run failed")},
			} {
				t.Run(first+"/"+flags.name+"/"+failure.name, func(t *testing.T) {
					report := first + "\nbody\n"
					if first == "" {
						report = ""
					}
					want, bad := report, true
					prefix := ""
					if first == "unrecognized" || first == "verified" || first == "" {
						// Explicit expected precedence, including both flags and errors.
						if flags.timedOut || failure.name == "deadline" {
							prefix = "NOT VERIFIED: time limit exceeded"
						} else if flags.cancelled || failure.name == "cancel" {
							prefix = "NOT VERIFIED: cancelled"
						} else if first == "verified" && failure.err != nil {
							prefix = "FAILED: the check returned an error"
						} else if first == "verified" {
							bad = false
						} else {
							prefix = "NOT VERIFIED: the check could not run"
						}
					}
					if prefix != "" {
						want = prefix + "\n" + want
					}
					if failure.err != nil {
						want += "\n" + failure.err.Error()
					}
					text, isError := verifyResult(report, failure.err, flags.timedOut, flags.cancelled)
					if text != want || isError != bad {
						t.Fatalf("text %q, isError=%v; want %q, %v", text, isError, want, bad)
					}
				})
			}
		}
	}
}

func TestContainerDeadlineMCPRows(t *testing.T) {
	for _, mode := range []string{"call limit", "callDeadline"} {
		for _, check := range verifyCheckNames() {
			t.Run("row 11 "+mode+"/"+check, func(t *testing.T) {
				t.Parallel()
				tree, copies := containerFixture(t), t.TempDir()
				started, deadline := make(chan struct{}), make(chan struct{})
				var runErr error
				runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, diagnostic io.Writer) (int, error) {
					if args[0] != "run" {
						return 0, nil
					}
					close(started)
					<-ctx.Done()
					runErr = ctx.Err()
					_, _ = io.WriteString(out, "unfinished output")
					_, _ = io.WriteString(diagnostic, "unfinished diagnostic")
					return -1, nil
				}}
				s := verifyMCPServer{verifier: containerVerifier{"", browserSecurity{}, grantContainerBudget(), tree, runner, copies, containerTimeout, false}, callLimit: time.Minute}
				if mode == "call limit" {
					// Source preparation invokes Git before the runner can wait.
					// Allow that setup margin even under the race detector.
					s.callLimit = 3 * time.Second
				} else {
					s.callDeadline = deadline
				}
				text := deadlineServe(t, s, check, func() {
					select {
					case <-started:
					case <-time.After(5 * time.Second):
						t.Fatal("container did not start before call deadline")
					}
					if mode == "callDeadline" {
						close(deadline)
					}
				})
				wantErr := context.DeadlineExceeded
				if mode == "callDeadline" {
					wantErr = context.Canceled
				}
				if runErr != wantErr {
					t.Fatalf("run context %v, want %v", runErr, wantErr)
				}
				first := "FAILED"
				if mode == "call limit" {
					// #1857: was FAILED
					first = "NOT VERIFIED: time limit exceeded"
				}
				wantSummary := first + "\nCheck: " + check + " ./...\nScope: " + containerCheckScope(check) + "\nDetails unavailable: the check did not complete.\n" + deadlineOmitted
				wantText := wantSummary + "\n" + wantErr.Error() + "\n" + wantErr.Error() + "\ncontainer check interrupted: " + wantErr.Error()
				if text != wantText {
					t.Fatalf("MCP text %q, want %q", text, wantText)
				}
				// The same parent state through container-check must agree with serve.
				row := deadlineRow{"row 11 host agreement", "run", "interrupted", wantErr}
				characterizeDeadlineRow(t, check, row)
			})
		}
	}
	t.Run("row 11 wrapper unrecognized", func(t *testing.T) {
		// TestVerifyMCPShutdownFakeVerifier already covers lifecycle shutdown.
		// Pin the unrecognized body here as well as verifyResult's exact text.
		v := fakeVerifier(func(ctx context.Context, _, _ string) (string, error) {
			<-ctx.Done()
			return "unrecognized\nbody", ctx.Err()
		})
		text := deadlineServe(t, verifyMCPServer{verifier: v, callLimit: 100 * time.Millisecond}, "test", func() {})
		want, bad := verifyResult("unrecognized\nbody", context.DeadlineExceeded, true, false)
		if !bad || text != want || text != "NOT VERIFIED: time limit exceeded\nunrecognized\nbody\ncontext deadline exceeded" {
			t.Fatal(text)
		}
	})
}

func deadlineServe(t *testing.T, server verifyMCPServer, check string, ready func()) string {
	t.Helper()
	in, send := io.Pipe()
	out, receive := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() { _ = send.Close(); _ = out.Close() }()
	var log bytes.Buffer
	done := make(chan int, 1)
	go func() {
		code := server.serve(ctx, in, receive, &log)
		_ = receive.Close()
		done <- code
	}()
	reader := bufio.NewReader(out)
	read := func() mcpResponse {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		responses := readMCPResponses(t, line)
		if len(responses) != 1 || responses[0].Error != nil {
			t.Fatalf("responses %s", line)
		}
		return responses[0]
	}
	_, _ = io.WriteString(send, mcpInit+"\n")
	read()
	_, _ = io.WriteString(send, strings.Replace(mcpCall, `"check":"test"`, `"check":"`+check+`"`, 1)+"\n")
	ready()
	response := read()
	_ = send.Close()
	if code := <-done; code != 0 {
		t.Fatalf("server exit %d: %s", code, &log)
	}
	var result struct {
		IsError bool
		Content []struct{ Type, Text string }
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if string(response.ID) != `"call"` || !result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("MCP result %s", response.Result)
	}
	return result.Content[0].Text
}
