package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	errImplementRealPath  = errors.New("path is not a clean real path")
	errImplementDirectory = errors.New("path is not a directory")
)

// implementRealDirectory preserves the implementer guard's path contract.
func implementRealDirectory(path string, lstat func(string) (fs.FileInfo, error)) error {
	resolved, err := filepath.EvalSymlinks(path)
	if !filepath.IsAbs(path) || err != nil || resolved != path || filepath.Clean(path) != path {
		return errImplementRealPath
	}
	info, err := lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errImplementDirectory
	}
	return nil
}

// implementRead is the only reader for compared and hashed files. Opening the
// parent component by component also refuses symlinks above the leaf.
func implementRead(path string, limit int64, shared bool) ([]byte, error) {
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC
	var fd int
	var err error
	if shared {
		// The account owns shared settings: follow its symlinks, as openShared and
		// checks.zsh do. Non-blocking and regular-only still apply to the descriptor.
		fd, err = unix.Open(path, flags, 0)
	} else {
		var parent int
		parent, err = walk(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		defer closeFD(parent)
		fd, err = unix.Openat(parent, filepath.Base(path), flags|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (type %s)", info.Mode().Type())
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

// Compare identities while walking up, rather than trusting string prefixes on
// a case-insensitive filesystem. A stat error never means "not nested".
func implementContains(parent, child string, lstat func(string) (fs.FileInfo, error)) (bool, error) {
	info, err := lstat(parent)
	if err != nil {
		return false, err
	}
	for {
		other, err := lstat(child)
		if err != nil {
			return false, err
		}
		if os.SameFile(info, other) {
			return true, nil
		}
		next := filepath.Dir(child)
		if next == child {
			return false, nil
		}
		child = next
	}
}

// The table is relative to home, slash separated and exact-case. A trailing
// slash denotes descendants only. These exempt NAMES, never symlinks.
func implementHomeExempt(rel string) bool {
	for _, exempt := range []string{
		".gemini", ".gemini/config/hooks.json", ".gemini/config/mcp_config.json",
		".gemini/config/agents", ".gemini/config/agents/argon-implementer.md",
		".gemini/antigravity-cli/builtin/", "implementer/", "guard/",
	} {
		if strings.HasSuffix(exempt, "/") {
			if strings.HasPrefix(rel, exempt) {
				return true
			}
		} else if rel == exempt {
			return true
		}
	}
	return false
}

func implementBaselineExempt(rel string) bool {
	switch rel {
	case ".git", "AGENTS.md", "CLAUDE.md", ".gitattributes", ".gitmodules", ".github":
		return true
	}
	return strings.HasPrefix(rel, ".github/")
}

func (c *implementChecker) scan(root string, home bool) {
	names := make(map[string]bool)
	for _, name := range implementRefusedNames {
		names[name] = true
	}
	if home {
		for _, name := range refusedCustomisation {
			names[name] = true
		}
		names["mcp_config.json"] = true
	}
	var visit func(string)
	visit = func(dir string) {
		entries, err := c.deps.readDir(dir)
		if err != nil {
			c.problem("listing %q: %q", dir, err)
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			info, err := c.deps.lstat(path)
			if err != nil {
				c.problem("checking %q: %q", path, err)
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				c.problem("relative path for %q: %q", path, err)
				continue
			}
			rel = filepath.ToSlash(rel)
			kind := "worktree"
			exempt := implementBaselineExempt(rel)
			if home {
				kind, exempt = "home", implementHomeExempt(rel)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := c.deps.readlink(path)
				if err != nil {
					c.problem("reading link %q: %q", path, err)
				} else if !home || !c.cliLog(rel, root, target) {
					c.problem("symlink in %s: %q -> %q", kind, path, target)
				}
			}
			refused := names[strings.ToLower(entry.Name())] && !exempt
			if refused {
				c.problem("refused name in %s: %q", kind, path)
			}
			if info.IsDir() && !refused && (home || rel != ".git") {
				visit(path)
			}
		}
	}
	visit(root)
}

func (c *implementChecker) cliLog(rel, root, target string) bool {
	if rel != ".gemini/antigravity-cli/cli.log" {
		return false
	}
	if !implementMatch(`^log/cli-[0-9]{8}_[0-9]{6}\.log$`, target) {
		return false
	}
	path := filepath.Join(root, ".gemini/antigravity-cli", target)
	info, err := c.deps.lstat(path)
	if err != nil {
		c.problem("checking cli.log target %q: %q", path, err)
		return false
	}
	return info.Mode().IsRegular()
}
