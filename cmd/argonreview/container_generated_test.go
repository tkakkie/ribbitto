package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func generatedFixture(rel, body string) string {
	return "diff -ru /work/" + rel + " /tmp/work/" + rel + "\n--- /work/" + rel + "\told\n+++ /tmp/work/" + rel + "\tnew\n" + body
}

func TestGeneratedArguments(t *testing.T) {
	// The common prefix is already independently golden-tested for all Go checks.
	want := containerArguments("copy", "name", "vet", "./...")
	want = append(want[:len(want)-4:len(want)-4], containerGeneratedScript)
	if got := containerArguments("copy", "name", "generated", "caller-text"); !reflect.DeepEqual(got, want) {
		t.Fatalf("generated arguments: %q, want %q", got, want)
	}
	const script = `set -eu
mkdir /tmp/work
cp -R /work/. /tmp/work
cd /tmp/work
/usr/local/bin/templ generate >&2 || exit 21
/usr/local/bin/sqlc generate >&2 || exit 21
/usr/local/bin/tailwindcss -i web/styles/app.css -o web/static/css/app.css --minify >&2 || exit 21
templ fmt -fail . >&2 || exit 22
names=$(gofmt -l .) || exit 24
if [ -n "$names" ]; then
 printf '%s\n' "$names"
 exit 23
fi
status=0
diff -ru /work /tmp/work || status=$?
case "$status" in
 0) exit 0 ;;
 1) exit 25 ;;
 *) exit 26 ;;
esac
`
	if containerGeneratedScript != script || strings.Contains(script, "$@") || strings.Contains(script, "./...") {
		t.Fatal("fixed generated script changed")
	}
	if generatedGenerationFailed != 21 || generatedTemplFailed != 22 || generatedGofmtNames != 23 || generatedGofmtFailed != 24 || generatedDiffer != 25 || generatedDiffFailed != 26 {
		t.Fatal("stage statuses changed")
	}
	for _, pattern := range []string{".", "./internal/web", "./internal/...", "-x"} {
		t.Run(pattern, func(t *testing.T) {
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Fatal("Docker reached")
				return 0, nil
			}}
			if _, err := runContainerCheck(context.Background(), "tree", "generated", pattern, runner, t.TempDir()); err == nil || !strings.Contains(err.Error(), "whole-tree package (./...)") {
				t.Fatalf("refusal: %v", err)
			}
		})
	}
}

