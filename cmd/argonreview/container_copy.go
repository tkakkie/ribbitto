package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const containerGitTimeout = 30 * time.Second

// A new nested module requires a one-line change here, using its exact lowercase path.
const containerNestedModules = "\ntools/go.mod\ntools/go.sum\ntools/tbls/go.mod\ntools/tbls/go.sum\n"

func containerGit(ctx context.Context, tree string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-C", tree, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never", "-c", "core.excludesFile=/dev/null"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}
	cmd.WaitDelay = containerCleanupTimeout
	return cmd
}

func listContainerFiles(ctx context.Context, tree string) ([]string, error) {
	return listContainerFilesWithin(ctx, tree, containerGitTimeout)
}

func listContainerFilesWithin(parent context.Context, tree string, timeout time.Duration) ([]string, error) {
	// Bound both Git commands even when metadata opens a FIFO or triggers a
	// stalled helper. The caller's longer container deadline is insufficient.
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	prefix, err := containerGit(ctx, tree, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return nil, fmt.Errorf("checking worktree root: %w", errors.Join(err, ctx.Err()))
	}
	if len(bytes.TrimSpace(prefix)) != 0 {
		return nil, errors.New("worktree must be the repository root")
	}
	// A bounded writer also bounds a hostile index/listing before splitting it.
	var out containerOutput
	out.cancel = func() {}
	cmd := containerGit(ctx, tree, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Stdout, cmd.Stderr = out.stdoutWriter(), out.stderrWriter()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("listing source files: %w", errors.Join(err, ctx.Err()))
	}
	if out.over {
		return nil, errors.New("source listing exceeds the output limit")
	}
	data := out.stdout.String()
	if data != "" && !strings.HasSuffix(data, "\x00") {
		return nil, errors.New("source listing is cut off")
	}
	if data == "" {
		return nil, nil
	}
	files := strings.Split(strings.TrimSuffix(data, "\x00"), "\x00")
	slices.Sort(files)
	return slices.Compact(files), nil
}

// Validate every component before omitting a baseline tree: a nested refused
// name must not be hidden underneath an omitted .github or .git entry.
func containerCopyPath(name string) (omit bool, err error) {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") {
		return false, fmt.Errorf("unsafe source path %q", name)
	}
	for i, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || part == "" {
			return false, fmt.Errorf("unsafe source path %q", name)
		}
		switch strings.ToLower(part) {
		case "go.work", "go.work.sum":
			return false, fmt.Errorf("refused source path %q", name)
		case "go.mod", "go.sum":
			if i != 0 && !strings.Contains(containerNestedModules, "\n"+name+"\n") {
				return false, fmt.Errorf("refused nested module path %q", name)
			}
		case ".git", ".github", "agents.md", "claude.md", ".gitattributes", ".gitmodules":
			if i != 0 {
				return false, fmt.Errorf("refused source path %q", name)
			}
			omit = true
		case ".agents", ".agent", "_agents", "_agent", ".gemini", ".claude", ".codex", ".mcp.json", "hooks.json", "plugin.json", "skills.json", "plugins.json", "gemini.md", "agents.override.md", "claude.local.md":
			return false, fmt.Errorf("refused source path %q", name)
		}
	}
	return omit, nil
}

type containerSourceStamp struct {
	info  os.FileInfo
	ctime unix.Timespec
}

func stampContainerSource(f *os.File) (containerSourceStamp, error) {
	info, err := f.Stat()
	if err != nil {
		return containerSourceStamp{}, fmt.Errorf("examining source descriptor: %w", err)
	}
	if !info.Mode().IsRegular() {
		return containerSourceStamp{}, errors.New("source is not a regular file")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return containerSourceStamp{}, fmt.Errorf("examining source change time: %w", err)
	}
	if uint64(st.Nlink) != 1 {
		return containerSourceStamp{}, errors.New("source regular file has multiple hard links")
	}
	return containerSourceStamp{info, st.Ctim}, nil
}

