package main

import (
	"bytes"
	"context"
	"strings"
)

// containerAggregator serialises the two exec copy goroutines. Its own stop is
// recorded before cancellation, so a cancelled child cannot become row E6.
type containerAggregator struct {
	record                         *containerRecord
	cancel                         context.CancelFunc
	packages                       map[string]*aggregatorPackage
	allowlist                      []measurementSkip
	allowedNames                   []string
	lines                          [2][]byte
	tests, outputBytes, indexBytes int
	failureSignal, noTests         bool
	first, last                    *aggregatorEntry
	skipped                        []string
	sealedState                    aggregatorRunState
	sealedCode                     int
	sealedErr                      error
}

type aggregatorStream struct {
	aggregator *containerAggregator
	stderr     bool
}

// newContainerAggregator reserves the index before any stop can forbid growth.
func newContainerAggregator(cancel context.CancelFunc, allowlist []measurementSkip) *containerAggregator {
	// The base envelope includes this allocation and the fixed index storage.
	a := &containerAggregator{record: &containerRecord{}, cancel: cancel, allowlist: allowlist}
	if a.charge(aggregatorBaseCharge) {
		a.packages = make(map[string]*aggregatorPackage)
	}
	// Trusted names must remain publishable even after a cap forbids new
	// allocations. Each observed test can match at most one trusted identity.
	if a.charge(len(allowlist) * 2 * aggregatorNameSlotCharge) {
		a.allowedNames = make([]string, len(allowlist))
		a.record.allowed = make([]string, 0, len(allowlist))
		for i, entry := range allowlist {
			a.allowedNames[i] = a.clone(entry.pkg + "/" + entry.test)
		}
	}
	return a
}

// allowedName returns precharged trusted text rather than cloning after a stop.
func (a *containerAggregator) allowedName(pkg, test string) string {
	for i, entry := range a.allowlist {
		if entry.pkg == pkg && entry.test == test {
			return a.allowedNames[i]
		}
	}
	return ""
}

// stdoutWriter and stderrWriter share state because their bytes share one cap.
func (a *containerAggregator) stdoutWriter() aggregatorStream { return aggregatorStream{aggregator: a} }
func (a *containerAggregator) stderrWriter() aggregatorStream {
	return aggregatorStream{aggregator: a, stderr: true}
}

// stop preserves the first loss of understanding, including its event boundary.
func (a *containerAggregator) stop(reason aggregatorStopReason, capName string) {
	if a.record.stop != stopNone {
		return
	}
	a.record.stop, a.record.capName = reason, capName
	a.record.afterEvent = a.record.events
	if a.cancel != nil {
		a.cancel()
	}
}

// charge checks before allocation, including replacement buffers while their
// old backing arrays are still live. Callers release the old charge afterwards.
func (a *containerAggregator) charge(n int) bool {
	if a.record.stop != stopNone {
		return false
	}
	if n > aggregatorMemoryLimit-a.record.accounted {
		a.stop(stopCap, "retained memory")
		return false
	}
	a.record.accounted += n
	return true
}

// textCharge covers the allocator's size class rather than just string length.
func textCharge(n int) int {
	return (n + aggregatorAllocationQuantum - 1) &^ (aggregatorAllocationQuantum - 1)
}

// clone prevents a retained identity or marker from pinning decoder storage.
func (a *containerAggregator) clone(s string) string {
	if !a.charge(textCharge(len(s))) {
		return ""
	}
	return strings.Clone(s)
}

// grow makes capacity explicit; append must never choose uncharged capacity.
func (a *containerAggregator) grow(buf *[]byte, need, limit int) bool {
	if need <= cap(*buf) {
		return true
	}
	size := min(limit, max(need, max(aggregatorAllocationQuantum, cap(*buf)*2)))
	if !a.charge(size) {
		return false
	}
	next := make([]byte, len(*buf), size)
	copy(next, *buf)
	a.record.accounted -= cap(*buf)
	*buf = next
	return true
}

// Write acknowledges discarded bytes too: exec must drain pipes after a stop.
func (s aggregatorStream) Write(p []byte) (int, error) {
	a := s.aggregator
	a.record.mu.Lock()
	defer a.record.mu.Unlock()
	n := len(p)
	if a.record.stop != stopNone || a.record.sealed {
		return n, nil
	}
	size := min(n, aggregatorStreamLimit-a.record.streamed)
	a.record.streamed += size
	data := p[:size]
	which := 0
	if s.stderr {
		which = 1
		a.keep(&a.record.stderr, string(data[:min(len(data), aggregatorStderrLimit)]), aggregatorStderrLimit, false)
		a.record.stderr.total += max(0, len(data)-aggregatorStderrLimit)
	} else {
		a.record.stdoutBytes += size
	}
	for len(data) > 0 && a.record.stop == stopNone {
		end := bytes.IndexByte(data, '\n')
		part := len(data)
		if end >= 0 {
			part = end
		}
		line := &a.lines[which]
		if part > aggregatorLineLimit-len(*line) {
			a.stop(stopCap, "line length")
			break
		}
		if !a.grow(line, len(*line)+part, aggregatorLineLimit) {
			break
		}
		*line = append(*line, data[:part]...)
		data = data[part:]
		if end < 0 {
			break
		}
		data = data[1:]
		if s.stderr {
			a.notice(string(*line))
		} else {
			a.event(*line)
		}
		*line = (*line)[:0]
	}
	if size < n {
		a.stop(stopCap, "total output")
	}
	return n, nil
}