func TestGeneratedVerdicts(t *testing.T) {
	diff := generatedFixture("a.go", "@@ -1 +1 @@\n-old\n+new\n")
	for _, tc := range []struct {
		name            string
		code            int
		stdout, verdict string
		runErr          error
		over, timeout   bool
	}{
		{name: "clean", verdict: "verified"},
		{name: "unexpected stdout", stdout: "surprise", verdict: "FAILED"},
		{name: "generation", code: 21, verdict: "FAILED: generation failed"},
		{name: "templ", code: 22, verdict: "FAILED: formatting"},
		{name: "gofmt names", code: 23, stdout: "a.go\nb.go\n", verdict: "FAILED: formatting"},
		{name: "gofmt error", code: 24, verdict: "FAILED"},
		{name: "differ", code: 25, stdout: diff, verdict: "FAILED: generated files differ"},
		{name: "empty diff", code: 25, verdict: "FAILED: generated files differ"},
		{name: "malformed diff", code: 25, stdout: "unknown\n", verdict: "FAILED: generated files differ"},
		{name: "diff error", code: 26, verdict: "FAILED"},
		{name: "unknown", code: 99, verdict: "FAILED"},
		{name: "runner error", runErr: errors.New("runner failed"), verdict: "FAILED"},
		{name: "timeout", timeout: true, verdict: "FAILED"},
		{name: "output cap", over: true, verdict: "NOT VERIFIED: output limit exceeded (output cut off)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.runErr
			if tc.timeout {
				err = context.DeadlineExceeded
			}
			verdict, stat, _ := generatedVerdict(tc.code, err, tc.stdout, tc.over)
			if verdict != tc.verdict {
				t.Fatalf("pure verdict %q, want %q", verdict, tc.verdict)
			}
			if tc.code == 25 && tc.stdout != diff && !strings.Contains(stat, "diff was not understood") {
				t.Fatalf("malformed stat: %q", stat)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []string
			runner := fakeContainerRunner{func(_ context.Context, args, _ []string, stdout, stderr io.Writer) (int, error) {
				calls = append(calls, args[0])
				if args[0] != "run" {
					return 0, nil
				}
				_, _ = io.WriteString(stderr, "tool diagnostic")
				_, _ = io.WriteString(stdout, tc.stdout)
				if tc.timeout {
					cancel()
					return tc.code, context.DeadlineExceeded
				}
				if tc.over {
					_, _ = io.WriteString(stdout, strings.Repeat("x", containerOutputLimit))
				}
				return tc.code, tc.runErr
			}}
			report, runErr := runContainerCheck(ctx, containerFixture(t), "generated", "./...", runner, t.TempDir())
			first, _, _ := strings.Cut(report, "\n")
			if first != tc.verdict || !reflect.DeepEqual(calls, []string{"run", "stop", "rm"}) {
				t.Fatalf("report %q, calls %v, error %v", report[:min(len(report), 500)], calls, runErr)
			}
			if !strings.Contains(report, "no write-back") || !strings.Contains(report, "--- stderr\ntool diagnostic") {
				t.Fatal("scope or stderr lost")
			}
			if tc.code == 25 && tc.stdout == diff && !strings.HasPrefix(report, tc.verdict+"\na.go +1 -1\n1 paths, +1 -1\nChecks") {
				t.Fatal("stat must precede scope and diff")
			}
			if tc.code == 24 || tc.code == 26 || tc.code == 99 {
				if !strings.Contains(report, fmt.Sprintf("could not complete (status %d)", tc.code)) {
					t.Fatal("missing status")
				}
			}
		})
	}
	// A genuinely expired parent also fails closed even if the runner claims success.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("expired call reached Docker")
		return 0, nil
	}}
	if report, err := runContainerCheck(ctx, containerFixture(t), "generated", "./...", runner, t.TempDir()); err == nil || strings.HasPrefix(report, "verified") {
		t.Fatal("expired run verified")
	}
}

