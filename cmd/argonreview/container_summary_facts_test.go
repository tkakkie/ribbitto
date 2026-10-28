package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestSummaryFactsProtocol ensures stopped interpretation never makes malformed protocol
// usable as complete evidence, including duplicates and incomplete states.
func TestSummaryFactsProtocol(t *testing.T) {
	pass := containerEvents("p", "TestOne", "pass")
	for _, data := range []string{
		"", "\n", "null\n", "{}\n", "[]\n", pass + "garbage\n", strings.TrimSuffix(pass, "\n"),
		`{"Action":"fail","ACTION":"pass","Package":"p"}` + "\n",
		containerJSON("p", "pass"), pass + containerJSON("p", "pass"),
		containerJSON("p", "start", "run/Test", "pass/Test", "pass/Test", "pass"),
		containerJSON("p", "start", "run/Test", "run/Test", "pass"),
		containerJSON("p", "start", "pause/Test", "pass"), containerJSON("p", "start", "mystery", "pass"),
		containerJSON("p", "start", "run/Test", "pass"),
		strings.Repeat("x", aggregatorLineLimit+1) + "\n",
	} {
		f := recordFactsFixture("test", []byte(data), nil, false)
		if f.err == nil {
			t.Fatalf("understood malformed %q", data[:min(len(data), 200)])
		}
		if f.afterEvent < 0 || f.afterEvent > f.events {
			t.Fatal("invalid error boundary")
		}
	}
	broken := "bad\n" + containerJSON("p", "start", "run/TestBad") + testOutput("p", "TestBad", "panic: later diagnostic\n") + containerJSON("p", "fail/TestBad", "fail")
	f := recordFactsFixture("test", []byte(broken), nil, false)
	if f.err == nil || f.afterEvent != 0 || f.events != 0 || len(f.failures) != 0 || f.panicLine != "" {
		t.Fatalf("events after first stop were interpreted: %+v", f)
	}
	// A failure before the bad line remains observed; later text cannot supply evidence.
	f = recordFactsFixture("test", []byte(containerEvents("p", "TestBad", "fail")+"bad\n"), nil, false)
	if f.err == nil || len(f.failures) != 1 || !f.incomplete {
		t.Fatalf("observed failure lost: %+v", f)
	}
}

// TestSummaryFactsLeafLogic covers every slash ancestor, including an implicit
// ancestor, and proves failed parents never appear as failing leaf names.
func TestSummaryFactsLeafLogic(t *testing.T) {
	data := containerJSON("p", "start", "run/TestParent", "run/TestParent/child/a", "pass/TestParent/child/a", "fail/TestParent", "run/TestSkip", "skip/TestSkip", "run/TestBad", "fail/TestBad", "fail")
	f := recordFactsFixture("test", []byte(data), nil, false)
	if f.err != nil || f.leaves != (summaryCounts{passed: 1, failed: 1, skipped: 1}) || len(f.failures) != 1 || f.failures[0].name != "p/TestBad" {
		t.Fatalf("leaf facts %+v", f)
	}
}

// TestSummaryFactsFlood has many events but few identities, then exceeds the
// identity cap. Neither path retains output proportional to the event count.
func TestSummaryFactsFlood(t *testing.T) {
	data := containerJSON("p", "start", "run/TestOne") + strings.Repeat(testOutput("p", "TestOne", "noise\n"), 100000) + containerJSON("p", "pass/TestOne", "pass")
	f := recordFactsFixture("test", []byte(data), nil, false)
	if f.err != nil || f.leaves.passed != 1 || len(f.failures) != 0 || len(f.stdout.lines) > 8 {
		t.Fatalf("flood facts %+v", f)
	}
	var stream strings.Builder
	stream.WriteString(containerJSON("p", "start"))
	for i := 0; i < aggregatorTestLimit+1; i++ {
		name := fmt.Sprintf("Test%05d", i)
		stream.WriteString(containerJSON("p", "run/"+name, "pass/"+name))
	}
	stream.WriteString(containerJSON("p", "pass"))
	f = recordFactsFixture("test", []byte(stream.String()), nil, false)
	if f.err == nil || f.leaves.passed != aggregatorTestLimit {
		t.Fatal("retention cap not enforced")
	}
}

