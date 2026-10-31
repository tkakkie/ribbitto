package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

func implementExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding executable: %w", err)
	}
	return filepath.EvalSymlinks(path)
}

func implementFeatureArguments(state, source string) error {
	if state != "off" && state != "on" {
		return errors.New("--verify must be explicitly off or on")
	}
	if state == "on" && (source == "" || !filepath.IsAbs(source) || filepath.Clean(source) != source) {
		return errors.New("--verify on requires --trusted-source with an absolute clean path")
	}
	if state == "off" && source != "" {
		return errors.New("--trusted-source requires --verify on")
	}
	return nil
}

func implementFeatureSource(home, tree, source string, trust func(string) error) error {
	if err := verifyRealDirectory(source); err != nil {
		return fmt.Errorf("validating trusted source: %w", err)
	}
	for _, other := range []string{home, tree} {
		if err := verifyDisjointDirectories(source, other); err != nil {
			return fmt.Errorf("separating trusted source: %w", err)
		}
	}
	return trust(source)
}

func implementTrustedSource(source string) error {
	info, ok := debug.ReadBuildInfo()
	// Use the same fenced Git reader as the home/baseline check. Source
	// configuration must not run hooks, filters or fetch missing objects.
	read := func(dir string, args ...string) (string, error) {
		data, code, err := implementGit(dir)(args...)
		if err != nil {
			return "", fmt.Errorf("reading trusted source: %w", err)
		}
		if code != 0 {
			return "", fmt.Errorf("reading trusted source: git exit %d", code)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return implementSourceRevision(source, info, ok, read)
}

func implementSourceRevision(source string, info *debug.BuildInfo, ok bool, read func(string, ...string) (string, error)) error {
	if err := checkTrustedSource(source, info, ok, read); err != nil {
		return err
	}
	revision, err := trusted(info, ok, func() (string, error) { return read(source, "rev-parse", "--verify", "HEAD^{commit}") })
	if err != nil {
		return fmt.Errorf("checking trusted source HEAD: %w", err)
	}
	status, err := read(source, "status", "--porcelain", "--ignored", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("checking trusted source status at %s: %w", revision, err)
	}
	if status != "" {
		return errors.New("trusted source is not clean (including ignored and untracked files)")
	}
	return nil
}

// implementBrowserArgument keeps browser configuration absent in feature-off homes.
func implementBrowserArgument(state, path string, set bool) error {
	if !set {
		return nil
	}
	if state != "on" {
		return errors.New("--browser-mode requires --verify on")
	}
	return browserModePath(path)
}

// implementLogsArgument keeps log permissions absent in feature-off homes.
func implementLogsArgument(state, value string, set bool) error {
	if !set {
		return nil
	}
	if state != "on" {
		return errors.New("--logs requires --verify on")
	}
	if value != "on" && value != "off" {
		return errors.New("--logs must be off or on")
	}
	return nil
}