func TestGeneratedDiffStat(t *testing.T) {
	modified := generatedFixture("a.go", "@@ -1,2 +1,2 @@\n same\n-old\n+new\n")
	binary := "Binary files /work/a and /tmp/work/a differ\n"
	// GNU diff quotes these names in both the command and file headers.
	const quoted = `diff -ru "/work/sp ace" "/tmp/work/sp ace"
--- "/work/sp ace"	2026-10-25 11:45:54.009625005 +0000
+++ "/tmp/work/sp ace"	2026-10-25 11:45:54.009625005 +0000
@@ -1 +1 @@
-a
+b
`
	for _, tc := range []struct{ name, data, want string }{
		{"modified", modified, "a.go +1 -1\n1 paths, +1 -1\n"},
		{"only copy", "Only in /tmp/work: new.go\n", "new.go (new in the copy)\n1 paths, +0 -0\n"},
		{"only worktree", "Only in /work/sub dir: old.go\n", "sub dir/old.go (only in the worktree)\n1 paths, +0 -0\n"},
		{"new directory", "Only in /tmp/work: newdir\n", "newdir (new in the copy)\n1 paths, +0 -0\n"},
		{"binary", binary, "a (binary)\n1 paths, +0 -0\n"},
		{"binary header", "diff -ru /work/a /tmp/work/a\n" + binary, "a (binary)\n1 paths, +0 -0\n"},
		{"space quoted", quoted, "sp ace +1 -1\n1 paths, +1 -1\n"},
		{"double quote quoted", strings.ReplaceAll(quoted, "sp ace", `qu\"ote`), "qu\"ote +1 -1\n1 paths, +1 -1\n"},
		{"tab quoted", strings.ReplaceAll(quoted, "sp ace", `tab\tx`), "tab\tx +1 -1\n1 paths, +1 -1\n"},
		{"backslash quoted", strings.ReplaceAll(quoted, "sp ace", `back\\slash`), "back\\slash +1 -1\n1 paths, +1 -1\n"},
		{"non-ASCII unquoted", generatedFixture("uni€", "@@ -1 +1 @@\n-a\n+b\n"), "uni€ +1 -1\n1 paths, +1 -1\n"},
		{"octal escape", strings.ReplaceAll(quoted, "sp ace", `oct\001l`), "oct\x01l +1 -1\n1 paths, +1 -1\n"},
		{"only copy space unquoted", "Only in /tmp/work: only sp\n", "only sp (new in the copy)\n1 paths, +0 -0\n"},
		{"binary space unquoted", "Binary files /work/b bin and /tmp/work/b bin differ\n", "b bin (binary)\n1 paths, +0 -0\n"},
		{"header-like content", generatedFixture("a", "@@ -1 +1 @@\n--- old\n+++ new\n"), "a +1 -1\n1 paths, +1 -1\n"},
		{"no newline markers", generatedFixture("a", "@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n"), "a +1 -1\n1 paths, +1 -1\n"},
		{"multiple hunks", generatedFixture("a", "@@ -1 +1 @@\n-a\n+b\n@@ -8,0 +9,2 @@\n+c\n+d\n"), "a +3 -1\n1 paths, +3 -1\n"},
		{"large css", generatedFixture("app.css", "@@ -1 +1 @@\n-"+strings.Repeat("a", 28<<10)+"\n+"+strings.Repeat("b", 28<<10)+"\n"), "app.css +1 -1\n1 paths, +1 -1\n"},
		{"empty", "", ""}, {"unknown", modified + "unknown\n", ""},
		{"count mismatch", generatedFixture("a", "@@ -1 +1 @@\n-a\n"), ""},
		{"extra content", modified + "+extra\n", ""},
		{"header without body", "diff -ru /work/a /tmp/work/a\n", ""},
		{"file headers only", generatedFixture("a", ""), ""},
		{"truncated hunk", generatedFixture("a", "@@ -1,2 +1,2 @@\n same\n"), ""},
		{"signed count", generatedFixture("a", "@@ -1,+1 +1,1 @@\n-a\n+b\n"), ""},
		{"oversized range", generatedFixture("a", "@@ -999999999999999999999999 +1 @@\n-a\n+b\n"), ""},
		{"empty count", generatedFixture("a", "@@ -1, +1 @@\n-a\n+b\n"), ""},
		{"invalid hunk", generatedFixture("a", "@@ invalid @@\n-a\n+b\n"), ""},
		{"missing newline", strings.TrimSuffix(modified, "\n"), ""},
		{"foreign root", strings.Replace(modified, "/work/", "/foreign/", 1), ""},
		{"quoted unterminated", modified + strings.Replace(quoted, `"/work/sp ace"`, `"/work/sp ace`, 1), ""},
		{"quoted bad escape", strings.ReplaceAll(quoted, "sp ace", `bad\q`), ""},
		{"quoted mismatch", strings.Replace(quoted, `"/tmp/work/sp ace"`, `"/tmp/work/other"`, 1), ""},
		{"quoted trailing junk", strings.Replace(quoted, `"/tmp/work/sp ace"`, `"/tmp/work/sp ace"junk`, 1), ""},
		{"quoted foreign root", strings.Replace(quoted, "/work/", "/foreign/", 1), ""},
		{"quoted traversal", strings.ReplaceAll(quoted, "sp ace", "../other"), ""},
		{"quoted absolute", strings.ReplaceAll(quoted, "sp ace", "/other"), ""},
		{"quoted unclean", strings.ReplaceAll(quoted, "sp ace", "sub/../other"), ""},
		{"quoted NUL", strings.ReplaceAll(quoted, "sp ace", `bad\000`), ""},
		{"quoted CR", strings.ReplaceAll(quoted, "sp ace", `bad\r`), ""},
		{"quoted LF", strings.ReplaceAll(quoted, "sp ace", `bad\n`), ""},
		{"quoted file header unterminated", strings.Replace(quoted, `--- "/work/sp ace"`, `--- "/work/sp ace`, 1), ""},
		{"quoted file header bad escape", strings.Replace(quoted, `+++ "/tmp/work/sp ace"`, `+++ "/tmp/work/bad\q"`, 1), ""},
		{"quoted file header trailing junk", strings.Replace(quoted, `--- "/work/sp ace"`, `--- "/work/sp ace"junk`, 1), ""},
		{"quoted file header mismatch", strings.Replace(quoted, `+++ "/tmp/work/sp ace"`, `+++ "/tmp/work/other"`, 1), ""},
		{"foreign file header", strings.Replace(modified, "--- /work/", "--- /other/", 1), ""},
		{"foreign only", "Only in /foreign: a\n", ""},
		{"traversal", "Only in /tmp/work/../other: a\n", ""},
		{"binary mismatch", "diff -ru /work/b /tmp/work/b\n" + binary, ""},
		{"naive prefix trap", generatedFixture("a", "@@ -1 +1 @@\n--- old\n+++ new\n+extra\n"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := generatedDiffStat(tc.data)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("stat %q, ok %v; want %q", got, ok, tc.want)
			}
			if tc.want == "" {
				verdict, stat, _ := generatedVerdict(25, nil, tc.data, false)
				if verdict != "FAILED: generated files differ" || stat != "Diff stat: diff was not understood\n" {
					t.Fatalf("malformed diff verdict %q, stat %q", verdict, stat)
				}
			}
			if tc.name == "large css" {
				_, stat, out := generatedVerdict(25, nil, tc.data, false)
				if len(stat+out) > 2000 || !strings.Contains(out, "[diff cut off]") {
					t.Fatal("long css report unbounded")
				}
			}
		})
	}
}

