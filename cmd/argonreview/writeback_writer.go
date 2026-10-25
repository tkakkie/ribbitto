package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type writebackFile struct {
	entry                  writebackEntry
	parent                 int
	before                 unix.Stat_t // Mode == 0 means absent; no valid regular file has that mode.
	oldHash, newHash, temp string
	identical, written     bool
}

func openWritebackParent(root int, name string) (int, error) {
	fd, err := unix.Openat(root, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		closeFD(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// Do not compare atime: our own content read can change it.
func sameWritebackIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Nlink == b.Nlink && a.Uid == b.Uid && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func writebackDestinationOK(st unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink == 1 && st.Uid == uint32(os.Getuid()) && st.Size >= 0 && st.Size <= writebackFileLimit
}

func (f *writebackFile) validate(hook func(string, string)) error {
	name := path.Base(f.entry.name)
	err := unix.Fstatat(f.parent, name, &f.before, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		f.before = unix.Stat_t{}
		if !strings.HasSuffix(name, "_templ.go") && !strings.HasSuffix(name, ".sql.go") {
			return errors.New("destination must already exist")
		}
		f.oldHash = "absent"
		return nil
	}
	if err != nil {
		return fmt.Errorf("examining destination: %w", err)
	}
	if !writebackDestinationOK(f.before) {
		return errors.New("destination must be a bounded regular file with one link owned by the running user")
	}
	if hook != nil {
		hook("before-destination-open", f.entry.name)
	}
	// NONBLOCK prevents a FIFO substitution from hanging before fstat.
	fd, err := unix.Openat(f.parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("opening destination: %w", err)
	}
	src := os.NewFile(uintptr(fd), name)
	var opened, after unix.Stat_t
	statErr := unix.Fstat(fd, &opened)
	var data []byte
	if statErr == nil && writebackDestinationOK(opened) && sameWritebackIdentity(f.before, opened) {
		data, err = io.ReadAll(io.LimitReader(src, writebackFileLimit+1))
	} else {
		err = errors.Join(statErr, errors.New("destination changed before reading"))
	}
	afterErr := unix.Fstat(fd, &after)
	closeErr := src.Close()
	if err := errors.Join(err, afterErr, closeErr); err != nil {
		return fmt.Errorf("reading destination: %w", err)
	}
	if len(data) > writebackFileLimit || !sameWritebackIdentity(opened, after) {
		return errors.New("destination changed while reading")
	}
	f.oldHash = fmt.Sprintf("%x", sha256.Sum256(data))
	f.identical = bytes.Equal(data, f.entry.content)
	return nil
}

// Prepare every temp before any rename: disk/write/fsync failures in this phase
// leave all destinations untouched. Explicit chmod defeats a restrictive umask.
func (f *writebackFile) prepare() error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("naming temporary file: %w", err)
	}
	name := fmt.Sprintf(".argonreview-writeback-%x", random)
	fd, err := unix.Openat(f.parent, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	f.temp = name
	dst := os.NewFile(uintptr(fd), name)
	modeErr := dst.Chmod(0o644)
	_, writeErr := io.Copy(dst, bytes.NewReader(f.entry.content))
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if err := errors.Join(modeErr, writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("preparing temporary file: %w", err)
	}
	return nil
}

func (f *writebackFile) recheck() error {
	var now unix.Stat_t
	err := unix.Fstatat(f.parent, path.Base(f.entry.name), &now, unix.AT_SYMLINK_NOFOLLOW)
	if f.before.Mode == 0 && errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("rechecking destination: %w", err)
	}
	if f.before.Mode == 0 || !sameWritebackIdentity(f.before, now) {
		return errors.New("destination identity changed before rename")
	}
	return nil
}

// Reopen the path as well as holding descriptors: a parent moved out of the
// tree must not receive a write just because its descriptor remains usable.
func (f *writebackFile) recheckParent(tree string, root int) error {
	if err := checkContainerRoot(tree, root); err != nil {
		return err
	}
	fd, err := openWritebackParent(root, f.entry.name)
	if err != nil {
		return fmt.Errorf("reopening destination parent: %w", err)
	}
	defer closeFD(fd)
	before, err := identity(f.parent)
	if err != nil {
		return err
	}
	now, err := identity(fd)
	if err != nil {
		return err
	}
	if before != now {
		return errors.New("destination parent changed")
	}
	return nil
}

// Atomicity is per file, not per set. After the first rename a later identity
// check/rename failure leaves replaced files in place. Unused temps are removed
// via their held parent descriptors (cleanup failures are reported). Retrying is
// idempotent: identical content is skipped. No run process may be alive here.
func writebackFiles(tree string, root int, entries []writebackEntry, repo writebackRepo, hook func(string, string)) (report string, err error) {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b writebackEntry) int { return strings.Compare(a.name, b.name) })
	var files []*writebackFile
	defer func() {
		// Directory durability is best effort: replacements already happened, so a
		// failure is reported with nonzero status rather than promising rollback.
		for _, f := range files {
			if f.written {
				if syncErr := unix.Fsync(f.parent); syncErr != nil {
					err = errors.Join(err, writebackNameError(f.entry.name, "syncing destination directory: "+syncErr.Error()))
				}
			}
		}
		for _, f := range files {
			if f.temp != "" {
				if cleanupErr := unix.Unlinkat(f.parent, f.temp, 0); cleanupErr != nil {
					err = errors.Join(err, writebackNameError(f.entry.name, "cannot remove temporary file "+writebackQuotedName(f.temp)+": "+cleanupErr.Error()))
				}
			}
			closeFD(f.parent)
		}
		report = writebackReport(entries, files, err)
	}()
	if err := validateWritebackPaths(entries, repo); err != nil {
		return "", err
	}
	for _, entry := range entries {
		fd, err := openWritebackParent(root, entry.name)
		if err != nil {
			return "", writebackNameError(entry.name, "opening destination parent: "+err.Error())
		}
		f := &writebackFile{entry: entry, parent: fd, newHash: fmt.Sprintf("%x", sha256.Sum256(entry.content))}
		files = append(files, f)
		if err := f.validate(hook); err != nil {
			return "", writebackNameError(entry.name, err.Error())
		}
	}
	if hook != nil {
		hook("after-validation", "")
	}
	for _, f := range files {
		if f.identical {
			continue
		}
		if err := f.recheckParent(tree, root); err != nil {
			return "", writebackNameError(f.entry.name, err.Error())
		}
		if hook != nil {
			hook("before-temp", f.entry.name)
		}
		if err := f.prepare(); err != nil {
			return "", writebackNameError(f.entry.name, err.Error())
		}
	}
	// This second phase is the only partial-replacement window. Recheck even
	// identical files so a changed skipped file cannot yield success.
	for _, f := range files {
		if hook != nil {
			hook("before-rename", f.entry.name)
		}
		if err := f.recheckParent(tree, root); err != nil {
			return "", writebackNameError(f.entry.name, err.Error())
		}
		if err := f.recheck(); err != nil {
			return "", writebackNameError(f.entry.name, err.Error())
		}
		if f.identical {
			continue
		}
		if err := unix.Renameat(f.parent, f.temp, f.parent, path.Base(f.entry.name)); err != nil {
			return "", writebackNameError(f.entry.name, "renaming temporary file: "+err.Error())
		}
		f.temp, f.written = "", true
	}
	return "", err
}