func sameContainerSource(a, b containerSourceStamp) bool {
	return os.SameFile(a.info, b.info) && a.info.Size() == b.info.Size() && a.info.ModTime().Equal(b.info.ModTime()) && a.info.Mode() == b.info.Mode() && a.ctime == b.ctime
}

// Every component is opened relative to a held descriptor, never following a
// link. NONBLOCK prevents a FIFO planted after listing from hanging the copy;
// fstat on the opened file, rather than an earlier pathname check, decides.
func openContainerSource(root int, name string) (*os.File, error) {
	fd, err := unix.Dup(root)
	if err != nil {
		return nil, fmt.Errorf("duplicating source root: %w", err)
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i != len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		closeFD(fd)
		if openErr != nil {
			return nil, fmt.Errorf("opening source %q: %w", name, openErr)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), name), nil
}

func containerEntryAllowed(mode uint32) bool {
	return mode&unix.S_IFMT == unix.S_IFREG || mode&unix.S_IFMT == unix.S_IFDIR
}

// Git hides nested repositories and empty/ignored directories. Inspect names
// there too, without following links or copying ignored data, so an empty .git
// cannot evade the refusal. Bound the inspection as well as the actual copy.
func checkContainerNames(ctx context.Context, root int) error {
	seen := 0
	var scan func(int, string) error
	scan = func(fd int, prefix string) error {
		f := os.NewFile(uintptr(fd), prefix)
		defer func() { _ = f.Close() }()
		for {
			entries, err := f.ReadDir(128)
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("inspecting source names: %w", err)
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return fmt.Errorf("inspecting source names: %w", err)
				}
				seen++
				if seen > containerFileLimit {
					return errors.New("source inspection count exceeds limit")
				}
				name := path.Join(prefix, entry.Name())
				if _, err := containerCopyPath(name); err != nil {
					return err
				}
				var st unix.Stat_t
				if err := unix.Fstatat(fd, entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					return fmt.Errorf("inspecting source entry %q: %w", name, err)
				}
				if !containerEntryAllowed(uint32(st.Mode)) {
					return fmt.Errorf("source entry %q is not a regular file or directory", name)
				}
				if st.Mode&unix.S_IFMT == unix.S_IFREG && uint64(st.Nlink) != 1 {
					return fmt.Errorf("source regular file %q has multiple hard links", name)
				}
				// Linked worktrees use a root .git file; ordinary repositories use
				// a directory. Both are omitted, without descending. Their integrity
				// is implement-check's job (#1434): compare with the saved copy
				// before and after a run.
				if prefix == "" && strings.EqualFold(entry.Name(), ".git") {
					continue
				}
				if st.Mode&unix.S_IFMT == unix.S_IFDIR {
					child, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
					if err != nil {
						return fmt.Errorf("inspecting source directory %q: %w", name, err)
					}
					if err := scan(child, name); err != nil {
						return err
					}
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
	// A fresh open file description keeps ReadDir's offset independent of root.
	fd, err := unix.Openat(root, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("inspecting source root: %w", err)
	}
	return scan(fd, "")
}

// hook is used only by race fixtures, at the points where an attacker could
// replace or modify a listed source. Production passes nil.
func copyContainerSource(ctx context.Context, tree, dest string, list func(context.Context, string) ([]string, error), hook func(stage, name string)) error {
	root, err := walk(tree)
	if err != nil {
		return fmt.Errorf("opening source root without symlinks: %w", err)
	}
	defer closeFD(root)
	if err := checkContainerNames(ctx, root); err != nil {
		return err
	}
	files, err := list(ctx, tree)
	if err != nil {
		return err
	}
	selected := make([]string, 0, min(len(files), containerFileLimit))
	for _, name := range files {
		omit, err := containerCopyPath(name)
		if err != nil {
			return err
		}
		if !omit {
			selected = append(selected, name)
		}
		if len(selected) > containerFileLimit {
			return errors.New("source file count exceeds limit")
		}
	}
	if err := os.Mkdir(dest, 0o777); err != nil {
		return fmt.Errorf("creating copied tree: %w", err)
	}
	// Chmod defeats a restrictive caller umask and keeps directories removable
	// during cleanup. The mount enforces read-only access for the container;
	// the private parent stays 0700. No owner or special mode is preserved.
	if err := os.Chmod(dest, 0o777); err != nil {
		return fmt.Errorf("setting copied tree mode: %w", err)
	}
	stamps := make(map[string]containerSourceStamp, len(selected))
	var total int64
	for _, name := range selected {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("copying source: %w", err)
		}
		if hook != nil {
			hook("before-open", name)
		}
		f, err := openContainerSource(root, name)
		if err != nil {
			return err
		}
		stamp, copyErr := copyContainerFile(f, dest, name, containerCopyLimit-total, hook)
		closeErr := f.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return fmt.Errorf("copying %q: %w", name, err)
		}
		stamps[name] = stamp
		total += stamp.info.Size()
	}
	if hook != nil {
		hook("after-copy", "")
	}
	for _, name := range selected {
		f, err := openContainerSource(root, name)
		if err != nil {
			return err
		}
		now, statErr := stampContainerSource(f)
		closeErr := f.Close()
		if err := errors.Join(statErr, closeErr); err != nil {
			return err
		}
		if !sameContainerSource(stamps[name], now) {
			return fmt.Errorf("source %q changed after copying", name)
		}
	}
	if err := checkContainerRoot(tree, root); err != nil {
		return err
	}
	if err := checkContainerNames(ctx, root); err != nil {
		return err
	}
	final, err := list(ctx, tree)
	if err != nil {
		return err
	}
	if hook != nil {
		hook("after-final-list", "")
	}
	if err := checkContainerRoot(tree, root); err != nil {
		return err
	}
	if !slices.Equal(files, final) {
		return errors.New("source file set changed while copying")
	}
	return nil
}

