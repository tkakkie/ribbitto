package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type browserSummary struct {
	shard, product, revision, jsVersion string
	os, imageOS, imageVersion           string
	runID, runAttempt, date             string
	messageTimeSelected                 bool
	messageTimePassed                   bool
}

func formatBrowserSummary(s browserSummary) string {
	if s.shard == "" {
		s.shard = "all"
	}
	scenario := "message-time not in this shard"
	if s.messageTimeSelected {
		scenario = "message-time not passed"
		if s.messageTimePassed {
			scenario = "message-time passed"
		}
	}
	details := []string{"rod via DevTools Browser.getVersion"}
	if s.revision != "" {
		details = append(details, "revision "+s.revision)
	}
	if s.jsVersion != "" {
		details = append(details, "JS "+s.jsVersion)
	}
	parts := []string{
		fmt.Sprintf("browser check: shard %s, scenario %s", s.shard, scenario),
		fmt.Sprintf("browser %s (%s)", s.product, strings.Join(details, ", ")),
	}
	var environment []string
	if s.os != "" {
		environment = append(environment, "OS "+s.os)
	}
	if image := strings.Trim(strings.Join([]string{s.imageOS, s.imageVersion}, "/"), "/"); image != "" {
		environment = append(environment, "runner image "+image)
	}
	if len(environment) > 0 {
		parts = append(parts, strings.Join(environment, ", "))
	}
	if run := strings.Trim(strings.Join([]string{s.runID, s.runAttempt}, "/"), "/"); run != "" {
		parts = append(parts, "run "+run)
	}
	if s.date != "" {
		parts = append(parts, s.date)
	}
	return strings.Join(parts, "; ")
}

func browserSummaryOS() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

func appendBrowserSummary(line string) error {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening step summary: %w", err)
	}
	_, writeErr := fmt.Fprintln(file, line)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("appending step summary: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing step summary: %w", closeErr)
	}
	return nil
}

func TestBrowserSummary(t *testing.T) {
	full := browserSummary{
		shard: "2/3", product: "HeadlessChrome/131.0.6778.85", revision: "abc123", jsVersion: "13.1.201.16",
		os: "Ubuntu 24.04.1 LTS", imageOS: "ubuntu24", imageVersion: "20251001.1",
		runID: "12345", runAttempt: "2", date: "2026-10-26", messageTimeSelected: true, messageTimePassed: true,
	}
	browser := "browser HeadlessChrome/131.0.6778.85 (rod via DevTools Browser.getVersion)"
	for _, tt := range []struct {
		name  string
		input browserSummary
		want  string
	}{
		{
			name: "all parts", input: full,
			want: "browser check: shard 2/3, scenario message-time passed; browser HeadlessChrome/131.0.6778.85 (rod via DevTools Browser.getVersion, revision abc123, JS 13.1.201.16); OS Ubuntu 24.04.1 LTS, runner image ubuntu24/20251001.1; run 12345/2; 2026-10-26",
		},
		{
			name: "missing OS and runner", input: browserSummary{shard: "2/3", product: full.product, date: full.date, messageTimeSelected: true, messageTimePassed: true},
			want: "browser check: shard 2/3, scenario message-time passed; " + browser + "; 2026-10-26",
		},
		{
			name: "empty shard", input: browserSummary{product: full.product, messageTimeSelected: true, messageTimePassed: true},
			want: "browser check: shard all, scenario message-time passed; " + browser,
		},
		{
			name: "not in shard", input: browserSummary{shard: "1/3", product: full.product, messageTimePassed: true},
			want: "browser check: shard 1/3, scenario message-time not in this shard; " + browser,
		},
		{
			name: "not passed", input: browserSummary{shard: "2/3", product: full.product, messageTimeSelected: true},
			want: "browser check: shard 2/3, scenario message-time not passed; " + browser,
		},
		{
			name: "partial runner and run", input: browserSummary{product: full.product, imageVersion: full.imageVersion, runID: full.runID},
			want: "browser check: shard all, scenario message-time not in this shard; " + browser + "; runner image 20251001.1; run 12345",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBrowserSummary(tt.input); got != tt.want {
				t.Fatalf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBrowserSummaryAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary")
	t.Setenv("GITHUB_STEP_SUMMARY", path)
	for _, line := range []string{"existing summary", "browser check: first", "browser check: second"} {
		if err := appendBrowserSummary(line); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "existing summary\nbrowser check: first\nbrowser check: second\n"; string(got) != want {
		t.Fatalf("summary file = %q, want %q", got, want)
	}
}