// TestSummaryExistingVerifiedFixtures reads the existing table without editing
// its verdict tests. Any added verified case must be understood by this check,
// so fixture drift cannot leave the no-honest-downgrade claim silently stale.
func TestSummaryExistingVerifiedFixtures(t *testing.T) {
	source, err := parser.ParseFile(token.NewFileSet(), "container_verdict_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	ast.Inspect(source, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var verdict string
		var data ast.Expr
		for _, element := range literal.Elts {
			field, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			name, ok := field.Key.(*ast.Ident)
			if !ok {
				continue
			}
			if name.Name == "want" {
				if text, ok := field.Value.(*ast.BasicLit); ok {
					verdict, _ = strconv.Unquote(text.Value)
				}
			}
			if name.Name == "data" {
				data = field.Value
			}
		}
		if !strings.HasPrefix(verdict, "verified\n") {
			return true
		}
		count++
		stream := summaryFixtureExpression(t, data)
		actual := testVerdict(0, nil, []byte(stream), false)
		if actual != verdict {
			t.Fatalf("fixture interpretation %q != %q", actual, verdict)
		}
		in := summaryFixture("test", verdict, stream, "")
		got := buildContainerSummary(in)
		assertSummary(t, in, got)
		if !strings.HasPrefix(got, "verified\n") {
			t.Fatal(got)
		}
		return true
	})
	if count == 0 {
		t.Fatal("no verified fixtures inspected")
	}
	// container_testdb_test.go tests the script, while its verified result fixture
	// lives in container_allowlist_test.go. Exercise every real allowlist entry,
	// including output split across events, with that file's measurementStream.
	for _, entry := range measurementSkips() {
		for _, split := range []bool{false, true} {
			stream := measurementStream(entry, entry.message, true)
			if split {
				stream = containerJSON(entry.pkg, "start", "run/"+entry.test) + testOutput(entry.pkg, entry.test, entry.message[:5]) + testOutput(entry.pkg, entry.test, entry.message[5:]) + containerJSON(entry.pkg, "skip/"+entry.test, "run/TestSibling", "pass/TestSibling", "pass")
			}
			verdict := testVerdictWithAllowlist(0, nil, []byte(stream), false, measurementSkips())
			in := summaryFixture("test-db", verdict, stream, "")
			got := buildContainerSummary(in)
			assertSummary(t, in, got)
			if !strings.HasPrefix(verdict, "verified\n") || !strings.HasPrefix(got, "verified\n") {
				t.Fatal(got)
			}
		}
	}
}

// summaryFixtureExpression supports only constructors actually used by the
// existing verified table, refusing new syntax until its evidence is covered.
func summaryFixtureExpression(t *testing.T, expr ast.Expr) string {
	t.Helper()
	switch expr := expr.(type) {
	case *ast.BasicLit:
		value, err := strconv.Unquote(expr.Value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	case *ast.Ident:
		if expr.Name == "pass" {
			return containerEvents("p", "TestOne", "pass")
		}
	case *ast.BinaryExpr:
		if expr.Op == token.ADD {
			return summaryFixtureExpression(t, expr.X) + summaryFixtureExpression(t, expr.Y)
		}
	case *ast.CallExpr:
		name, ok := expr.Fun.(*ast.Ident)
		if !ok {
			break
		}
		var args []string
		for _, arg := range expr.Args {
			args = append(args, summaryFixtureExpression(t, arg))
		}
		switch name.Name {
		case "containerEvents":
			return containerEvents(args[0], args[1], args[2])
		case "containerJSON":
			return containerJSON(args[0], args[1:]...)
		}
	}
	t.Fatalf("uncovered verified fixture expression %T", expr)
	return ""
}

// TestSummaryStderrSignals finds late stderr panics without retaining all
// preceding server noise, while keeping stdout first when both contain one.
func TestSummaryStderrSignals(t *testing.T) {
	stderr := strings.Repeat("server noise\n", 100) + "panic: test timed out after 10m\n"
	f := recordFactsFixture("test", []byte(containerJSON("p", "start", "fail")), []byte(stderr), false)
	if f.err != nil || f.panicLine != "panic: test timed out after 10m" || f.timeoutLine != f.panicLine || len(f.stderr.lines) != 8 {
		t.Fatalf("stderr signals %+v", f)
	}
}
