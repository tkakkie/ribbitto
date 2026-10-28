package main

import (
	"errors"
	"regexp"
	"strings"
)

// Argon supplies run to go test -run; validate bytes before constructing a regexp.
func validateBrowserRun(run string) error {
	if len(run) < 1 || len(run) > 200 {
		return errors.New("browser run length out of range")
	}
	for i := range len(run) {
		c := run[i]
		if !browserAlphanumeric(c) && c != '_' && c != '=' && c != '-' && c != '/' {
			return errors.New("browser run has a forbidden byte")
		}
	}
	segments := strings.Split(run, "/")
	if len(segments) > 7 {
		return errors.New("browser run has too many segments")
	}
	for _, segment := range segments {
		if len(segment) < 1 || len(segment) > 64 {
			return errors.New("browser run segment length out of range")
		}
	}
	top := segments[0]
	if len(top) < 5 || !strings.HasPrefix(top, "Test") || top[4] >= 'a' && top[4] <= 'z' {
		return errors.New("browser run has an invalid test prefix")
	}
	if !strings.HasSuffix(top, "Browser") {
		return errors.New("browser run lacks Browser suffix")
	}
	if strings.ContainsAny(top, "=-") {
		return errors.New("browser run has an invalid top-level byte")
	}
	if run == "TestBrowser" {
		return errors.New("browser run must name a test")
	}
	for _, segment := range segments[1:] {
		if !browserAlphanumeric(segment[0]) {
			return errors.New("browser subtest must start alphanumeric")
		}
	}
	return nil
}

func browserAlphanumeric(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// browserPackageImportPath admits exactly the two packages that hold go-rod
// tests and maps each to the Package name test2json reports, so the verdict
// can refuse results from any other package.
func browserPackageImportPath(pattern string) (string, error) {
	switch pattern {
	case "./internal/web":
		return "github.com/tkakkie/ribbitto/internal/web", nil
	case "./cmd/ribbitto":
		return "github.com/tkakkie/ribbitto/cmd/ribbitto", nil
	default:
		return "", errors.New("browser package is not allowed")
	}
}

// browserRunPattern anchors each segment so go test -run matches exactly the
// named test and its subtests, never a name that merely starts the same way.
func browserRunPattern(run string) (string, error) {
	if err := validateBrowserRun(run); err != nil {
		return "", err
	}
	segments := strings.Split(run, "/")
	for i, segment := range segments {
		// A future alphabet expansion must not silently change regexp semantics.
		if regexp.QuoteMeta(segment) != segment {
			return "", errors.New("browser run needs regexp escaping")
		}
		segments[i] = "^" + segment + "$"
	}
	return strings.Join(segments, "/"), nil
}
