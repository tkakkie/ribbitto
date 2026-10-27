package main

import "context"

// Keep the request separate from the check name so future multi-unit checks
// use the same reservation and cleanup path.
type containerBudget interface {
	budgetReserve(context.Context, budgetRequest) (budgetReservation, budgetResult)
}

func newContainerBudget(runner containerRunner) budgetSlots {
	return budgetSlots{inventory: budgetDockerInventory{runner}, processes: psTable{}, locks: newBudgetLocks(), budgetHook: budgetOtherHook}
}
