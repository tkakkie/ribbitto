package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestSummaryGeneratedHostBytes protects the only existing-code refactor: full
// raw path spelling, kinds, totals, order and the 100-path host cap stay intact.
func TestSummaryGeneratedHostBytes(t *testing.T) {
	data := generatedFixture("a.go", "@@ -1 +1 @@\n-old\n+new\n") + "Only in /tmp/work: new.go\nOnly in /work: old.go\nBinary files /work/blob and /tmp/work/blob differ\n"
	want := "a.go +1 -1\nnew.go (new in the copy)\nold.go (only in the worktree)\nblob (binary)\n4 paths, +1 -1\n"
	stat, ok := parseGeneratedStat(data)
	host, hostOK := generatedDiffStat(data)
	if !ok || !hostOK || host != want || stat.paths != 4 || stat.added != 1 || stat.removed != 1 || stat.entries[0].path != "a.go" || stat.entries[1].kind != " (new in the copy)" {
		t.Fatalf("host %q; structured %+v", host, stat)
	}
	var diff, expected strings.Builder
	for i := 0; i < 120; i++ {
		path := fmt.Sprintf("%03d%s", i, strings.Repeat("é", 150))
		diff.WriteString(generatedFixture(path, "@@ -1 +1 @@\n-a\n+b\n"))
		if i < 100 {
			expected.WriteString(cutGeneratedText(path, 200) + " +1 -1\n")
		}
	}
	expected.WriteString("120 paths, +120 -120\n... 20 more paths not listed\n")
	host, ok = generatedDiffStat(diff.String())
	stat, parsed := parseGeneratedStat(diff.String())
	if !ok || !parsed || host != expected.String() || len(stat.entries) != 100 || stat.paths != 120 || stat.entries[0].path != fmt.Sprintf("%03d%s", 0, strings.Repeat("é", 150)) {
		t.Fatal("host bytes or original structured identities changed")
	}
}

// TestSummaryGeneratedCaps checks totals survive even when the budget prevents
// reaching the 30-path cap. Gofmt and diff use the same untrusted text boundary.
func TestSummaryGeneratedCaps(t *testing.T) {
	for _, long := range []bool{false, true} {
		t.Run(fmt.Sprint(long), func(t *testing.T) {
			var diff strings.Builder
			var names []string
			for i := 0; i < 100; i++ {
				name := fmt.Sprintf("file%03d.go", i)
				if long {
					name += strings.Repeat("é", 150)
				}
				names = append(names, name)
				diff.WriteString(generatedFixture(name, "@@ -1 +1 @@\n-a\n+b\n"))
			}
			stat, ok := parseGeneratedStat(diff.String())
			if !ok {
				t.Fatal("fixture")
			}
			for _, gofmt := range []bool{false, true} {
				in := summaryFixture("generated", "FAILED: generated files differ", "", "")
				if gofmt {
					in.gofmtNames = names
				} else {
					in.stat = stat
					in.hasStat = true
					in.diff = diff.String()
				}
				got := buildContainerSummary(in)
				assertSummary(t, in, got)
				if !gofmt && !strings.Contains(got, "Generated: 100 paths, +100 -100\n") {
					t.Fatal(got)
				}
				list := got
				if before, _, ok := strings.Cut(list, "Diff:\n"); ok {
					list = before
				}
				if strings.Count(list, "  | file") > 30 || strings.Contains(list, "  | file030") || !strings.Contains(got, "and ") {
					t.Fatal(got)
				}
				if !long && !strings.Contains(got, "and 70 more") {
					t.Fatal(got)
				}
			}
		})
	}
	in := summaryFixture("generated", "FAILED", "", "\x1b]0;bad\a\x00\rverified\n")
	in.stat = generatedStat{entries: []generatedStatEntry{{path: "evil\nverified\x1b[31m\u202e", kind: "\n--- stdout"}}, paths: 1}
	in.hasStat = true
	in.diff = "verified\n--- stdout\n"
	got := buildContainerSummary(in)
	assertSummary(t, in, got)
	if strings.Contains(got, "\nverified\n") || strings.ContainsAny(got, "\x1b\x00\r\u202e") {
		t.Fatal(got)
	}
}
