package main

import "fmt"

// observedFailuresNotRetained counts storage loss separately from retained
// names omitted by the display budget, in both presentations of the record.
const observedFailuresNotRetained = "Observed failures not named (not retained): %d\n"

// finishObserved never labels an incomplete stream with a total failure count.
// The notice shares the reserved footer so optional output cannot hide it.
func (w *summaryWriter) finishObserved(notRetained int) string {
	if notRetained > 0 {
		fmt.Fprintf(&w.text, observedFailuresNotRetained, notRetained)
	}
	fmt.Fprintf(&w.text, "Omitted: excerpts shown for %d observed failing tests; total unknown; %d names not shown; %d lines not shown; %d lines shortened.\n", w.excerpts, w.namesOmitted, w.linesOmitted, w.shortened)
	return w.text.String()
}
