package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestSummaryNamesContinuationHeader checks every name list with no displayed
// names, a full list, and too little room even for the list's labelled notice.
func TestSummaryNamesContinuationHeader(t *testing.T) {
	for _, header := range []string{"Skipped names:", "Failing leaf tests:", "Failures (observed, total unknown):", "Generated paths:", "Gofmt names:"} {
		for _, mode := range []string{"first name too long", "no retained names", "no space", "list cap"} {
			t.Run(header+"/"+mode, func(t *testing.T) {
				names := []string{strings.Repeat("x", summaryLineLimit)}
				total, room := 4, len(header)+summaryMoreReserve
				if mode == "no retained names" {
					names = nil
				}
				if mode == "no space" {
					room = len(header) - 1
				}
				if mode == "list cap" {
					names = make([]string, summaryNameLimit+1)
					total = len(names)
					room = summaryErrorBudget
				}
				w := summaryWriter{limit: summaryFooterReserve + room}
				w.names(header, names, total)
				got := w.finish(0)
				want := header + " 4 more not shown\n"
				if mode == "no space" {
					want = "Omitted:"
				}
				if mode == "list cap" {
					want = header + "\n"
				}
				if !strings.HasPrefix(got, want) {
					t.Fatalf("continuation must identify list: got %q want prefix %q", got, want)
				}
				shown := strings.Count(got, summaryIndent)
				omitted := total - shown
				if !strings.Contains(got, fmt.Sprintf("; %d names not shown;", omitted)) {
					t.Fatalf("names vanished without accounting: %q", got)
				}
				if mode == "list cap" && !strings.Contains(got, "\nand 1 more\n") {
					t.Fatalf("headed continuation missing: %q", got)
				}
			})
		}
	}
}

// TestSummaryObservedFooterBudget bounds the notice plus accounting even if
// every streamed byte contributed to each omission counter.
func TestSummaryObservedFooterBudget(t *testing.T) {
	w := summaryWriter{namesOmitted: aggregatorStreamLimit, linesOmitted: aggregatorStreamLimit, shortened: aggregatorStreamLimit, excerpts: aggregatorStreamLimit}
	got := w.finishObserved(aggregatorStreamLimit)
	if len(got) > summaryFooterReserve {
		t.Fatalf("observed accounting exceeds reserved footer: %d > %d", len(got), summaryFooterReserve)
	}
}
