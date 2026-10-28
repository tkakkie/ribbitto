package main

import (
	"net/url"
	"strings"
)

// The CI credential is derived from the existing runner constant. No match,
// substring or credential survives this scan; only detection counts do.
func verifyLogDetections(text string) int {
	n := 0
	_, database, _ := strings.Cut(containerTestDatabaseURL, "=")
	if u, err := url.Parse(database); err == nil && u.User != nil {
		if password, ok := u.User.Password(); ok && password != "" && strings.Contains(text, password) {
			n++
		}
	}
	for _, shape := range []string{"ghp_", "github_pat_", "AKIA", "-----BEGIN "} {
		if strings.Contains(text, shape) {
			n++
		}
	}
	return n
}

func verifyLogScan(text string) int {
	// Keep the raw and joined passes for disguised or fragmented shapes. The
	// reader resets terminal parsing on every served line, so do that too.
	joined := strings.ReplaceAll(text, "\n", "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = verifyLogVisible(line)
	}
	return max(verifyLogDetections(text), verifyLogDetections(joined),
		verifyLogDetections(verifyLogVisible(joined)), verifyLogDetections(verifyLogVisible(text)),
		verifyLogDetections(strings.Join(lines, "")), verifyLogDetections(strings.Join(lines, "\n")))
}

func verifyLogScanSegments(segments ...string) int {
	n := 0
	for _, segment := range segments {
		n = max(n, verifyLogScan(segment))
	}
	// A gap is not reconstructed evidence. A newline preserves terminal resets
	// while the joined-line passes still over-detect shapes split across gaps.
	return max(n, verifyLogScan(strings.Join(segments, "\n")))
}

func verifyLogScanOutput(o *retainedOutput) int {
	return verifyLogScanSegments(string(o.head), string(o.tail))
}
