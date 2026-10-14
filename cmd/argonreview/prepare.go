package main

import (
	"bytes"
	"encoding/json"
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

// The directories the launcher owns under HOME/.gemini.
var ownedDirs = []string{"antigravity-cli", "config"}

type ownedFile struct {
	dir, name string
	content   []byte
}

func owned(guardPath string) ([]ownedFile, error) {
	// The command runs through sh -c: the path is single-quoted, so a quote or
	// a newline in it is refused. encoding/json escapes the rest (Codex on
	// #1075).
	if strings.ContainsAny(guardPath, "'\n") || !filepath.IsAbs(guardPath) {
		return nil, fmt.Errorf("guard path %q cannot be quoted for the hook", guardPath)
	}
	type hook struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Matcher string `json:"matcher"`
		Hooks   []hook `json:"hooks"`
	}
	hooks, err := json.MarshalIndent(map[string]map[string][]matcher{
		"ribbitto-guard": {"PreToolUse": {{"*", []hook{{"'" + guardPath + "' guard", 10}}}}},
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return []ownedFile{
		{"antigravity-cli", "settings.json", []byte(settingsJSON)},
		{"config", "hooks.json", append(hooks, '\n')},
		// Antigravity creates this file empty: no MCP servers.
		{"config", "mcp_config.json", []byte{}},
	}, nil
}

// prepare writes the three files the launcher owns in the dedicated HOME and
// checks them, refusing to run on anything else (#1069). The sign-in, logs,
// caches and Antigravity's other state are neither written nor removed.
// accountHome is the account's home, whose .gemini other projects share.
func prepare(home, accountHome, guardPath string) (string, error) {
	return prepareWith(home, accountHome, guardPath, hooks{})
}

// hooks run just before the first write, after each directory prepare
// creates (before it is opened), and just after the last write; a test swaps
// directories or files there.
type hooks struct {
	beforeWrite, afterWrite func()
	afterMkdir              func(name string)
}

func (h hooks) run(f func()) {
	if f != nil {
		f()
	}
}

// prepareWith is prepare with test hooks.
//
// HOME is opened one component at a time from the root without following
// symlinks, and every directory below it the same way. Files are created and
// renamed relative to those descriptors, and places are compared by device
// and inode, not by spelling, so case-insensitive aliases and symlinks cannot
// lead a write into the shared configuration. Every check runs before the
// first write, any error a check meets refuses, and the whole path is walked
// again after the last write (Codex on #1075).
func prepareWith(home, accountHome, guardPath string, test hooks) (string, error) {
	files, err := owned(guardPath)
	if err != nil {
		return "", err
	}
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", errors.New("RIBBITTO_ARGON_HOME must be a clean absolute path of a dedicated directory")
	}
	shared, err := openShared(accountHome)
	defer shared.close()
	if err != nil {
		return "", err
	}
	root, err := walk(home)
	if err != nil {
		return "", fmt.Errorf("opening RIBBITTO_ARGON_HOME %s without symlinks: %w", home, err)
	}
	defer closeFD(root)
	if err := separate(root, shared); err != nil {
		return "", err
	}

	gemini, dirs, err := openOwned(root)
	defer closeAll(gemini, dirs)
	if err != nil {
		return "", err
	}
	if err := checkBeforeWrite(root, dirs, files); err != nil {
		return "", err
	}
	// Also before any directory is created: a shared file aliasing an owned
	// one that does not exist yet is dangling, and refuses here (Codex on
	// #1075).
	checked := map[string]place{}
	for name, fd := range dirs {
		if fd < 0 {
			continue
		}
		p, err := identity(fd)
		if err != nil {
			return "", err
		}
		checked[name] = p
	}
	if err := checkSharedFiles(accountHome, files, checked); err != nil {
		return "", err
	}

	test.run(test.beforeWrite)
	// Just before creating or writing anything, HOME must still be the
	// directory checked, and the shared configuration is resolved again, so
	// a swap of either since the checks creates nothing (Argon on #1075).
	if err := sameHome(home, root); err != nil {
		return "", err
	}
	current, err := openShared(accountHome)
	defer current.close()
	if err != nil {
		return "", err
	}
	if err := separate(root, current); err != nil {
		return "", err
	}
	if err := checkSharedFiles(accountHome, files, checked); err != nil {
		return "", err
	}
	// The directories about to be written must be the ones checked (Copilot
	// on #1075), checked again before anything is created, and apart from the
	// shared configuration (Codex on #1075).
	gemini2, dirs2, err := matchOwned(root, gemini, dirs)
	defer func() { closeAll(gemini2, dirs2) }()
	if err != nil {
		return "", err
	}
	if err := checkBeforeWrite(root, dirs2, files); err != nil {
		return "", err
	}
	notShared := func(fd int) error {
		p, err := identity(fd)
		if err != nil {
			return err
		}
		if shared.has(p) || current.has(p) {
			return errors.New("refusing: an owned directory is the shared configuration")
		}
		return nil
	}
	if gemini2, err = createMissing(root, gemini2, dirs2, notShared, test.afterMkdir); err != nil {
		return "", err
	}
	written := map[string]place{}
	for name, fd := range dirs2 {
		p, err := identity(fd)
		if err != nil {
			return "", err
		}
		written[name] = p
	}
	if err := checkBeforeWrite(root, dirs2, files); err != nil {
		return "", err
	}
	if err := checkSharedFiles(accountHome, files, written); err != nil {
		return "", err
	}
	for _, f := range files {
		if err := writeOwned(dirs2[f.dir], f.name, f.content); err != nil {
			return "", fmt.Errorf("writing .gemini/%s/%s: %w", f.dir, f.name, err)
		}
	}
	test.run(test.afterWrite)

	// Walk the path again. The owned directories are where the files went, so
	// they must still be the ones HOME names: HOME or .gemini replaced around
	// them moves no file. They must also still be apart from the shared
	// configuration and hold exactly the launcher's content.
	again, err := walk(home)
	if err != nil {
		return "", fmt.Errorf("re-opening RIBBITTO_ARGON_HOME: %w", err)
	}
	defer closeFD(again)
	gemini3, dirs3, err := openOwned(again)
	defer closeAll(gemini3, dirs3)
	if err != nil {
		return "", err
	}
	for name, want := range written {
		if dirs3[name] < 0 {
			return "", fmt.Errorf("refusing: .gemini/%s disappeared while it was written", name)
		}
		if p, err := identity(dirs3[name]); err != nil || p != want {
			return "", fmt.Errorf("refusing: .gemini/%s changed while it was written", name)
		}
	}
	final, err := openShared(accountHome)
	defer final.close()
	if err != nil {
		return "", err
	}
	if err := separate(again, final); err != nil {
		return "", err
	}
	if err := checkBeforeWrite(again, dirs3, nil); err != nil {
		return "", err
	}
	if err := checkSharedFiles(accountHome, files, written); err != nil {
		return "", err
	}
	for _, f := range files {
		if err := checkOwned(dirs3[f.dir], f.name, f.content); err != nil {
			return "", fmt.Errorf("refusing .gemini/%s/%s: %w", f.dir, f.name, err)
		}
	}
	return home, nil
}

// sameHome refuses when the path no longer leads to the directory checked.
func sameHome(home string, root int) error {
	fd, err := walk(home)
	if err != nil {
		return fmt.Errorf("re-opening RIBBITTO_ARGON_HOME: %w", err)
	}
	defer closeFD(fd)
	now, err := identity(fd)
	if err != nil {
		return err
	}
	if checked, err := identity(root); err != nil || checked != now {
		return errors.New("refusing: RIBBITTO_ARGON_HOME changed after it was checked")
	}
	return nil
}

type place struct{ dev, ino uint64 }

func identity(fd int) (place, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return place{}, fmt.Errorf("refusing: cannot identify a directory: %w", err)
	}
	return place{uint64(st.Dev), st.Ino}, nil
}

