package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const implementConfigUsage = "implement-config --mode off|on --binary <abs path> --guard-dir <abs path> [--trusted-source <abs path> --worktree <abs path>] --out <dir>"

type implementConfig struct {
	verify                                  bool
	binary, guardDir, source, worktree, out string
}

func parseImplementConfig(args []string) (implementConfig, error) {
	var c implementConfig
	values := map[string]string{"--mode": "", "--binary": "", "--guard-dir": "", "--trusted-source": "", "--worktree": "", "--out": ""}
	seen := map[string]bool{}
	for i := 0; i < len(args); i += 2 {
		flag := args[i]
		if _, ok := values[flag]; !ok {
			return c, errors.New("unknown flag or positional argument")
		}
		if seen[flag] {
			return c, errors.New("repeated flag")
		}
		if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
			return c, errors.New("flag requires a nonempty value")
		}
		seen[flag], values[flag] = true, args[i+1]
	}
	if values["--mode"] != "off" && values["--mode"] != "on" {
		return c, errors.New("mode must be off or on")
	}
	c = implementConfig{values["--mode"] == "on", values["--binary"], values["--guard-dir"], values["--trusted-source"], values["--worktree"], values["--out"]}
	if !c.verify && (seen["--trusted-source"] || seen["--worktree"]) {
		return c, errors.New("off mode forbids trusted-source and worktree")
	}
	for _, flag := range []string{"--binary", "--guard-dir", "--out", "--trusted-source", "--worktree"} {
		if !c.verify && (flag == "--trusted-source" || flag == "--worktree") {
			continue
		}
		path := values[flag]
		// Hooks use sh -c; MCP arguments are trusted control code. Never let
		// quoting or invisible characters change their interpretation.
		if !utf8.ValidString(path) || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsRune(path, '\'') || strings.ContainsFunc(path, unicode.IsControl) {
			return c, fmt.Errorf("%s requires an absolute, clean, safely quotable non-root path", flag)
		}
	}
	return c, nil
}

func (c implementConfig) validate() error {
	info, err := os.Lstat(c.binary)
	if err != nil {
		return fmt.Errorf("examining binary: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return errors.New("binary must be a regular executable, not a symlink")
	}
	real, err := filepath.EvalSymlinks(c.binary)
	if err != nil {
		return fmt.Errorf("resolving binary: %w", err)
	}
	if real != c.binary {
		return errors.New("binary contains a symlink")
	}
	for _, dir := range []string{c.guardDir, filepath.Dir(c.out), c.source, c.worktree} {
		if dir != "" {
			if err := verifyRealDirectory(dir); err != nil {
				return fmt.Errorf("validating directory: %w", err)
			}
		}
	}
	if c.verify {
		for _, pair := range [][2]string{{c.source, c.worktree}, {c.source, filepath.Dir(c.binary)}, {c.worktree, filepath.Dir(c.binary)}, {c.guardDir, c.source}, {c.guardDir, c.worktree}} {
			if err := verifyDisjointDirectories(pair[0], pair[1]); err != nil {
				return fmt.Errorf("validating directory separation: %w", err)
			}
		}
	}
	if err := implementConfigEmptyOut(c.out); err != nil {
		return err
	}
	for _, root := range []string{c.guardDir, c.worktree, c.source, filepath.Dir(c.binary)} {
		if root == "" {
			continue
		}
		if err := implementConfigOutside(c.out, root); err != nil {
			return err
		}
	}
	return nil
}

func implementConfigEmptyOut(out string) error {
	info, err := os.Lstat(out)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("examining output: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("output must be a real empty directory")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		return fmt.Errorf("reading output directory: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("output directory is not empty")
	}
	return nil
}

func implementConfigOutside(out, root string) error {
	if out == root || strings.HasPrefix(out, root+string(filepath.Separator)) {
		return errors.New("output lies inside a protected directory")
	}
	other, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("examining protected directory: %w", err)
	}
	// The output may not exist. Its existing ancestors still catch case aliases.
	for current := out; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("examining output ancestor: %w", err)
		}
		if err == nil && os.SameFile(info, other) {
			return errors.New("output aliases a protected directory")
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func implementConfigFiles(verify bool, binary, guardDir, source, worktree string) (hooks, settings, mcp []byte, err error) {
	hooks, err = implementerHooks(verify, binary, guardDir)
	if err != nil {
		return nil, nil, nil, err
	}
	mcp, err = implementerMCPConfig(verify, binary, source, worktree)
	return hooks, implementerSettings(verify), mcp, err
}

type implementConfigIO struct {
	write  func(string, []byte) error
	read   func(string) ([]byte, error)
	rename func(string, string) error
}

func writeImplementConfigFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func writeImplementConfig(c implementConfig, fs implementConfigIO) (string, error) {
	hooks, settings, mcp, err := implementConfigFiles(c.verify, c.binary, c.guardDir, c.source, c.worktree)
	if err != nil {
		return "", err
	}
	// A sibling keeps rename on one filesystem; nothing partial becomes out.
	temp, err := os.MkdirTemp(filepath.Dir(c.out), ".implement-config-")
	if err != nil {
		return "", fmt.Errorf("creating staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := os.Chmod(temp, 0700); err != nil {
		return "", fmt.Errorf("setting staging mode: %w", err)
	}
	names := []string{"hooks.json", "settings.json", "mcp_config.json"}
	data := [][]byte{hooks, settings, mcp}
	for i, name := range names {
		if err := fs.write(filepath.Join(temp, name), data[i]); err != nil {
			return "", fmt.Errorf("writing %s: %w", name, err)
		}
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		return "", fmt.Errorf("reading staging directory: %w", err)
	}
	if len(entries) != 3 {
		return "", errors.New("staging directory must contain exactly three files")
	}
	var report strings.Builder
	for i, name := range names {
		path := filepath.Join(temp, name)
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("examining %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode() != 0600 {
			return "", errors.New("configuration must be regular files with mode 0600")
		}
		data[i], err = fs.read(path)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", name, err)
		}
		_, _ = fmt.Fprintf(&report, "%x  %s\n", sha256.Sum256(data[i]), name)
	}
	if err := checkImplementerConfig(c.verify, data[0], data[1], data[2], c.binary, c.source, c.worktree, c.guardDir); err != nil {
		return "", fmt.Errorf("checking written configuration: %w", err)
	}
	if err := implementConfigEmptyOut(c.out); err != nil {
		return "", fmt.Errorf("rechecking output: %w", err)
	}
	if err := os.Remove(c.out); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("removing empty output directory: %w", err)
	}
	if err := fs.rename(temp, c.out); err != nil {
		return "", fmt.Errorf("publishing configuration: %w", err)
	}
	mode := "off"
	if c.verify {
		mode = "on"
	}
	_, _ = fmt.Fprintf(&report, "implement-config: wrote mode=%s files=3 out=%s\n", mode, c.out)
	return report.String(), nil
}

func implementConfigCommand(args []string, stdout, stderr io.Writer) int {
	return implementConfigCommandWithIO(args, stdout, stderr, implementConfigIO{writeImplementConfigFile, os.ReadFile, os.Rename})
}

func implementConfigCommandWithIO(args []string, stdout, stderr io.Writer, fs implementConfigIO) int {
	c, err := parseImplementConfig(args)
	if err == nil {
		err = c.validate()
	}
	code := 2
	var report string
	if err == nil {
		code = 1
		report, err = writeImplementConfig(c, fs)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview implement-config: %s\n", strings.Join(strings.Fields(err.Error()), " "))
		return code
	}
	_, _ = io.WriteString(stdout, report)
	return 0
}