func TestGeneratedReportCaps(t *testing.T) {
	var diff strings.Builder
	for i := 0; i < 120; i++ {
		diff.WriteString(generatedFixture(fmt.Sprintf("%03d%s", i, strings.Repeat("é", 150)), "@@ -1 +1 @@\n-a\n+b\n"))
	}
	stat, ok := generatedDiffStat(diff.String())
	if !ok || len(strings.Split(strings.TrimSuffix(stat, "\n"), "\n")) != 102 || !strings.Contains(stat, "120 paths, +120 -120\n... 20 more paths not listed") || strings.Contains(stat, "100é") {
		t.Fatalf("capped stat: %q", stat)
	}
	if !utf8.ValidString(stat) {
		t.Fatal("path cut split a rune")
	}
	verdict, stat, out := generatedVerdict(25, nil, diff.String(), false)
	report := verdict + "\n" + stat + containerCheckScope("generated") + "\n--- stdout\n" + out + "\n--- stderr\n"
	if len(report) >= verifyResultLimit || len(out) > generatedDiffLimit+30 || !strings.Contains(out, "[diff cut off]") {
		t.Fatalf("report too large: %d, diff %d", len(report), len(out))
	}
	result, bad := verifyResult(report+strings.Repeat("stderr", verifyResultLimit), nil, false, false)
	if !bad || !strings.Contains(result, stat) || !strings.Contains(result, out) {
		t.Fatal("MCP truncation lost stat or bounded diff")
	}
	names := strings.Repeat(strings.Repeat("é", 150)+".go\n", 60)
	_, _, out = generatedVerdict(23, nil, names, false)
	if !strings.HasSuffix(out, "and 10 more") || len(out) > 50*201+30 || !utf8.ValidString(out) {
		t.Fatal("gofmt names not bounded")
	}
}