// Listing uses a pathname, so its root must still match the held descriptor
// both before and after Git runs, even when a replacement has the same files.
func checkContainerRoot(tree string, root int) error {
	before, err := identity(root)
	if err != nil {
		return err
	}
	again, err := walk(tree)
	if err != nil {
		return fmt.Errorf("reopening source root: %w", err)
	}
	defer closeFD(again)
	after, err := identity(again)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("source root changed while copying")
	}
	return nil
}

func copyContainerFile(f *os.File, dest, name string, remaining int64, hook func(string, string)) (containerSourceStamp, error) {
	before, err := stampContainerSource(f)
	if err != nil {
		return before, err
	}
	if before.info.Size() > remaining {
		return before, errors.New("source total size exceeds limit")
	}
	if hook != nil {
		hook("after-stat", name)
	}
	parent := filepath.Dir(name)
	if parent != "." {
		current := dest
		for _, part := range strings.Split(parent, "/") {
			current = filepath.Join(current, part)
			if err := os.MkdirAll(current, 0o777); err != nil {
				return before, fmt.Errorf("creating copy directory: %w", err)
			}
			if err := os.Chmod(current, 0o777); err != nil {
				return before, fmt.Errorf("setting copy directory mode: %w", err)
			}
		}
	}
	mode := os.FileMode(0o666)
	if before.info.Mode().Perm()&0o111 != 0 {
		mode = 0o777
	}
	dst, err := os.OpenFile(filepath.Join(dest, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return before, fmt.Errorf("creating copied file: %w", err)
	}
	n, copyErr := io.Copy(dst, io.LimitReader(f, before.info.Size()+1))
	modeErr := dst.Chmod(mode)
	closeErr := dst.Close()
	if err := errors.Join(copyErr, modeErr, closeErr); err != nil {
		return before, fmt.Errorf("writing copied file: %w", err)
	}
	if hook != nil {
		hook("after-read", name)
	}
	after, err := stampContainerSource(f)
	if err != nil {
		return before, err
	}
	if n != before.info.Size() || !sameContainerSource(before, after) {
		return before, errors.New("source changed during copying")
	}
	return before, nil
}
