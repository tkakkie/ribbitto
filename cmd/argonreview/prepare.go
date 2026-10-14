package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The deny rules #1041 showed Antigravity honours. unsandboxed(*) is left out
// because Antigravity ignores it as a deny rule; commands are denied anyway.
const settingsJSON = `{
  "permissions": {
    "deny": [
      "write_file(*)",
      "command(*)",
      "read_url(*)",
      "execute_url(*)",
      "mcp(*)"
    ]
  }
}
`

// Customisation that could add tools, hooks or instructions: global and
// plugin hooks, skills, rules, agents and workflows (#1069). Its presence
// stops the run instead of being deleted, so nothing put there is lost.
var refusedCustomisation = []string{"config.json", "skills.json", "rules.json", "agents.json", "plugins.json", "plugins", "skills", "rules", "agents", "workflows"}

// prepare writes the three files the launcher owns in the dedicated HOME and
// checks them, refusing to run on anything else (#1069). The sign-in, logs,
// caches and Antigravity's other state are neither written nor removed.
// realHome is the account's home, whose .gemini other projects share.
//
// Every directory below HOME is opened without following symlinks, and files
// are created and renamed relative to those descriptors. A directory or file
// swapped for a symlink during the run is therefore refused or replaced, never
// followed.
func prepare(home, realHome, guardPath string) (string, error) {
	h, err := dedicatedHome(home, realHome)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(guardPath, "'\n") || !filepath.IsAbs(guardPath) {
		return "", fmt.Errorf("guard path %q cannot be quoted for the hook", guardPath)
	}
	root, err := unix.Open(h, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", h, err)
	}
	defer func() { _ = unix.Close(root) }()
	gemini, err := ownedDir(root, ".gemini")
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(gemini) }()
	dirs := map[string]int{}
	for _, name := range []string{"antigravity-cli", "config"} {
		fd, err := ownedDir(gemini, name)
		if err != nil {
			return "", err
		}
		defer func() { _ = unix.Close(fd) }()
		dirs[name] = fd
	}
	refused := map[string][]string{
		".gemini/config":          refusedCustomisation,
		".gemini/antigravity-cli": append([]string{"hooks.json", "mcp_config.json"}, refusedCustomisation...),
	}
	for dir, names := range refused {
		for _, name := range names {
			if exists(dirs[filepath.Base(dir)], name) {
				return "", fmt.Errorf("refusing %s: customisation could add tools, hooks or instructions", dir+"/"+name)
			}
		}
	}
	if exists(root, ".agents") {
		return "", errors.New("refusing .agents in RIBBITTO_ARGON_HOME: customisation could add tools, hooks or instructions")
	}
	files := owned(guardPath)
	for _, f := range files {
		if err := writeOwned(dirs[f.dir], f.name, f.content); err != nil {
			return "", fmt.Errorf("writing .gemini/%s/%s: %w", f.dir, f.name, err)
		}
	}
	for _, f := range files {
		if err := checkOwned(dirs[f.dir], f.name, f.content); err != nil {
			return "", fmt.Errorf("refusing .gemini/%s/%s: %w", f.dir, f.name, err)
		}
	}
	return h, nil
}

type ownedFile struct {
	dir, name string
	content   []byte
}

func owned(guardPath string) []ownedFile {
	hooks := `{
  "ribbitto-guard": {
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"command": "'` + guardPath + `' guard", "timeout": 10}]}
    ]
  }
}
`
	return []ownedFile{
		{"antigravity-cli", "settings.json", []byte(settingsJSON)},
		{"config", "hooks.json", []byte(hooks)},
		// Antigravity creates this file empty: no MCP servers.
		{"config", "mcp_config.json", []byte{}},
	}
}

// dedicatedHome resolves home through symlinks and refuses the account home, its
// ancestors, and anything overlapping its .gemini. A directory elsewhere under
// the account home is allowed.
func dedicatedHome(home, realHome string) (string, error) {
	if home == "" || !filepath.IsAbs(home) {
		return "", errors.New("RIBBITTO_ARGON_HOME must be the absolute path of a dedicated directory")
	}
	h, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolving RIBBITTO_ARGON_HOME: %w", err)
	}
	account, err := filepath.EvalSymlinks(realHome)
	if err != nil {
		return "", fmt.Errorf("resolving the account home: %w", err)
	}
	shared := filepath.Join(account, ".gemini")
	if within(account, h) || within(h, shared) {
		return "", fmt.Errorf("RIBBITTO_ARGON_HOME %s is the account home, an ancestor of it, or overlaps %s", h, shared)
	}
	return h, nil
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// ownedDir creates name under parent if it is missing and opens it, refusing
// a symlink or anything that is not a directory.
func ownedDir(parent int, name string) (int, error) {
	if err := unix.Mkdirat(parent, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("creating %s: %w", name, err)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("refusing %s: not a plain directory (%w)", name, err)
	}
	return fd, nil
}

func exists(dir int, name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
}

// writeOwned refuses a non-regular file at name, then writes an exclusive
// temporary file in the same directory and renames it over name, so a
// symlink put at name is replaced rather than followed.
func writeOwned(dir int, name string, content []byte) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && st.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("not a regular file")
	}
	tmp := fmt.Sprintf(".argonreview-%d", os.Getpid())
	fd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, werr := f.Write(content)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = unix.Unlinkat(dir, tmp, 0)
		return err
	}
	if err := unix.Renameat(dir, tmp, dir, name); err != nil {
		_ = unix.Unlinkat(dir, tmp, 0)
		return err
	}
	return nil
}

// checkOwned refuses an owned file that is a symlink, not regular, or differs
// from what the launcher writes: an extra grant, a trusted workspace, another
// hook or an MCP server all differ.
func checkOwned(dir int, name string, want []byte) error {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("not a regular file (%w)", err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	got, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("it differs from the launcher's configuration")
	}
	return nil
}
