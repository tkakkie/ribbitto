package main

import (
	"errors"
	"fmt"
	"runtime/debug"
)

// trusted returns the revision the binary was built from when that is
// origin/main and the build had no local changes. It only catches an
// accidental local or stale build: code that removes this check is not
// stopped by it, so the trusted invocation (building from a clean origin/main
// checkout) remains the boundary.
func trusted(info *debug.BuildInfo, ok bool, resolve func() (string, error)) (string, error) {
	if !ok || info == nil {
		return "", errors.New("no build information; build with go build from a clean origin/main checkout")
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	revision := settings["vcs.revision"]
	if revision == "" {
		return "", errors.New("no VCS revision in the build; build with go build from a clean origin/main checkout")
	}
	// A missing flag is not a clean build: go run and -buildvcs=false omit it.
	if settings["vcs.modified"] != "false" {
		return "", fmt.Errorf("built from a modified or unstamped tree (vcs.modified=%q)", settings["vcs.modified"])
	}
	want, err := resolve()
	if err != nil {
		return "", fmt.Errorf("resolving origin/main: %w", err)
	}
	if want == "" || revision != want {
		return "", fmt.Errorf("built from %s, but origin/main is %q", revision, want)
	}
	return revision, nil
}
