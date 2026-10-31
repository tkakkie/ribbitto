package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
)

func TestVerifyLogExecutionConsistency(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, logs := range []bool{false, true} {
			t.Run(fmt.Sprintf("complete=%v/logs=%v", complete, logs), func(t *testing.T) {
				runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, _ io.Writer) (int, error) {
					if args[0] != "run" {
						return 0, nil
					}
					_, _ = io.WriteString(out, containerJSON("p", "start"))
					// Fill output before filling identities. The final output-bearing failure
					// sorts first so even the summary exposes both kinds of storage loss.
					for i := 0; i < aggregatorOutputLimit/aggregatorTestOutputLimit; i++ {
						name := fmt.Sprintf("TestZ%04d", i)
						if i == 0 {
							name = "TestABRetained"
						}
						_, _ = io.WriteString(out, containerJSON("p", "run/"+name))
						_, _ = io.WriteString(out, testOutput("p", name, "head\n"+strings.Repeat("x", aggregatorTestOutputLimit)+"\ntail\n"))
						_, _ = io.WriteString(out, containerJSON("p", "fail/"+name))
					}
					name := "TestAAUnretained"
					_, _ = io.WriteString(out, containerJSON("p", "run/"+name))
					_, _ = io.WriteString(out, testOutput("p", name, "lost output\n"))
					_, _ = io.WriteString(out, containerJSON("p", "fail/"+name))
					for i := 0; i < aggregatorIndexLimit; i++ {
						name := fmt.Sprintf("TestZZ%04d", i)
						_, _ = io.WriteString(out, containerJSON("p", "run/"+name, "fail/"+name))
					}
					if complete {
						_, _ = io.WriteString(out, containerJSON("p", "fail"))
					} else {
						_, _ = io.WriteString(out, "{")
					}
					return 1, nil
				}}
				v := &logExecutionVerifier{execution: containerExecution{budget: grantContainerBudget(), logs: logs}, tree: containerFixture(t), copies: t.TempDir(), runner: runner}
				var store *verifyLogStore
				if logs {
					store = &verifyLogStore{}
				}
				summary, bad := logMCPCall(t, verifyMCPServer{verifier: v, logs: store}, context.Background(), mcpCall, nil)
				r := v.execution.record
				if !bad || r == nil || r.complete != complete || r.namesNotRetained == 0 {
					t.Fatal("storage-loss fixture failed")
				}
				assertLogCapture(t, store, summary, r, logs)
				// Logs off retains nothing in the server. A temporary test-only projection
				// still compares the same record against the unwired reader.
				c := newVerifyLogCapture("FAILED", r)
				if logs {
					c = store.captures[0]
				}
				index := strings.Join(c.indexLines(), "\n")
				retained := len(c.entries)
				if retained+r.namesNotRetained != r.confirmedFailures || retained != strings.Count(v.host, "(test, fail)\n")+strings.Count(v.host, "(package, fail)\n") {
					t.Fatal("retained failure counts disagree")
				}
				if complete {
					count := fmt.Sprintf("Leaf tests: 0 passed, %d failed, 0 skipped", r.failedLeaves)
					for _, text := range []string{v.host, summary, index} {
						if !strings.Contains(text, count) || strings.Contains(text, "observed, total unknown") {
							t.Fatal("complete counts disagree")
						}
					}
					notice := fmt.Sprintf("names not retained for %d failures", r.namesNotRetained)
					if !strings.Contains(v.host, notice) || !strings.Contains(index, notice) {
						t.Fatal("complete storage omissions disagree")
					}
					list := strings.Split(strings.Split(summary, "Failing leaf tests:\n")[1], "\nand ")[0]
					shown := strings.Count(list, summaryIndent)
					omitted := consistencyNumber(t, summary, `; ([0-9]+) names not shown;`)
					if shown != summaryNameLimit || shown+omitted != r.failedLeaves {
						t.Fatal("complete summary omission accounting disagrees")
					}
				} else {
					for _, text := range []string{v.host, summary, index} {
						if !strings.Contains(text, "Failures (observed, total unknown):") || strings.Contains(text, "Leaf tests:") {
							t.Fatal("incomplete counts claimed totals")
						}
						consistencyNotRetained(t, text, r.namesNotRetained)
					}
					observed := regexp.MustCompile(`(?m)^  \| (?:#[0-9]+ )?p/.* \((?:test|package)\)$`).FindAllString(summary, -1)
					omitted := consistencyNumber(t, summary, `; ([0-9]+) names not shown;`)
					if len(observed) != summaryNameLimit || len(observed)+omitted != retained {
						t.Fatal("observed display/storage omissions overlap")
					}
				}
				found := 0
				for i, item := range c.entries {
					e := item.entry
					if e.name != "TestAAUnretained" && e.name != "TestABRetained" {
						continue
					}
					unit, _, class := c.unit(verifyLogArguments{stream: "test", name: fmt.Sprintf("#%d", i+1)})
					if class != verifyLogOK {
						t.Fatal("retained failure not served")
					}
					dropped := e.output.total - len(e.output.head) - len(e.output.tail)
					if dropped <= 0 {
						t.Fatal("fixture did not lose output")
					}
					notice := fmt.Sprintf("truncated: %d bytes not retained", dropped)
					for _, text := range []string{v.host, summary, strings.Join(unit, "\n")} {
						if !strings.Contains(text, notice) {
							t.Fatalf("missing shared truncation notice %q", notice)
						}
						if e.output.notRetained && !regexp.MustCompile(`(?m)^(?:  \| )?not retained$`).MatchString(text) {
							t.Fatal("not-retained notice missing")
						}
					}
					found++
				}
				if found != 2 {
					t.Fatal("retained unit fixture missing")
				}
			})
		}
	}
}
