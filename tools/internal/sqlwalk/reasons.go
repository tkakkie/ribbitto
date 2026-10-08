package sqlwalk

import (
	"fmt"
	"regexp"
	"strings"
)

// ReasonRules preserves each gate's existing marker and provenance syntax.
// Scope reasons require a colon on the pending marker; read exemptions also
// reject bare markers and accept GitHub review-comment provenance.
type ReasonRules struct{ PendingColon, ReviewComments bool }

// Check rejects empty reasons, pending approvals and untraceable maintainer claims.
// Provenance records where a decision is described, not verification of approval.
func (rules ReasonRules) Check(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("empty reason")
	}
	marker := `(?i)pending[^a-z0-9]+maintainer(?:[^a-z0-9]|$)`
	if rules.PendingColon {
		marker = `(?i)pending[^a-z0-9]+maintainer\s*:`
	}
	if regexp.MustCompile(marker).MatchString(reason) {
		return fmt.Errorf("pending maintainer approval")
	}
	comment := `issuecomment-`
	if rules.ReviewComments {
		comment = `(?:discussion_r|issuecomment-)`
	}
	provenance := regexp.MustCompile(`(?i)(#[1-9][0-9]*\b|https://github\.com/[^/\s]+/[^/\s]+/pull/[1-9][0-9]*#` + comment + `[1-9][0-9]*\b|\bdecision\s+[1-9][0-9]*\b)`)
	if strings.Contains(strings.ToLower(reason), "maintainer") && !provenance.MatchString(reason) {
		return fmt.Errorf("missing maintainer provenance")
	}
	return nil
}
