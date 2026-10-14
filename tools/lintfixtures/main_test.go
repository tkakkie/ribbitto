package main

import (
	"strings"
	"testing"
)

// recorded is golangci-lint v2.14.0's output for the fixtures, trimmed to
// each report's first line.
const recorded = `internal/lintfixture/bodyclose.go:9:28: response body must be closed (bodyclose)
internal/lintfixture/badroot/root.go:8:2: import 'github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge' is not allowed from list 'bridge': Only stores under internal/postgres unwrap Tx and Snapshot; see decision 26. (depguard)
internal/lintfixture/nilerr.go:8:3: error is not nil (line 7) but it returns nil (nilerr)
internal/lintfixture/sqlclosecheck.go:16:25: Rows/Stmt/NamedStmt was not closed (sqlclosecheck)
internal/lintfixture/bad.go:8:36: SA4017: TrimSpace doesn't have side effects and its return value is ignored (staticcheck)
internal/lintfixture/bad_templ.go:36:3: SA4006: this value of ctx is never used (staticcheck)
internal/lintfixture/bad_templ.go:37:3: SA4017: TrimSpace doesn't have side effects and its return value is ignored (staticcheck)
7 issues:`

func TestEvaluate(t *testing.T) {
	if err := evaluate(recorded, true); err != nil {
		t.Fatalf("the recorded output: %v", err)
	}
	without := func(fragment string) string {
		var kept []string
		for _, line := range strings.Split(recorded, "\n") {
			if !strings.Contains(line, fragment) {
				kept = append(kept, line)
			}
		}
		return strings.Join(kept, "\n")
	}
	for _, tc := range []struct {
		name, out string
		failed    bool
		want      string
	}{
		{"lint passed", recorded, false, "depguard must reject pgxbridge in a module root"},
		{"no depguard report", without("badroot/root.go"), true, "depguard must reject pgxbridge in a module root"},
		{"depguard in a store", recorded + "\ninternal/lintfixture/internal/postgres/store.go:5:2: import 'x/pgxbridge' is not allowed (depguard)", true, "depguard must accept pgxbridge in a store"},
		{"no SA4017 in bad.go", without("lintfixture/bad.go:"), true, "staticcheck must reject an ignored result in bad.go"},
		{"only SA4006 in bad_templ.go", without("bad_templ.go:37"), true, "staticcheck must reject an ignored result in bad_templ.go"},
		{"no bodyclose", without("bodyclose.go"), true, "bodyclose must reject an unclosed HTTP response body"},
		{"no sqlclosecheck", without("sqlclosecheck.go"), true, "sqlclosecheck must reject unused, unclosed pgx rows"},
		{"no nilerr", without("nilerr.go"), true, "nilerr must reject returning nil after a non-nil error"},
		{"report on another file", strings.Replace(recorded, "lintfixture/nilerr.go", "lintfixture/other.go", 1), true, "nilerr must reject returning nil after a non-nil error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluate(tc.out, tc.failed)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("evaluate = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"valid", []string{"-lint", "../bin/golangci-lint", ".."}, true},
		{"equals", []string{"-lint=../bin/golangci-lint", ".."}, true},
		{"no arguments", nil, false},
		{"no binary", []string{".."}, false},
		{"empty binary", []string{"-lint=", ".."}, false},
		{"no root", []string{"-lint", "../bin/golangci-lint"}, false},
		{"two roots", []string{"-lint", "../bin/golangci-lint", "..", ".."}, false},
		{"unknown flag", []string{"-unknown", ".."}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, lint, err := parse(tc.args)
			if (err == nil) != tc.ok {
				t.Fatalf("parse error = %v, want success = %v", err, tc.ok)
			}
			if tc.ok && (root != ".." || lint != "../bin/golangci-lint") {
				t.Fatalf("parse = (%q, %q)", root, lint)
			}
		})
	}
}
