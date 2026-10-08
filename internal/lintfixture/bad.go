//go:build lintfixture

package lintfixture

import "strings"

// IgnoredResult must fail staticcheck: this call has no side effects.
func IgnoredResult(value string) { strings.TrimSpace(value) }
