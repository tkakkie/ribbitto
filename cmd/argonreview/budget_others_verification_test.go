package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestBudgetNeverTouchesForeign pins the observation-only authority of M2.
func TestBudgetNeverTouchesForeign(t *testing.T) {
	fixtures := []otherInspect{
		otherFixture("unregistered", "", "running"),
		otherFixture("exclusive", "0,2", "created"),
		otherMemory(otherFixture("dev", "1", "running"), 0),
		otherMemory(otherFixture("verification", "2", "running"), 0),
		otherFixture("dev", "0", "private-unknown-state"),
	}
	f := otherFakeDocker(t, fixtures...)
	if got := otherPoll(f.otherQuery); got.line != otherStateLine {
		t.Fatalf("unexpected report: %+v", got)
	}
	forbidden := []string{"stop", "rm", "kill", "pause", "update", "start", "restart", "create", "run", "exec"}
	if len(f.calls) != 1+len(fixtures) {
		t.Fatalf("did not inspect all fixtures: %v", f.calls)
	}
	for _, args := range f.calls {
		if len(args) == 0 || (args[0] != "ps" && args[0] != "inspect") || slices.Contains(forbidden, args[0]) {
			t.Fatalf("non-read-only call: %v", args)
		}
	}
	for _, verb := range append(forbidden, "", "unknown") {
		t.Run("refuse "+verb, func(t *testing.T) {
			calls := 0
			docker := func(...string) ([]byte, error) { calls++; return nil, nil }
			args := []string{verb, "private-id"}
			if verb == "" {
				args = nil
			}
			if _, err := otherReadOnly(docker, args...); err == nil || calls != 0 {
				t.Fatalf("guard err = %v, transport calls = %d", err, calls)
			}
		})
	}
	// This text check is a tripwire, not a proof of the absence of mutations.
	source, err := os.ReadFile("budget_others.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range forbidden {
		if strings.Contains(string(source), `"`+verb+`"`) {
			t.Errorf("production source contains forbidden quoted verb %q", verb)
		}
	}
}

// TestBudgetOthersReadOnlyErrors keeps Docker diagnostics behind the report boundary.
func TestBudgetOthersReadOnlyErrors(t *testing.T) {
	for _, verb := range []string{"ps", "inspect"} {
		t.Run(verb, func(t *testing.T) {
			secret := "private-name-id-label-output"
			_, err := otherReadOnly(func(...string) ([]byte, error) {
				return []byte(secret), errors.New(secret)
			}, verb)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("transport error was not sanitized: %v", err)
			}
		})
	}
}

// TestBudgetOthersLines pins the fixed public text independently of fixtures.
func TestBudgetOthersLines(t *testing.T) {
	rows := []struct{ got, want string }{
		{otherStateLine, "NOT VERIFIED: verification budget unavailable: the container state could not be determined"},
		{otherVerificationLine, "NOT VERIFIED: verification budget unavailable: a verification container does not match its reservation"},
		{otherRegistrationLine, "NOT VERIFIED: verification budget unavailable: a registered container does not match its registration"},
		{otherUnlimitedLine, "NOT VERIFIED: verification budget unavailable: a container has no memory limit"},
		{otherUnregisteredLine, "NOT VERIFIED: verification budget unavailable: an unregistered container is present"},
		{otherDuplicateLine, "NOT VERIFIED: verification budget unavailable: a slot is claimed twice"},
		{otherExclusiveLine, "NOT VERIFIED: verification budget paused: an exclusive container is present"},
	}
	for _, row := range rows {
		if row.got != row.want {
			t.Errorf("line = %q, want %q", row.got, row.want)
		}
	}
}
