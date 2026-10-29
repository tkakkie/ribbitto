package main

import (
	"errors"
	"strconv"
	"strings"
)

type agyCheckHookEvent struct {
	Index, Call     int
	Kind, Word, Tag string
}

const agyCheckNoOverlap = "no overlapping hook invocations"

func agyCheckReadEvents(isolation, dir string, f agyCheckFiles) ([]agyCheckHookEvent, error) {
	records, err := agyCheckReadRecords(isolation, dir, "events", 2*agyCheckHookCallCap, f)
	if err != nil {
		return nil, err
	}
	var events []agyCheckHookEvent
	for i, data := range records {
		fields := strings.Split(strings.TrimSuffix(string(data), "\n"), " ")
		if len(fields) != 2 && len(fields) != 4 {
			return nil, errors.New("invalid hook event")
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n <= 0 || fields[1] != strconv.Itoa(n) {
			return nil, errors.New("invalid hook event")
		}
		e := agyCheckHookEvent{Index: i + 1, Call: n, Kind: fields[0]}
		switch {
		case len(fields) == 2 && e.Kind == "end":
		case len(fields) == 4 && e.Kind == "start" && (fields[2] == "allow" || fields[2] == "deny") && (fields[3] == "-" || agyCheckHookTag(fields[3])):
			e.Word, e.Tag = fields[2], fields[3]
		default:
			return nil, errors.New("invalid hook event")
		}
		events = append(events, e)
	}
	return events, nil
}

// Group sizes come from the transcript; only exclusive event creations prove overlap.
func agyCheckParallel(groups []int, claims []agyCheckClaim, events []agyCheckHookEvent) (int, string) {
	n := len(claims)
	if len(events) != 2*n {
		return 0, "wrong hook event count"
	}
	byCall := make(map[int]agyCheckClaim, n)
	for _, c := range claims {
		byCall[c.N] = c
	}
	starts, ends := make(map[int]int, n), make(map[int]int, n)
	var order []int
	for i, e := range events {
		if e.Index != i+1 {
			return 0, "non-contiguous hook events"
		}
		c, known := byCall[e.Call]
		if !known {
			return 0, "unknown hook call number"
		}
		switch e.Kind {
		case "start":
			if starts[e.Call] != 0 {
				return 0, "duplicate hook start"
			}
			if e.Word != c.Word || e.Tag != c.Tag {
				return 0, "hook calls and events disagree"
			}
			starts[e.Call] = e.Index
			order = append(order, e.Call)
		case "end":
			if ends[e.Call] != 0 {
				return 0, "duplicate hook end"
			}
			if starts[e.Call] == 0 {
				return 0, "hook end before start"
			}
			ends[e.Call] = e.Index
		default:
			return 0, "invalid hook event"
		}
	}
	// 2N known, unique, ordered records imply one start and end for every call.
	total := 0
	for _, size := range groups {
		total += size
	}
	if total != n {
		return 0, "response group sizes disagree"
	}
	batches, offset, previousEnd := 0, 0, 0
	for _, size := range groups {
		latestEnd, overlap := 0, false
		for _, call := range order[offset : offset+size] {
			if starts[call] <= previousEnd {
				return 0, "overlap across response groups"
			}
			if starts[call] < latestEnd {
				overlap = true
			}
			latestEnd = max(latestEnd, ends[call])
		}
		if size >= 2 && overlap {
			batches++
		}
		previousEnd = max(previousEnd, latestEnd)
		offset += size
	}
	if batches == 0 {
		return 0, agyCheckNoOverlap
	}
	if batches < 2 {
		return batches, "fewer than two parallel batches"
	}
	return batches, ""
}