// shared holds what HOME must stay apart from, opened following symlinks:
// the account's home, which HOME may lie inside but not be or contain, and
// its .gemini with the two directories under it that the launcher writes in
// its own HOME, which HOME may not be, lie inside or contain.
type shared struct {
	account int
	gemini  []place
	parents []map[place]bool
}

func openShared(accountHome string) (shared, error) {
	s := shared{account: -1}
	fd, err := unix.Open(accountHome, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return s, fmt.Errorf("opening the account's home: %w", err)
	}
	s.account = fd
	for _, rel := range []string{".gemini", ".gemini/antigravity-cli", ".gemini/config"} {
		path := filepath.Join(accountHome, rel)
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			// Absent, unless a dangling symlink names a place this run could
			// create, which would then be shared (Codex on #1075).
			var st unix.Stat_t
			if err := unix.Lstat(path, &st); !errors.Is(err, unix.ENOENT) {
				return s, fmt.Errorf("refusing: the shared %s exists but cannot be resolved", rel)
			}
			continue
		}
		if err != nil {
			return s, fmt.Errorf("refusing: cannot open the shared %s: %w", rel, err)
		}
		p, err := identity(fd)
		var up map[place]bool
		if err == nil {
			up, err = ancestors(fd)
		}
		closeFD(fd)
		if err != nil {
			return s, err
		}
		s.gemini = append(s.gemini, p)
		s.parents = append(s.parents, up)
	}
	return s, nil
}

