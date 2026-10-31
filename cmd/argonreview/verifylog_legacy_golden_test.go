package main

import (
	"context"
	"strings"
	"testing"
)

func TestVerifyLogMCPOffGolden(t *testing.T) {
	// These bytes were obtained by running the fixtures with a Go overlay of
	// the unchanged base 2ba8c24 production files, not the new projection.
	const metadata = "Check: test ./...\nScope: Race detector was not run. Cgo-dependent packages and database/browser tests are CI-only. Ran on regenerated sources (templ, sqlc, Tailwind); the generated check covers whether the committed generated files are current.\nTrust: stdout cannot be authenticated within one uid and one container. Structural rules check consistency, not proof. verified is Argon's own check, never a security control; CI, review and handoff checks remain the check.\nPackages: 1 passed, 0 failed, 0 skipped\n"
	const footer = "Omitted: excerpts shown for 0 of 0 failing tests; 0 names not shown; 0 lines not shown; 0 lines shortened.\n"
	for _, tc := range []struct{ state, name, want string }{
		{"fail", "Testghp_" + strings.Repeat("a", 40), "FAILED\n" + metadata + "Leaf tests: 0 passed, 1 failed, 0 skipped\nFailing leaf tests:\n  | [name withheld]\nOmitted: excerpts shown for 0 of 1 failing tests; 0 names not shown; 0 lines not shown; 1 lines shortened.\n"},
		{"skip", "TestOne", "NOT VERIFIED: 1 skipped\n" + metadata + "Leaf tests: 0 passed, 0 failed, 1 skipped\nSkipped names:\n  | p/TestOne\n" + footer},
		{"pass", "TestOne", "verified\n" + metadata + "Leaf tests: 1 passed, 0 failed, 0 skipped\n" + footer},
	} {
		t.Run(tc.state, func(t *testing.T) {
			v := containerVerifier{worktree: containerFixture(t), runner: summaryRunner(containerEvents("p", tc.name, tc.state), "", 0), copyParent: t.TempDir(), budget: grantContainerBudget()}
			text, bad := logMCPCall(t, verifyMCPServer{verifier: v}, context.Background(), mcpCall, nil)
			if text != tc.want || bad != (tc.state != "pass") {
				t.Fatalf("logs-off bytes differ from base: %q", text)
			}
		})
	}
}
