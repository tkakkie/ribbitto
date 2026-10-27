package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestContainerBudgetInvalidReservation(t *testing.T) {
	for _, kind := range []string{"zero memory", "negative memory", "empty label", "nil release"} {
		t.Run(kind, func(t *testing.T) {
			releases := 0
			r := singleContainerReservation()
			r.release = func() { releases++ }
			wantReleases := 1
			switch kind {
			case "zero memory":
				r.memory = 0
			case "negative memory":
				r.memory = -budgetUnit
			case "empty label":
				r.label = ""
			case "nil release":
				r.release, wantReleases = nil, 0
			}
			result := containerExecution{budget: fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
				return r, ""
			})}
			parent := t.TempDir()
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Error("Docker reached after invalid reservation")
				return 0, nil
			}}
			report, err := runContainerExecution(context.Background(), containerFixture(t), "build", "./...", runner, parent, &result)
			if err != nil || result.code != 1 || !strings.HasPrefix(report, budgetStateUnknown.line("verification budget")+"\n") || releases != wantReleases {
				t.Fatalf("invalid reservation: report %q, code %d, error %v, releases %d; want %d", report, result.code, err, releases, wantReleases)
			}
			assertNoContainerCopy(t, parent)
		})
	}
}

func TestContainerBudgetCanceledGrant(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	releases := 0
	result := containerExecution{budget: fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
		cancel()
		r := singleContainerReservation()
		r.release = func() { releases++ }
		return r, ""
	})}
	parent := t.TempDir()
	runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
		t.Error("Docker reached after canceled grant")
		return 0, nil
	}}
	report, err := runContainerExecution(ctx, containerFixture(t), "build", "./...", runner, parent, &result)
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "preparing container: ") || report != "" || result.summary != "" || releases != 1 {
		t.Fatalf("canceled grant: report %q, summary %q, error %v, releases %d", report, result.summary, err, releases)
	}
	assertNoContainerCopy(t, parent)
}
