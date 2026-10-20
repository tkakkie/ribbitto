package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func lockContainerCopy(dir string) (*os.File, error) {
	f, err := os.Open(dir) // os.Open sets CLOEXEC; children must never keep liveness alive.
	if err != nil {
		return nil, fmt.Errorf("opening copy lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking copy: %w", err)
	}
	return f, nil
}
func openSweepParent(parent string) (int, error) {
	return unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// Keep the successful probe locked until inspection/removal finishes, so two
// sweepers cannot concurrently reclaim the same copy.
func (s *containerSweep) openCopy(parent int, name string) (*os.File, unix.Stat_t, bool, error) {
	var before, st, parentStat unix.Stat_t
	if err := s.statAt(parent, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, st, false, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, st, false, errors.New("unsafe copy type")
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, st, false, err
	}
	f := os.NewFile(uintptr(fd), name)
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, st, false, err
	}
	if err := unix.Fstat(parent, &parentStat); err != nil || !s.sameDevice(uint64(parentStat.Dev), uint64(st.Dev)) {
		_ = f.Close()
		return nil, st, false, errors.New("copy root crosses device")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || before.Dev != st.Dev || before.Ino != st.Ino || int(st.Uid) != s.uid() || st.Mode&0o7777 != 0o700 {
		_ = f.Close()
		return nil, st, false, errors.New("unsafe copy identity")
	}
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return f, st, true, nil
	}
	if err != nil {
		_ = f.Close()
		return nil, st, false, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, st, false, err
	}
	return f, st, s.now().Sub(info.ModTime()) < sweepGrace, nil
}

// Real Docker's host-mount form must be recorded by the host preflight.
const sweepHostMountPrefixes = "/host_mnt:/run/host_mnt:/run/desktop/mnt/host"

// A mount of either a copy subtree or its ancestor can expose the copy.
func sweepPathsOverlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	within := func(parent, child string) bool {
		return parent == child || strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
	}
	if within(a, b) || within(b, a) {
		return true
	}
	for _, component := range strings.Split(b, string(filepath.Separator)) {
		if component == filepath.Base(a) {
			return true
		}
	}
	alias := a
	if strings.HasPrefix(a, "/private/tmp/") {
		alias = strings.TrimPrefix(a, "/private")
	} else if strings.HasPrefix(a, "/tmp/") {
		alias = "/private" + a
	}
	for _, prefix := range filepath.SplitList(sweepHostMountPrefixes) {
		if b == prefix || strings.HasPrefix(b, prefix+"/") {
			b = strings.TrimPrefix(b, prefix)
			if b == "" {
				b = "/"
			}
			break
		}
	}
	if within(a, b) || within(b, a) || within(alias, b) || within(b, alias) {
		return true
	}
	return false
}

