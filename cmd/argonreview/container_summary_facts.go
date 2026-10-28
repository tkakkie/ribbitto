package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// summaryCounts excludes parent tests so subtests cannot inflate evidence.
type summaryCounts struct{ passed, failed, skipped int }

// summaryFailure keeps raw identities for sorting, never for direct printing.
type summaryFailure struct {
	name   string
	output summaryLines
}

// summaryFacts is evidence only. A partial parse must never supply success or
// counts; afterEvent records the first event boundary the parser did not trust.
type summaryFacts struct {
	packages, leaves                          summaryCounts
	failures                                  []summaryFailure
	allowed, skipped                          []string
	compiler, packageFailures, stderr, stdout summaryLines
	panicLine, timeoutLine                    string
	events, afterEvent                        int
	err                                       error
	cut                                       bool
	noTests, failureSignal                    bool
	incomplete                                bool
	capName                                   string
	failedTotal, skippedTotal                 int
	observed                                  []string
}

// summaryEvent checks duplicate keys with a small field cap before using Go's
// case-insensitive decoding. This avoids quadratic work on hostile wide objects.
func summaryEvent(line []byte) (containerTestEvent, error) {
	var e containerTestEvent
	d := json.NewDecoder(bytes.NewReader(line))
	token, err := d.Token()
	if err != nil {
		return e, fmt.Errorf("reading event object: %w", err)
	}
	if token != json.Delim('{') {
		return e, errors.New("event is not an object")
	}
	var keys []string
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return e, fmt.Errorf("reading event key: %w", err)
		}
		key, ok := token.(string)
		if !ok || len(keys) == summaryFieldLimit {
			return e, errors.New("event fields exceed limit")
		}
		for _, previous := range keys {
			if strings.EqualFold(previous, key) {
				return e, errors.New("duplicate event field")
			}
		}
		keys = append(keys, key)
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return e, fmt.Errorf("reading event value: %w", err)
		}
	}
	if err := json.Unmarshal(line, &e); err != nil {
		return e, fmt.Errorf("decoding event: %w", err)
	}
	return e, nil
}

// summaryCount handles final states; unfinished work never supplies a pass.
func summaryCount(count *summaryCounts, state string) {
	switch state {
	case "pass":
		count.passed++
	case "fail":
		count.failed++
	case "skip":
		count.skipped++
	}
}
