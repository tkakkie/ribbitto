package main

const (
	// Hard total-stream cap includes both exec copy goroutines.
	aggregatorStreamLimit = 32 << 20
	// Hard line cap bounds decoder temporaries, including newline-free output.
	aggregatorLineLimit = 256 << 10
	// Hard test cap covers explicit runs; parent placeholders are charged.
	aggregatorTestLimit = 32768
	// Hard package cap bounds maps independently of test count.
	aggregatorPackageLimit = 4096
	// Hard memory cap includes capacity and amortised map overhead.
	aggregatorMemoryLimit = 16 << 20
	// The hard timeout remains containerTimeout; the runner owns its clock.

	// Soft per-test output cap keeps equally sized head and tail halves.
	aggregatorTestOutputLimit = 32 << 10
	// Soft shared output cap excludes reserved skip-confirmation slots.
	aggregatorOutputLimit = 1 << 20
	// Soft stderr cap keeps diagnostics without cutting the stream.
	aggregatorStderrLimit = 256 << 10
	// Soft index-entry cap never changes failure or skip counters.
	aggregatorIndexLimit = 1000
	// Soft index-name cap bounds retrieval identities independently.
	aggregatorNameLimit = 256 << 10
	// Soft stdout-head cap reserves command diagnostics for the record.
	aggregatorStdoutLimit = 16 << 10

	// Accounting constants include allocator rounding and amortised map buckets.
	// Keep these conservative: the live-heap test checks their combined bound.
	aggregatorBaseCharge        = 2048 + aggregatorIndexLimit*aggregatorEntryCharge
	aggregatorPackageCharge     = 512
	aggregatorTestCharge        = 384
	aggregatorEntryCharge       = 256
	aggregatorSlotCharge        = 64
	aggregatorNameSlotCharge    = 16
	aggregatorAllocationQuantum = 16
)
