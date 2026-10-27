package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

type budgetRecordingRunner struct {
	t       *testing.T
	calls   [][]string
	respond func([]string, io.Writer, io.Writer) (int, error)
}

func (b *budgetRecordingRunner) run(_ context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
	b.t.Helper()
	if len(args) == 0 || (args[0] != "ps" && args[0] != "inspect") {
		b.t.Fatalf("mutating call: %v", args)
	}
	if !slices.Equal(env, dockerEnvironment()) {
		b.t.Fatal("Docker environment changed")
	}
	b.calls = append(b.calls, slices.Clone(args))
	return b.respond(args, stdout, stderr)
}

func TestBudgetInventory(t *testing.T) {
	for _, kind := range []string{
		"valid", "all-containers", "listing-error", "listing-exit", "listing-oversize", "stderr-oversize", "invalid-id", "uppercase-id", "duplicate-id",
		"inspect-error-present", "inspect-exit", "inspect-oversize", "bad-json", "id-mismatch", "inspect-gone", "inspect-gone-invalid-list", "inspect-gone-list-error",
	} {
		t.Run(kind, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			listing := 0
			runner := &budgetRecordingRunner{t: t}
			runner.respond = func(args []string, stdout, stderr io.Writer) (int, error) {
				if args[0] == "ps" {
					if !slices.Equal(args, []string{"ps", "-a", "-q", "--no-trunc"}) {
						t.Fatalf("listing args: %v", args)
					}
					listing++
					switch kind {
					case "listing-error":
						return -1, errors.New("untrusted docker data")
					case "listing-exit":
						_, _ = fmt.Fprint(stderr, "untrusted docker data")
						return 1, nil
					case "listing-oversize":
						_, _ = fmt.Fprint(stdout, strings.Repeat("x", containerOutputLimit+1))
						return 0, nil
					case "stderr-oversize":
						_, _ = fmt.Fprint(stderr, strings.Repeat("x", containerOutputLimit+1))
						return 0, nil
					case "invalid-id":
						_, _ = fmt.Fprintln(stdout, "--bad")
						return 0, nil
					case "uppercase-id":
						_, _ = fmt.Fprintln(stdout, strings.ToUpper(id))
						return 0, nil
					case "duplicate-id":
						_, _ = fmt.Fprintln(stdout, id, id)
						return 0, nil
					case "all-containers":
						_, _ = fmt.Fprintln(stdout, id, strings.Repeat("b", 64))
						return 0, nil
					}
					if listing > 1 {
						switch kind {
						case "inspect-gone":
							return 0, nil
						case "inspect-gone-invalid-list":
							_, _ = fmt.Fprintln(stdout, "bad")
							return 0, nil
						case "inspect-gone-list-error":
							return 1, nil
						}
					}
					_, _ = fmt.Fprintln(stdout, id)
					return 0, nil
				}
				if len(args) != 4 || !slices.Equal(args[:3], []string{"inspect", "--format", "{{json .}}"}) || !sweepID.MatchString(args[3]) {
					t.Fatalf("inspect args: %v", args)
				}
				switch kind {
				case "inspect-error-present", "inspect-gone", "inspect-gone-invalid-list", "inspect-gone-list-error":
					return -1, errors.New("untrusted docker data")
				case "inspect-exit":
					_, _ = fmt.Fprint(stderr, "untrusted docker data")
					return 1, nil
				case "inspect-oversize":
					_, _ = fmt.Fprint(stdout, strings.Repeat("x", containerOutputLimit+1))
					return 0, nil
				case "bad-json":
					_, _ = fmt.Fprint(stdout, "untrusted docker data")
					return 0, nil
				case "id-mismatch":
					_, _ = fmt.Fprintf(stdout, `{"Id":%q}`, strings.Repeat("b", 64))
					return 0, nil
				}
				// A foreign, stopped container must survive inventory unchanged too.
				_, _ = fmt.Fprintf(stdout, `{"Id":%q,"Name":"/foreign","Config":{"Image":"image","Labels":{"x":"y"}},"State":{"Status":"exited"},"HostConfig":{"Memory":123,"MemorySwap":456}}`, args[3])
				return 0, nil
			}
			inventory := budgetDockerInventory{runner: runner}
			containers, err := inventory.budgetList(context.Background())
			switch kind {
			case "valid", "all-containers":
				want := 1
				if kind == "all-containers" {
					want = 2
				}
				if err != nil || len(containers) != want {
					t.Fatalf("inventory: %v, %v", containers, err)
				}
				for _, c := range containers {
					if !sweepID.MatchString(c.ID) || c.Name != "/foreign" || c.Image != "image" || c.State != "exited" || c.Labels["x"] != "y" || c.Memory != 123 || c.MemorySwap != 456 {
						t.Fatalf("lost inspect fields: %+v", c)
					}
				}
			case "inspect-gone":
				if err != nil || len(containers) != 0 || listing != 2 {
					t.Fatalf("disappearance: %v, %v", containers, err)
				}
			default:
				if err == nil || len(containers) != 0 {
					t.Fatalf("accepted %s: %v", kind, containers)
				}
				switch err.Error() {
				case "listing containers: container query failed", "invalid container listing", "inspecting container: container query failed", "invalid container inspection":
				default:
					t.Fatalf("non-fixed error: %v", err)
				}
				b, _, _ := budgetSlotsFixture(t)
				b.inventory = inventory
				budgetAssertCause(t, b, "unavailable: the container state could not be determined")
			}
		})
	}
}

