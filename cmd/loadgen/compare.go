package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

type comparison struct {
	Missing            uint64          `json:"missing"`
	MissingByStream    []streamMissing `json:"missing_by_stream"`
	StreamsAffected    uint64          `json:"streams_affected"`
	ReplayedDuplicates uint64          `json:"replayed_duplicates"`
	RepeatedPairs      uint64          `json:"repeated_pairs"`
	DuplicatePosts     uint64          `json:"duplicate_posts"`
	Unexpected         uint64          `json:"unexpected"`
	Resets             uint64          `json:"resets"`
	ReceiptsWatermark  uint64          `json:"receipts_watermark"`
	ExpectedWatermark  uint64          `json:"expected_watermark"`
	WatermarkDiffers   uint64          `json:"watermark_differs"`
}
type streamMissing struct {
	Index   int    `json:"index"`
	Missing uint64 `json:"missing"`
}

// Checking presence separately preserves the distinction between absent and zero.
func requiredObject(raw json.RawMessage, fields ...string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil || len(obj) != len(fields) {
		return nil, fmt.Errorf("invalid object fields")
	}
	for _, field := range fields {
		if value, ok := obj[field]; !ok || string(value) == "null" {
			return nil, fmt.Errorf("missing required field %s", field)
		}
	}
	return obj, nil
}

func readRunFile(path, kind string) (runFile, error) {
	var data runFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return data, fmt.Errorf("reading %s: %w", kind, err)
	}
	body := "streams"
	if kind == "expected" {
		body = "messages"
	}
	obj, err := requiredObject(raw, "header", body)
	if err != nil {
		return data, fmt.Errorf("invalid %s file: %w", kind, err)
	}
	if _, err := requiredObject(obj["header"], "version", "kind", "organization_slug", "channel_id", "initial_cursor", "final_watermark"); err != nil {
		return data, fmt.Errorf("invalid %s header: %w", kind, err)
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("decoding %s: %w", kind, err)
	}
	h := data.Header
	if h.Version != 1 || h.Kind != kind || h.OrganizationSlug == "" || h.ChannelID == "" {
		return data, fmt.Errorf("invalid or unsupported %s header", kind)
	}
	var records []json.RawMessage
	if err := json.Unmarshal(obj[body], &records); err != nil {
		return data, fmt.Errorf("decoding %s records: %w", kind, err)
	}
	marker := regexp.MustCompile(`^loadgen[A-Z2-7]{26}(0|[1-9][0-9]*)Z$`)
	validMarker := func(value string) bool { return value == "" || marker.MatchString(value) }
	previous := h.InitialCursor
	for i, raw := range records {
		if kind == "expected" {
			if _, err := requiredObject(raw, "sequence", "marker"); err != nil {
				return data, fmt.Errorf("invalid message %d: %w", i, err)
			}
			m := data.Messages[i]
			if m.Sequence <= previous || m.Sequence > h.FinalWatermark || !validMarker(m.Marker) {
				return data, fmt.Errorf("invalid expected message %d", i)
			}
			previous = m.Sequence
			continue
		}
		s, err := requiredObject(raw, "index", "sequences", "reset", "reconnects")
		if err != nil {
			return data, fmt.Errorf("invalid stream %d: %w", i, err)
		}
		if data.Streams[i].Index != i {
			return data, fmt.Errorf("duplicate or unordered stream index at %d", i)
		}
		if _, err := requiredObject(s["reconnects"], "established", "503", "refused", "other"); err != nil {
			return data, fmt.Errorf("invalid reconnects for stream %d: %w", i, err)
		}
		var sequences map[string]json.RawMessage
		if err := json.Unmarshal(s["sequences"], &sequences); err != nil {
			return data, fmt.Errorf("invalid sequences for stream %d: %w", i, err)
		}
		for key, raw := range sequences {
			seq, err := strconv.ParseUint(key, 10, 64)
			if err != nil || seq == 0 || strconv.FormatUint(seq, 10) != key {
				return data, fmt.Errorf("invalid sequence in stream %d", i)
			}
			if _, err := requiredObject(raw, "arrivals", "marker"); err != nil {
				return data, fmt.Errorf("invalid receipt in stream %d: %w", i, err)
			}
			r := data.Streams[i].Sequences[seq]
			if r.Arrivals == 0 || !validMarker(r.Marker) {
				return data, fmt.Errorf("invalid receipt in stream %d", i)
			}
		}
	}
	return data, nil
}

func compareFiles(receiptsPath, expectedPath string, out io.Writer) error {
	receipts, err := readRunFile(receiptsPath, "receipts")
	if err != nil {
		return err
	}
	expected, err := readRunFile(expectedPath, "expected")
	if err != nil {
		return err
	}
	a, b := receipts.Header, expected.Header
	if a.Version != b.Version || a.OrganizationSlug != b.OrganizationSlug || a.ChannelID != b.ChannelID || a.InitialCursor != b.InitialCursor {
		return fmt.Errorf("run headers differ")
	}
	r := comparison{MissingByStream: []streamMissing{}, ReceiptsWatermark: a.FinalWatermark, ExpectedWatermark: b.FinalWatermark}
	if a.FinalWatermark != b.FinalWatermark {
		r.WatermarkDiffers = 1
	}
	sequences, posts := make(map[uint64]bool), make(map[string]uint64)
	for _, m := range expected.Messages {
		sequences[m.Sequence] = true
		if m.Marker != "" {
			posts[m.Marker]++
			if posts[m.Marker] == 2 {
				r.DuplicatePosts++
			}
		}
	}
	for _, stream := range receipts.Streams {
		missing := uint64(len(sequences))
		r.Resets += stream.Reset
		for seq, receipt := range stream.Sequences {
			if sequences[seq] {
				missing--
			} else {
				r.Unexpected++
			}
			r.ReplayedDuplicates += receipt.Arrivals - 1
			if receipt.Arrivals > 1 {
				r.RepeatedPairs++
			}
		}
		r.MissingByStream = append(r.MissingByStream, streamMissing{stream.Index, missing})
		r.Missing += missing
		if missing > 0 {
			r.StreamsAffected++
		}
	}
	if err := json.NewEncoder(out).Encode(r); err != nil {
		return fmt.Errorf("writing comparison: %w", err)
	}
	return nil
}
