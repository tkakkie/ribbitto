package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type budgetInventoryFunc func(context.Context) ([]budgetContainer, error)

func (f budgetInventoryFunc) budgetList(ctx context.Context) ([]budgetContainer, error) {
	return f(ctx)
}

func TestBudgetReservationDeadline(t *testing.T) {
	inBubble := func(check func(*testing.T)) func(*testing.T) {
		return func(t *testing.T) { synctest.Test(t, check) }
	}
	for _, kind := range []string{"first inventory", "under lock", "late attempt", "sleep", "parent cancellation"} {
		t.Run(kind, inBubble(func(t *testing.T) {
			b, _, _ := budgetSlotsFixture(t)
			// Virtual time keeps scheduling load out of the deadline margin; the
			// watchdog still bounds an inventory with an independent deadline.
			defaults := newBudgetLocks()
			b.locks.limit = defaults.limit
			b.locks.poll = b.locks.limit * 3 / 4
			if kind == "sleep" {
				b.locks.poll = b.locks.limit + defaults.poll
			}
			if defaults.limit <= budgetInventoryTimeout || defaults.poll >= defaults.limit {
				t.Fatal("production bounds changed; review deadline fixture")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads, active := 0, 0
			var firstDeadline time.Time
			b.inventory = budgetInventoryFunc(func(readCtx context.Context) ([]budgetContainer, error) {
				reads++
				active++
				defer func() { active-- }()
				deadline, ok := readCtx.Deadline()
				if !ok {
					t.Error("inventory has no reservation deadline")
				}
				if reads == 1 {
					firstDeadline = deadline
				} else if !deadline.Equal(firstDeadline) {
					t.Error("inventory deadline restarted between reads")
				}
				if reads == 1 {
					switch kind {
					case "under lock":
						return nil, nil
					case "late attempt":
						return []budgetContainer{budgetContainerFixture("0"), budgetContainerFixture("1,2")}, nil
					case "sleep":
						return []budgetContainer{budgetContainerFixture("")}, nil
					}
				}
				if kind == "late attempt" && (time.Until(deadline) <= 0 || time.Until(deadline) >= b.locks.poll) {
					t.Error("regression attempt did not start just before deadline")
				}
				if kind == "parent cancellation" {
					cancel()
				}
				watchdog := time.NewTimer(4 * b.locks.limit)
				defer watchdog.Stop()
				select {
				case <-readCtx.Done():
				case <-watchdog.C:
					t.Error("inventory outlived reservation deadline")
				}
				return nil, errors.New("inventory interrupted")
			})
			parent, tree := t.TempDir(), containerFixture(t)
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Error("Docker reached after reservation deadline")
				return 0, nil
			}}
			result := containerExecution{budget: b}
			start := time.Now()
			report, err := runContainerExecution(ctx, tree, "build", "./...", runner, parent, &result)
			wantElapsed := b.locks.limit
			if kind == "parent cancellation" {
				wantElapsed = 0
			}
			if elapsed := time.Since(start); elapsed != wantElapsed {
				t.Errorf("reservation duration: %v; want %v", elapsed, wantElapsed)
			}
			want, wantReads := budgetStateUnknown, 1
			if kind == "under lock" || kind == "late attempt" {
				wantReads = 2
			}
			if kind == "late attempt" {
				want = budgetBusy
			}
			if kind == "sleep" {
				want = budgetInvalidLabel
			}
			if kind == "parent cancellation" {
				if !errors.Is(err, context.Canceled) || report != "" {
					t.Fatalf("parent cancellation: report %q, error %v", report, err)
				}
			} else if err != nil || result.code != 1 || !strings.HasPrefix(report, want.line("verification budget")+"\n") {
				t.Fatalf("deadline cause: report %q, code %d, error %v; want %q", report, result.code, err, want)
			}
			if reads != wantReads || active != 0 {
				t.Fatalf("inventory work leaked or retried: reads %d, active %d; want %d, 0", reads, active, wantReads)
			}
			assertNoContainerCopy(t, parent)
			b.locks.limit = 0
			budgetTake(t, b.locks, budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2)()
		}))
	}
}