// No content or raw untrusted bytes cross this boundary. Entry count and name
// caps bound the report independently of container output and generator logs.
func writebackReport(entries []writebackEntry, files []*writebackFile, err error) string {
	var out strings.Builder
	written, identical := 0, 0
	for _, f := range files {
		if f.written {
			written++
			fmt.Fprintf(&out, "written %s sha256 %s -> %s\n", writebackQuotedName(f.entry.name), f.oldHash, f.newHash)
		}
		if f.identical {
			identical++
		}
	}
	fmt.Fprintf(&out, "%d written, %d identical\n", written, identical)
	if err != nil {
		writebackRefusalLines(&out, err)
		for _, entry := range entries {
			replaced := false
			for _, f := range files {
				if f.entry.name == entry.name {
					replaced = f.written
					break
				}
			}
			if !replaced {
				fmt.Fprintf(&out, "not replaced %s\n", writebackQuotedName(entry.name))
			}
		}
	}
	out.WriteString("Stale files: nothing deleted; files no longer produced remain for review.\n")
	return out.String()
}

// Bound each refusal separately so a long primary error cannot hide a later
// temporary-file cleanup failure. At most one cleanup error exists per entry.
func writebackRefusalLines(out *strings.Builder, err error) {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			writebackRefusalLines(out, cause)
		}
		return
	}
	fmt.Fprintf(out, "refused: %s\n", cutGeneratedText(strconv.Quote(err.Error()), 4000))
}