func (s *containerSweep) mounted(dir string) (bool, error) {
	ids, err := s.ids(false)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		c, err := s.inspect(id)
		if err != nil {
			if s.absent(id) {
				continue
			}
			return false, err
		}
		for _, m := range c.Mounts {
			if m.Source == "" {
				if m.Type != "bind" {
					continue
				}
				return false, errors.New("empty bind source")
			}
			if !filepath.IsAbs(m.Source) || strings.ContainsRune(m.Source, 0) {
				return false, errors.New("invalid mount source")
			}
			for _, component := range strings.Split(m.Source, "/") {
				if component == ".." {
					return false, errors.New("parent traversal in mount source")
				}
			}
			real, err := s.resolveMount(m.Source)
			// Non-bind Sources need lexical protection even when host resolution fails.
			if m.Type == "bind" && err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOTDIR) {
				return false, err
			}
			if sweepPathsOverlap(dir, m.Source) || err == nil && sweepPathsOverlap(dir, real) {
				return true, nil
			}
		}
	}
	return false, nil
}
func (s *containerSweep) copies() {
	if s.copyRoot == nil {
		return
	}
	f := s.copyRoot
	fd, parent := int(f.Fd()), s.resolvedParent
	entries, err := s.readDir(f, -1)
	if err != nil && !errors.Is(err, io.EOF) {
		s.fail("listing copies")
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !sweepCopyName.MatchString(name) {
			continue
		}
		dir := filepath.Join(parent, name)
		copy, st, live, err := s.openCopy(fd, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			s.unreclaimed("copy", dir)
			continue
		}
		s.copy(fd, name, dir, copy, st, live)
	}
}
func (s *containerSweep) copy(parent int, name, dir string, f *os.File, st unix.Stat_t, live bool) {
	defer func() { _ = f.Close() }()
	if live {
		s.line("skipped (in use) copy %s", dir)
		return
	}
	mounted, err := s.mounted(dir)
	if err != nil {
		s.fail("checking copy mounts")
		s.unreclaimed("copy", dir)
		return
	}
	if mounted {
		s.line("skipped (in use) copy %s", dir)
		return
	}
	s.line("leftover copy %s", dir)
	if s.dry {
		s.fail("detecting leftover copy")
		return
	}
	if err := s.removeCopy(f, st, 0); err != nil {
		s.unreclaimed("copy", dir)
		return
	}
	// Refuse replacement of the root pathname before the final unlinkat.
	var current unix.Stat_t
	if err := s.statAt(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) && s.copyEntryGone(parent, name) {
		s.line("reclaimed copy %s", dir)
		return
	} else if err != nil || current.Dev != st.Dev || current.Ino != st.Ino {
		s.unreclaimed("copy", dir)
		return
	}
	if err := s.unlinkAt(parent, name, unix.AT_REMOVEDIR); err != nil && (!errors.Is(err, unix.ENOENT) || !s.copyEntryGone(parent, name)) {
		s.unreclaimed("copy", dir)
		return
	}
	s.line("reclaimed copy %s", dir)
}

func (s *containerSweep) copyEntryGone(fd int, name string) bool {
	var st unix.Stat_t
	return errors.Is(s.statAt(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW), unix.ENOENT)
}

func (s *containerSweep) removeCopy(f *os.File, root unix.Stat_t, depth int) error {
	if depth > 256 {
		return errors.New("copy nesting limit")
	}
	// The ninth listing only verifies the eight deletion passes.
	for pass := 0; pass <= 8; pass++ {
		// Reopen through the held descriptor: deleting while reading can skip
		// entries on macOS, and each full pass must start at the beginning.
		reader, err := unix.Openat(int(f.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("reopening copy: %w", err)
		}
		listing := os.NewFile(uintptr(reader), f.Name())
		entries, err := s.readDir(listing, -1)
		_ = listing.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading copy: %w", err)
		}
		if len(entries) == 0 {
			return nil
		}
		if pass == 8 {
			return errors.New("copy removal pass limit")
		}
		for _, entry := range entries {
			fd, name := int(f.Fd()), entry.Name()
			var st unix.Stat_t
			if err := s.statAt(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if errors.Is(err, unix.ENOENT) && s.copyEntryGone(fd, name) {
					continue
				}
				return fmt.Errorf("examining copy entry: %w", err)
			}
			flags := 0
			if st.Mode&unix.S_IFMT == unix.S_IFDIR {
				child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					if errors.Is(err, unix.ENOENT) && s.copyEntryGone(fd, name) {
						continue
					}
					return fmt.Errorf("opening copy entry: %w", err)
				}
				sub := os.NewFile(uintptr(child), name)
				var opened unix.Stat_t
				statErr := unix.Fstat(child, &opened)
				if statErr != nil || st.Dev != opened.Dev || st.Ino != opened.Ino || !s.sameDevice(uint64(root.Dev), uint64(opened.Dev)) {
					_ = sub.Close()
					return errors.New("copy entry changed or crosses device")
				}
				removeErr := s.removeCopy(sub, root, depth+1)
				_ = sub.Close()
				if removeErr != nil {
					return removeErr
				}
				flags = unix.AT_REMOVEDIR
			}
			var again unix.Stat_t
			if err := s.statAt(fd, name, &again, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) && s.copyEntryGone(fd, name) {
				continue
			} else if err != nil || st.Dev != again.Dev || st.Ino != again.Ino {
				return errors.New("copy entry changed")
			}
			if err := s.unlinkAt(fd, name, flags); err != nil && (!errors.Is(err, unix.ENOENT) || !s.copyEntryGone(fd, name)) {
				return fmt.Errorf("unlinking copy entry: %w", err)
			}
		}
	}
	return errors.New("copy removal pass limit")
}