func TestBudgetReservationDeadlineAfterGrant(t *testing.T) {
	b, i, _ := budgetSlotsFixture(t)
	now := time.Unix(0, 0)
	b.locks.now = func() time.Time { return now }
	b.locks.limit = newBudgetLocks().limit
	i.read = func(n int) {
		if n == 2 {
			// Expire the injected clock between the attempt's last context check
			// and the reservation loop accepting its granted lock.
			now = now.Add(b.locks.limit)
		}
	}
	budgetAssertCause(t, b, string(budgetStateUnknown))
	b.locks.limit = 0
	budgetTake(t, b.locks, budgetSlot0, budgetSlot1, budgetSlot2)()
}

func TestBudgetReservationRemainingSleep(t *testing.T) {
	b, i, _ := budgetSlotsFixture(t)
	now := time.Unix(0, 0)
	b.locks.now = func() time.Time { return now }
	b.locks.limit, b.locks.poll = 3*time.Second/2, time.Second
	i.err = errors.New("unavailable")
	sleeps := 0
	b.locks.sleep = func(_ context.Context, d time.Duration) error {
		sleeps++
		want := b.locks.poll
		if sleeps == 2 {
			want = b.locks.limit - b.locks.poll
		}
		if d != want || sleeps > 2 {
			t.Fatalf("sleep exceeds remaining reservation time: sleep %d, duration %v; want %v", sleeps, d, want)
		}
		now = now.Add(d)
		return nil
	}
	budgetAssertCause(t, b, string(budgetStateUnknown))
	if sleeps != 2 {
		t.Fatalf("sleeps: %d; want 2", sleeps)
	}
}

func TestBudgetReservationProcessDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, i, _ := budgetSlotsFixture(t)
		b.locks.limit = newBudgetLocks().limit
		calls := 0
		b.processes = psTable{runContext: func(ctx context.Context, _ ...string) (string, error) {
			calls++
			select {
			case <-ctx.Done():
			case <-time.After(2 * b.locks.limit):
			}
			return "", errors.New("process listing interrupted")
		}}
		start := time.Now()
		budgetAssertCause(t, b, string(budgetStateUnknown))
		if elapsed := time.Since(start); elapsed != b.locks.limit || calls != 1 || i.reads != 1 {
			t.Fatalf("process deadline: elapsed %v, calls %d, inventory reads %d; want %v, 1, 1", elapsed, calls, i.reads, b.locks.limit)
		}
	})
}

func TestBudgetReservationInventoryDeadline(t *testing.T) {
	tree, copies := containerFixture(t), t.TempDir()
	b, _, _ := budgetSlotsFixture(t)
	b.locks.limit = newBudgetLocks().limit
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		calls, active := 0, 0
		inventory := budgetDockerInventory{runner: containerRunnerFunc(func(ctx context.Context, args, _ []string, _, _ io.Writer) (int, error) {
			calls++
			active++
			defer func() { active-- }()
			if args[0] != "ps" {
				t.Fatalf("unexpected inventory call: %v", args)
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * budgetInventoryTimeout):
			}
			return -1, errors.New("inventory interrupted")
		})}
		b.inventory = budgetInventoryFunc(func(ctx context.Context) ([]budgetContainer, error) {
			if time.Since(start) < b.locks.limit-b.locks.poll {
				return []budgetContainer{budgetContainerFixture("0"), budgetContainerFixture("1,2")}, nil
			}
			return inventory.budgetList(ctx)
		})
		runner := containerRunnerFunc(func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
			t.Error("Docker run reached after late inventory")
			return 0, nil
		})
		result := containerExecution{budget: b}
		report, err := runContainerExecution(context.Background(), tree, "build", "./...", runner, copies, &result)
		if err != nil || !strings.HasPrefix(report, budgetBusy.line("verification budget")+"\n") || time.Since(start) != b.locks.limit || calls != 1 || active != 0 {
			t.Fatalf("late inventory: report %q, error %v, elapsed %v, calls %d, active %d", report, err, time.Since(start), calls, active)
		}
		assertNoContainerCopy(t, copies)
	})
}
