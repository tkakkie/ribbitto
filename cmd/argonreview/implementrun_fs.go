package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Validate even the currently absent suffix before creating any directory.
func implementRunTarget(path string, directory bool) error {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if i < len(parts)-1 || directory {
			if !info.IsDir() {
				return fmt.Errorf("not a real directory: %q", current)
			}
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %q", current)
		}
	}
	return nil
}

// Saved comparison bytes must not alias a file the implementer can edit.
func implementRunSingleLink(path string) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	if st.Nlink != 1 {
		return errors.New("saved .git file must have exactly one link")
	}
	return nil
}

func implementRunMkdir(path string) error {
	if path == "/" {
		return nil
	}
	if err := implementRunMkdir(filepath.Dir(path)); err != nil {
		return err
	}
	fd, err := walk(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer closeFD(fd)
	if err := unix.Mkdirat(fd, filepath.Base(path), 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	opened, err := walk(path)
	if err == nil {
		closeFD(opened)
	}
	return err
}

// Keep the parent descriptor across unlink/create; a swapped parent cannot
// redirect writes. O_EXCL refuses any replacement of the old regular leaf.
func implementRunOpen(path string, replace bool) (*os.File, error) {
	parent, err := walk(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer closeFD(parent)
	name := filepath.Base(path)
	var st unix.Stat_t
	err = unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if !replace || st.Mode&unix.S_IFMT != unix.S_IFREG {
			return nil, fmt.Errorf("refusing existing target %q", path)
		}
		if err := unix.Unlinkat(parent, name, 0); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	fd, err := unix.Openat(parent, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func implementRunWrite(path string, data []byte) error {
	if err := implementRunMkdir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := implementRunOpen(path, true)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

// Remove only children of a real directory, using a confined descriptor.
// Root.RemoveAll unlinks symlink leaves and never traverses them. Keeping the
// root open also prevents a concurrent rename redirecting the child removals.
func implementRunEmpty(path string) error {
	fd, err := walk(path)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	before, statErr := f.Stat()
	after, rootErr := root.Stat(".")
	if statErr != nil || rootErr != nil || !os.SameFile(before, after) {
		return errors.New("directory changed while opening removal root")
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	var result error
	for _, entry := range entries {
		result = errors.Join(result, root.RemoveAll(entry.Name()))
	}
	return result
}

func implementRunRemove(path string, directory bool) error {
	fd, err := walk(filepath.Dir(path))
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer closeFD(fd)
	flags := 0
	if directory {
		flags = unix.AT_REMOVEDIR
	}
	err = unix.Unlinkat(fd, filepath.Base(path), flags)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
