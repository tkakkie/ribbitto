package main

import "strings"

type measurementSkip struct{ pkg, test, message string }

// This list is a convenience for honest code, not a security control.
// The AST scan checks both directions against the repository's gated skip sites.
const measurementSkipAllowlist = `github.com/tkakkie/ribbitto/cmd/ribbitto	TestFirstUnreadBench	set RIBBITTO_FIRST_UNREAD_BENCH=1 outside CI
github.com/tkakkie/ribbitto/cmd/ribbitto	TestReadAdvanceCost	set RIBBITTO_READ_COST=1
github.com/tkakkie/ribbitto/internal/conversation/conversationpg	TestUnreadBench	set RIBBITTO_UNREAD_BENCH=1 outside CI
github.com/tkakkie/ribbitto/internal/realtime	TestStreamCost	set RIBBITTO_STREAM_COST=1 to measure the stream's cost
github.com/tkakkie/ribbitto/internal/unread/internal/postgres	TestReadMergeCost	set RIBBITTO_READ_COST=1`

func measurementSkips() []measurementSkip {
	var entries []measurementSkip
	for line := range strings.SplitSeq(measurementSkipAllowlist, "\n") {
		pkg, rest, _ := strings.Cut(line, "\t")
		test, message, _ := strings.Cut(rest, "\t")
		entries = append(entries, measurementSkip{pkg, test, message})
	}
	return entries
}
