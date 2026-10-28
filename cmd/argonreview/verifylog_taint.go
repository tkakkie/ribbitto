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
	// Scan complete units before any display cap. Removing newlines catches
	// headers/tokens split across lines; ANSI/invisible removal catches disguise.
	joined := strings.ReplaceAll(text, "\n", "")
	return max(verifyLogDetections(text), verifyLogDetections(joined), verifyLogDetections(verifyLogVisible(joined)))
}

func verifyLogScanOutput(o *retainedOutput) int {
	// A head/tail gap is not reconstructed evidence, but joining it is a safe
	// over-detection here: taint only withholds a unit, never affects a verdict.
	return verifyLogScan(string(o.head) + string(o.tail))
}