func (s shared) close() { closeFD(s.account) }

func (s shared) has(p place) bool {
	for _, g := range s.gemini {
		if g == p {
			return true
		}
	}
	return false
}

// checkSharedFiles refuses a shared file, at the place of an owned one, that
// is a symlink into an owned directory: after the launcher's rename it would
// read the launcher's content. A symlink that cannot be resolved, such as one
// to an owned file not yet created, refuses too (the maintainer on #1075).
func checkSharedFiles(accountHome string, files []ownedFile, owned map[string]place) error {
	ownedPlaces := map[place]bool{}
	for _, p := range owned {
		ownedPlaces[p] = true
	}
	for _, f := range files {
		rel := filepath.Join(".gemini", f.dir, f.name)
		path := filepath.Join(accountHome, rel)
		var st unix.Stat_t
		err := unix.Lstat(path, &st)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("refusing: cannot inspect the shared %s: %w", rel, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFLNK {
			continue
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("refusing: the shared %s is a symlink that cannot be resolved", rel)
		}
		fd, err := unix.Open(filepath.Dir(target), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("refusing: cannot open where the shared %s points: %w", rel, err)
		}
		p, err := identity(fd)
		closeFD(fd)
		if err != nil {
			return err
		}
		if ownedPlaces[p] {
			return fmt.Errorf("refusing: the shared %s is a symlink into the dedicated HOME", rel)
		}
	}
	return nil
}

// separate compares identities up the directory chains, so neither a symlink
// nor a differently spelled path (Darwin's case-insensitive names) can make
// HOME the account's home or overlap its shared .gemini. A chain it cannot
// walk to the root refuses.
func separate(root int, s shared) error {
	h, err := identity(root)
	if err != nil {
		return err
	}
	accountUp, err := ancestors(s.account)
	if err != nil {
		return err
	}
	if accountUp[h] {
		return errors.New("refusing RIBBITTO_ARGON_HOME: it is the account's home or an ancestor of it")
	}
	up, err := ancestors(root)
	if err != nil {
		return err
	}
	for i, g := range s.gemini {
		if up[g] || s.parents[i][h] {
			return errors.New("refusing RIBBITTO_ARGON_HOME: it overlaps the account's shared .gemini")
		}
	}
	return nil
}

// ancestors returns the identities of fd's directory and every directory
// above it up to the root; failing to reach the root is an error, never a
// shorter chain.
func ancestors(fd int) (map[place]bool, error) {
	seen := map[place]bool{}
	cur, err := unix.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("refusing: cannot walk up a directory: %w", err)
	}
	for {
		p, err := identity(cur)
		if err != nil {
			closeFD(cur)
			return nil, err
		}
		if seen[p] {
			closeFD(cur)
			return seen, nil
		}
		seen[p] = true
		parent, err := unix.Openat(cur, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		closeFD(cur)
		if err != nil {
			return nil, fmt.Errorf("refusing: cannot walk up a directory: %w", err)
		}
		cur = parent
	}
}

// walk opens path from the root one component at a time, refusing any
// symlink on the way.
func walk(path string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if name == "" {
			continue
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		closeFD(fd)
		if err != nil {
			return -1, fmt.Errorf("%s: %w", name, err)
		}
		fd = next
	}
	return fd, nil
}

// openOwned opens .gemini and the owned directories under it without
// following symlinks; a missing one is -1. Whatever it opened is returned
// even on error, for the caller to close.
func openOwned(root int) (int, map[string]int, error) {
	dirs := map[string]int{}
	for _, name := range ownedDirs {
		dirs[name] = -1
	}
	gemini, err := openDir(root, ".gemini")
	if err != nil || gemini < 0 {
		return gemini, dirs, err
	}
	for _, name := range ownedDirs {
		if dirs[name], err = openDir(gemini, name); err != nil {
			return gemini, dirs, err
		}
	}
	return gemini, dirs, nil
}

// matchOwned opens .gemini and the owned directories again without creating
// anything and requires each to be the one the pre-write checks used: present
// and the same directory, or still absent. Whatever it opened is returned,
// for the caller to close.
func matchOwned(root, checkedGemini int, checked map[string]int) (int, map[string]int, error) {
	gemini, dirs, err := openOwned(root)
	if err != nil {
		return gemini, dirs, err
	}
	same := func(now, then int) error {
		if now < 0 && then < 0 {
			return nil
		}
		if now < 0 || then < 0 {
			return errors.New("refusing: .gemini changed after it was checked")
		}
		a, err := identity(now)
		if err != nil {
			return err
		}
		if b, err := identity(then); err != nil || a != b {
			return errors.New("refusing: .gemini changed after it was checked")
		}
		return nil
	}
	if err := same(gemini, checkedGemini); err != nil {
		return gemini, dirs, err
	}
	for _, name := range ownedDirs {
		if err := same(dirs[name], checked[name]); err != nil {
			return gemini, dirs, err
		}
	}
	return gemini, dirs, nil
}

// createMissing creates what matchOwned found missing, relative to the matched
// descriptors, and refuses as soon as a directory it holds, existing or just
// created, is a shared one: a shared directory moved into HOME between
// creation and opening keeps its identity (Codex on #1075). dirs is updated in
// place; the returned .gemini descriptor replaces the one passed.
func createMissing(root, gemini int, dirs map[string]int, notShared func(int) error, afterMkdir func(string)) (int, error) {
	var err error
	if gemini < 0 {
		if gemini, err = mkdirOpen(root, ".gemini", afterMkdir); err != nil {
			return gemini, err
		}
	}
	if err := notShared(gemini); err != nil {
		return gemini, err
	}
	for _, name := range ownedDirs {
		if dirs[name] < 0 {
			if dirs[name], err = mkdirOpen(gemini, name, afterMkdir); err != nil {
				return gemini, err
			}
		}
		if err := notShared(dirs[name]); err != nil {
			return gemini, err
		}
	}
	return gemini, nil
}

// mkdirOpen creates name under parent and opens it. It must not exist yet:
// one that appeared since the checks is not this run's to use.
func mkdirOpen(parent int, name string, after func(string)) (int, error) {
	if err := unix.Mkdirat(parent, name, 0o700); err != nil {
		return -1, fmt.Errorf("refusing %s: creating it: %w", name, err)
	}
	if after != nil {
		after(name)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("refusing %s: not a plain directory (%w)", name, err)
	}
	return fd, nil
}

func openDir(parent int, name string) (int, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return -1, nil
	}
	if err != nil {
		return -1, fmt.Errorf("refusing %s: not a plain directory (%w)", name, err)
	}
	return fd, nil
}

