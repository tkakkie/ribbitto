package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// The runner and virtual clock exercise the production deadline without waiting
// on wall time or depending on Docker's speed or inventory size.
func TestBudgetInventoryDeadline(t *testing.T) {
	for _, parentLimit := range []time.Duration{time.Hour, 5 * time.Second} {
		t.Run(parentLimit.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				calls := 0
				runner := containerRunnerFunc(func(ctx context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
					calls++
					select {
					case <-ctx.Done():
						return -1, ctx.Err()
					case <-time.After(12 * time.Second):
					}
					if args[0] == "ps" {
						_, _ = fmt.Fprintln(stdout, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
					} else {
						c := otherFixture("dev", "0", "running")
						c.ID = args[len(args)-1]
						_ = json.NewEncoder(stdout).Encode(c)
					}
					return 0, nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), parentLimit)
				defer cancel()
				b, _, _ := budgetSlotsFixture(t)
				b.inventory = budgetDockerInventory{runner: runner}
				b.budgetHook = budgetOtherHook
				r, cause := b.budgetReserve(ctx, budgetSingle)
				if r.release != nil {
					r.release()
					t.Fatal("expired inventory granted reservation")
				}
				want := budgetStateUnknown
				if parentLimit < budgetInventoryTimeout {
					want = budgetCanceled
				}
				if cause != want {
					t.Fatalf("deadline cause = %q; want %q", cause, want)
				}
				if elapsed := time.Since(start); elapsed != min(parentLimit, budgetInventoryTimeout) {
					t.Fatalf("inventory elapsed = %v; want %v", elapsed, min(parentLimit, budgetInventoryTimeout))
				}
				if calls > 5 {
					t.Fatalf("deadline did not bound inventory: %d calls", calls)
				}
			})
		})
	}
	// Recompiling the name grammar on every read is forbidden independently of
	// timing: pin reuse of the sweep matcher without an allocation benchmark.
	source, err := os.ReadFile("budget_slots.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "budget_slots.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.SelectorExpr); ok && call.Sel.Name == "MustCompile" {
			t.Error("budget read recompiles the container name regexp")
		}
		return true
	})
}

type containerRunnerFunc func(context.Context, []string, []string, io.Writer, io.Writer) (int, error)

func (f containerRunnerFunc) run(ctx context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
	return f(ctx, args, env, stdout, stderr)
}

func TestBudgetCombinedInventoryComplete(t *testing.T) {
	for _, row := range []struct {
		name, host string
		line       string
	}{
		{"missing host", "", otherStateLine},
		{"null host", `,"HostConfig":null`, otherStateLine},
		{"missing memory", `,"HostConfig":{"MemorySwap":0}`, otherStateLine},
		{"missing swap", `,"HostConfig":{"Memory":0}`, otherStateLine},
		{"null memory", `,"HostConfig":{"Memory":null,"MemorySwap":0}`, otherStateLine},
		{"null swap", `,"HostConfig":{"Memory":0,"MemorySwap":null}`, otherStateLine},
		{"explicit zero", `,"HostConfig":{"Memory":0,"MemorySwap":0}`, otherUnlimitedLine},
	} {
		t.Run(row.name, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			runner := &budgetRecordingRunner{t: t, respond: func(args []string, stdout, _ io.Writer) (int, error) {
				if args[0] == "ps" {
					_, _ = fmt.Fprintln(stdout, id)
				} else {
					_, _ = fmt.Fprintf(stdout, `{"Id":%q,"Name":"/dev","Config":{"Labels":{"argonreview.budget":"dev","argonreview.slot":"0"}},"State":{"Status":"running"}%s}`, id, row.host)
				}
				return 0, nil
			}}
			b, _ := budgetCombinedFixture(t)
			b.inventory = budgetDockerInventory{runner: runner}
			budgetAssertCause(t, b, strings.TrimPrefix(row.line, "NOT VERIFIED: verification budget "))
			if len(runner.calls) != 3 {
				t.Fatalf("inventory calls = %d; want one listing, inspect, confirmation", len(runner.calls))
			}
		})
	}
}

func TestBudgetCombinedInventoryAppearance(t *testing.T) {
	for _, unstable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unstable=%v", unstable), func(t *testing.T) {
			listings, inspects := 0, 0
			runner := &budgetRecordingRunner{t: t, respond: func(args []string, stdout, _ io.Writer) (int, error) {
				if args[0] == "ps" {
					listings++
					count := min(listings, 2)
					if unstable {
						count = listings
					}
					for i := 1; i <= count; i++ {
						_, _ = fmt.Fprintf(stdout, "%064x\n", i)
					}
				} else {
					inspects++
					c := otherFixture("unregistered", "", "exited")
					c.ID = args[len(args)-1]
					if c.ID == fmt.Sprintf("%064x", 2) {
						c.State.Status = "running"
					}
					_ = json.NewEncoder(stdout).Encode(c)
				}
				return 0, nil
			}}
			b, _ := budgetCombinedFixture(t)
			b.inventory = budgetDockerInventory{runner: runner}
			want, wantLists, wantInspects := otherUnregisteredLine, 4, 3
			if unstable {
				want, wantLists, wantInspects = otherStateLine, 6, 9
			}
			budgetAssertCause(t, b, strings.TrimPrefix(want, "NOT VERIFIED: verification budget "))
			if listings != wantLists || inspects != wantInspects {
				t.Fatalf("snapshot calls = %d listings, %d inspects; want %d, %d", listings, inspects, wantLists, wantInspects)
			}
		})
	}
}

func TestBudgetCombinedMalformedDisappears(t *testing.T) {
	listings := 0
	runner := &budgetRecordingRunner{t: t, respond: func(args []string, stdout, _ io.Writer) (int, error) {
		if args[0] == "ps" {
			listings++
			if listings == 1 {
				_, _ = fmt.Fprintln(stdout, strings.Repeat("a", 64))
			}
		} else {
			_, _ = io.WriteString(stdout, `{}`)
		}
		return 0, nil
	}}
	b, _ := budgetCombinedFixture(t)
	b.inventory = budgetDockerInventory{runner: runner}
	budgetAssertCause(t, b, string(budgetStateUnknown))
	if listings != 2 {
		t.Fatalf("malformed inspection omitted confirmation: %d listings", listings)
	}
}
