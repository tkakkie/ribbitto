package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestContainerRunnerFailureRecord(t *testing.T) {
	for _, check := range []string{"test", "test-db", "build", "vet", "generated"} {
		for _, code := range []int{-1, 125, 126, 127} {
			t.Run(fmt.Sprintf("%s/%d", check, code), func(t *testing.T) {
				t.Parallel()
				execution := containerExecution{}
				runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					if args[0] != "run" {
						return 0, nil
					}
					_, _ = io.WriteString(out, containerEvents("p", "TestOne", "pass"))
					_, _ = io.WriteString(stderr, "benign diagnostic\n")
					return code, nil
				}}
				report, err := runContainerExecution(context.Background(), containerFixture(t), check, ".", runner, t.TempDir(), grantContainerExecution(&execution))
				if err != nil || execution.ownCancel || !strings.HasPrefix(report, "FAILED\n") {
					t.Fatalf("independent exit classification: own=%v err=%v report=%s", execution.ownCancel, err, report)
				}
				r := execution.record
				if !r.stderrNotServable || recordSummaryFacts(r).stderr.total != 0 || strings.Contains(execution.summary, "benign diagnostic") {
					t.Fatalf("runner stderr remained servable: complete=%v stderrNotServable=%v summary=%s", r.complete, r.stderrNotServable, execution.summary)
				}
				if aggregated, _ := containerCheckKind(check); aggregated {
					if execution.runState.completed || r.complete || r.stop != stopIncompleteRun || !strings.Contains(report, "Record: incomplete;") || !strings.Contains(report, "Stderr is not servable to log retrieval.\n") || strings.Contains(report, "Packages:") || strings.Contains(report, "Leaf tests:") {
						t.Fatalf("runner failure sealed complete stream: state=%+v report=%s", execution.runState, report)
					}
				}
				assertIncompleteSummary(t, report, execution.summary, err)
			})
		}
	}
}

func TestContainerOwnStopThenDeadline(t *testing.T) {
	for _, check := range []string{"test", "build"} {
		stops := []string{"cap"}
		if check == "test" {
			stops = append(stops, "malformed")
		}
		for _, stop := range stops {
			t.Run(check+"/"+stop, func(t *testing.T) {
				t.Parallel()
				execution := containerExecution{timeout: 2 * time.Second}
				runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					if args[0] != "run" {
						return 0, nil
					}
					_, _ = io.WriteString(stderr, "benign diagnostic\n")
					if check == "build" {
						_, _ = io.WriteString(out, strings.Repeat("x", containerOutputLimit+1))
					} else if stop == "cap" {
						wireCap(t, execution.aggregation, out, "total output")
					} else {
						_, _ = io.WriteString(out, "garbage\n")
					}
					if ctx.Err() != context.Canceled {
						t.Fatal("own stop did not cancel child before deadline")
					}
					// The child keeps Canceled after the outer run timer expires,
					// just as dockerRunner's context-kill return does.
					<-time.After(execution.timeout + 100*time.Millisecond)
					return -1, ctx.Err()
				}}
				report, err := runContainerExecution(context.Background(), containerFixture(t), check, ".", runner, t.TempDir(), grantContainerExecution(&execution))
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || execution.runErr != nil || execution.ownCancel {
					t.Fatalf("own stop polluted deadline provenance: own=%v runErr=%v err=%v", execution.ownCancel, execution.runErr, err)
				}
				want := "NOT VERIFIED: output limit exceeded (output cut off)"
				if stop == "malformed" {
					want = containerTimeLimitVerdict
				}
				text, _ := verifyResult(execution.summary, err, false, false)
				if !strings.HasPrefix(report, want+"\n") || strings.Contains(report, "context canceled") || strings.Contains(text, "context canceled") || !execution.record.stderrNotServable {
					t.Fatalf("own stop/deadline presentation: report=%s MCP=%s", report, text)
				}
				assertIncompleteSummary(t, report, execution.summary, err)
			})
		}
	}
}

func TestContainerContextKillRemovalFailure(t *testing.T) {
	for _, check := range []string{"test", "build"} {
		for _, interruption := range []string{"deadline", "parent"} {
			t.Run(check+"/"+interruption, func(t *testing.T) {
				t.Parallel()
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				execution := containerExecution{timeout: 2 * time.Second}
				runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					switch args[0] {
					case "run":
						_, _ = io.WriteString(out, containerEvents("p", "TestOne", "pass"))
						_, _ = io.WriteString(stderr, "benign diagnostic\n")
						if interruption == "parent" {
							cancel()
						}
						<-ctx.Done()
						return -1, ctx.Err()
					case "rm":
						return 1, errors.New("removal unconfirmed")
					default:
						return 0, nil
					}
				}}
				report, err := runContainerExecution(parent, containerFixture(t), check, ".", runner, t.TempDir(), grantContainerExecution(&execution))
				wantErr := context.Canceled
				if interruption == "deadline" {
					wantErr = context.DeadlineExceeded
				}
				if !errors.Is(err, wantErr) || execution.runErr != nil || execution.ownCancel || !strings.Contains(err.Error(), "container removal failed: removal unconfirmed") || strings.Contains(err.Error(), "may never have existed") {
					t.Fatalf("context kill misclassified as launch failure: runErr=%v err=%v", execution.runErr, err)
				}
				if !execution.record.stderrNotServable || !strings.HasPrefix(report, "FAILED\n") {
					t.Fatalf("context kill/removal presentation: %s", report)
				}
				assertIncompleteSummary(t, report, execution.summary, err)
			})
		}
	}
}

func TestContainerNonnegativeContextError(t *testing.T) {
	execution := containerExecution{}
	runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, _ io.Writer) (int, error) {
		if args[0] != "run" {
			return 0, nil
		}
		_, _ = io.WriteString(out, "garbage\n")
		return 0, context.Canceled
	}}
	report, err := runContainerExecution(context.Background(), containerFixture(t), "test", ".", runner, t.TempDir(), grantContainerExecution(&execution))
	if !errors.Is(err, context.Canceled) || !errors.Is(execution.runErr, context.Canceled) || execution.ownCancel || execution.runState.completed || !execution.record.stderrNotServable {
		t.Fatalf("nonnegative runner error discarded: own=%v runErr=%v err=%v", execution.ownCancel, execution.runErr, err)
	}
	if !strings.HasPrefix(report, "FAILED: malformed test stream\n") {
		t.Fatal(report)
	}
	assertIncompleteSummary(t, report, execution.summary, err)
}