func TestBudgetInventoryReadOnly(t *testing.T) {
	runner := &budgetRecordingRunner{t: t, respond: func([]string, io.Writer, io.Writer) (int, error) {
		t.Fatal("refused query reached runner")
		return 0, nil
	}}
	b := budgetDockerInventory{runner: runner}
	for _, args := range [][]string{nil, {"rm"}, {"stop"}, {"kill"}, {"pause"}, {"update"}, {"run"}, {"create"}, {"start"}} {
		data, err := b.budgetQuery(context.Background(), args...)
		if err == nil || err.Error() != "container query refused" || len(data) != 0 {
			t.Fatalf("guard: %v, %v", data, err)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatal("refused commands issued")
	}
}

func TestBudgetInspectComplete(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, row := range []struct {
		name, fields string
		valid        bool
	}{
		{"id-only", ``, false},
		{"missing-name", `,"Config":{},"State":{"Status":"running"}`, false},
		{"missing-config", `,"Name":"/foreign","State":{"Status":"running"}`, false},
		{"null-config", `,"Name":"/foreign","Config":null,"State":{"Status":"running"}`, false},
		{"missing-state", `,"Name":"/foreign","Config":{}`, false},
		{"null-state", `,"Name":"/foreign","Config":{},"State":null`, false},
		{"missing-status", `,"Name":"/foreign","Config":{},"State":{}`, false},
		{"null-status", `,"Name":"/foreign","Config":{},"State":{"Status":null}`, false},
		{"empty-status", `,"Name":"/foreign","Config":{},"State":{"Status":""}`, false},
		{"without-labels", `,"Name":"/foreign","Config":{},"State":{"Status":"running"}`, true},
		{"null-labels", `,"Name":"/foreign","Config":{"Labels":null},"State":{"Status":"running"}`, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			listings := 0
			runner := &budgetRecordingRunner{t: t, respond: func(args []string, stdout, _ io.Writer) (int, error) {
				if args[0] == "ps" {
					listings++
					_, _ = fmt.Fprintln(stdout, id)
				} else {
					_, _ = fmt.Fprintf(stdout, `{"Id":%q%s}`, id, row.fields)
				}
				return 0, nil
			}}
			inventory := budgetDockerInventory{runner: runner}
			c, err := inventory.budgetInspect(context.Background(), id)
			if row.valid {
				if err != nil || c.ID != id || c.Name != "/foreign" || c.State != "running" || c.Labels != nil {
					t.Fatalf("unlabeled inspection: %+v, %v", c, err)
				}
				return
			}
			if err == nil || err.Error() != "invalid container inspection" {
				t.Fatalf("incomplete inspection accepted or wrong error: %+v, %v", c, err)
			}
			b, _, _ := budgetSlotsFixture(t)
			b.inventory = inventory
			budgetAssertCause(t, b, "unavailable: the container state could not be determined")
			if listings != 2 {
				t.Fatalf("incomplete inspection not confirmed by fresh listing: %d", listings)
			}
		})
	}
}
