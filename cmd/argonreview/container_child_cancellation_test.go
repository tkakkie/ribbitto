package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestContainerRunnerChildCancellation(t *testing.T) {
	for _, check := range []string{"build", "writeback"} {
		for _, stage := range []string{"run", "stop", "rm"} {
			t.Run(check+"/"+stage, func(t *testing.T) {
				t.Parallel()
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				execution := containerExecution{}
				var child context.Context
				var stop context.CancelFunc
				var calls []string
				runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					calls = append(calls, args[0])
					if args[0] == "run" {
						child = ctx
						stop = out.(containerStream).output.cancel
						_, _ = io.WriteString(out, "completed command output\n")
						_, _ = io.WriteString(stderr, "benign diagnostic\n")
					} else if ctx.Err() != nil {
						t.Fatal("cleanup context was cancelled")
					}
					if args[0] == stage {
						stop()
					}
					return 0, nil
				}}
				report, err := runContainerExecution(parent, containerFixture(t), check, ".", runner, t.TempDir(), grantContainerExecution(&execution))
				if child.Err() != context.Canceled || parent.Err() != nil || execution.output.over || strings.Join(calls, ",") != "run,stop,rm" {
					t.Fatalf("child cancellation/cleanup: child=%v parent=%v cap=%v calls=%v", child.Err(), parent.Err(), execution.output.over, calls)
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("runner child cancellation lost: error=%v report=%q summary=%q", err, report, execution.summary)
				}
				if check == "writeback" {
					if report != "" || execution.summary != "" {
						t.Fatalf("writeback produced check presentation: report=%q summary=%q", report, execution.summary)
					}
					return
				}
				if !strings.HasPrefix(report, "FAILED\n") || !strings.HasPrefix(execution.summary, "FAILED\n") {
					t.Fatalf("runner child cancellation verified: report=%q summary=%q", report, execution.summary)
				}
				if !execution.record.stderrNotServable || recordSummaryFacts(execution.record).stderr.total != 0 {
					t.Fatal("cancelled runner child stderr remained servable")
				}
				assertIncompleteSummary(t, report, execution.summary, err)
			})
		}
	}
}