// checkBeforeWrite refuses customisation, .agents, any owned file that exists
// but is not a regular file, and anything it cannot inspect.
func checkBeforeWrite(root int, dirs map[string]int, files []ownedFile) error {
	if found, err := exists(root, ".agents"); err != nil || found {
		return errors.Join(errors.New("refusing .agents in RIBBITTO_ARGON_HOME: customisation could add tools, hooks or instructions"), err)
	}
	for name, fd := range dirs {
		if fd < 0 {
			continue
		}
		names := refusedCustomisation
		if name == "antigravity-cli" {
			names = append([]string{"hooks.json", "mcp_config.json"}, names...)
		}
		for _, n := range names {
			if found, err := exists(fd, n); err != nil || found {
				return errors.Join(fmt.Errorf("refusing .gemini/%s/%s: customisation could add tools, hooks or instructions", name, n), err)
			}
		}
	}
	for _, f := range files {
		fd := dirs[f.dir]
		if fd < 0 {
			continue
		}
		var st unix.Stat_t
		err := unix.Fstatat(fd, f.name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			return errors.Join(fmt.Errorf("refusing .gemini/%s/%s: not a regular file", f.dir, f.name), err)
		}
	}
	return nil
}

// exists reports whether name is in dir; an error other than "not found"
// is returned, never read as absence.
func exists(dir int, name string) (bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return err == nil, err
}

// writeOwned writes an exclusive temporary file in dir and renames it over
// name, so a symlink put at name meanwhile is replaced rather than followed.
func writeOwned(dir int, name string, content []byte) error {
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
	// Non-blocking, so a FIFO swapped in cannot hang the run (Copilot on #1075);
	// it is refused below as not a regular file.
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
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

func closeFD(fd int) {
	if fd >= 0 {
		_ = unix.Close(fd)
	}
}

func closeAll(gemini int, dirs map[string]int) {
	closeFD(gemini)
	for _, fd := range dirs {
		closeFD(fd)
	}
}
