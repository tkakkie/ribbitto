package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// These operations keep failure injection at the filesystem boundary. Path
// checks deliberately retain the hook's check/create race, not a tamper-proof
// boundary against the dedicated user or an allow-everything agent.
type agyCheckFiles struct {
	lstat   func(string) (os.FileInfo, error)
	read    func(string, int64) ([]byte, error)
	entries func(string) ([]os.DirEntry, error)
	mkdir   func(string, os.FileMode) error
	temp    func(string, string) (string, error)
	open    func(string, int, os.FileMode) (*os.File, error)
	remove  func(string) error
	uid     int
}

func agyCheckFilesDefaults() agyCheckFiles {
	return agyCheckFiles{os.Lstat, func(p string, n int64) ([]byte, error) { return implementRead(p, n, false) },
		os.ReadDir, os.Mkdir, os.MkdirTemp, os.OpenFile, os.Remove, os.Getuid()}
}

// Walk from / before reaching the leaf, including for an absent target. Only
// ancestors may be absent when creating a new tree; symlinks are never followed.
func (f agyCheckFiles) path(root, path string) error {
	if !agyCheckCleanPath(root) || !agyCheckCleanPath(path) || strings.EqualFold(root, path) ||
		!implementRunInside(strings.ToLower(root), strings.ToLower(path)) || !implementRunInside(root, path) {
		return errors.New("path must be strictly inside its isolation root without aliases")
	}
	current := string(filepath.Separator)
	missing := false
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if missing {
			continue
		}
		info, err := f.lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return errors.New("examining confined path")
		}
		if info.Mode()&os.ModeSymlink != 0 || current != path && !info.IsDir() {
			return errors.New("confined path contains a symlink or non-directory ancestor")
		}
	}
	return nil
}

func (f agyCheckFiles) private(path string, directory bool) error {
	return agyCheckHookPrivate(path, directory, f.uid, agyCheckHookDeps{lstat: f.lstat})
}

func (f agyCheckFiles) write(root, path string, data []byte, exclusive bool, mode os.FileMode) (err error) {
	if err := f.path(root, path); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | syscall.O_NOFOLLOW
	if exclusive {
		flags |= os.O_EXCL
	} else {
		flags |= os.O_TRUNC
	}
	out, err := f.open(path, flags, mode)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if err := out.Chmod(mode); err != nil {
		return err
	}
	return agyCheckHookWrite(out, data)
}

func (f agyCheckFiles) readInside(root, path string, cap int64) ([]byte, error) {
	if err := f.path(root, path); err != nil {
		return nil, err
	}
	return f.read(path, cap)
}

// Do not use RemoveAll: a changed tree containing a symlink is invalid and
// must be reported, rather than silently followed or repaired during cleanup.
func (f agyCheckFiles) cleanup(root, path string) error {
	if err := f.path(root, path); err != nil {
		return err
	}
	info, err := f.lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := f.entries(path)
		if err != nil {
			return err
		}
		// Alternate hooks leave two events per call.
		cap := agyCheckHookCallCap
		if filepath.Base(path) == "events" {
			cap *= 2
		}
		if len(entries) > cap {
			return errors.New("cleanup entry limit exceeded")
		}
		for _, entry := range entries {
			if err := f.cleanup(root, filepath.Join(path, entry.Name())); err != nil {
				return err
			}
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("unknown cleanup entry")
	}
	if err := f.remove(path); err != nil {
		return err
	}
	if _, err := f.lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("cleanup left an entry")
	}
	return nil
}

type agyCheckRowDirs struct{ Run, Row, State, Stub, Workspace, Outside, Tmp string }

func agyCheckNewRowDirs(isolation string, f agyCheckFiles) (r agyCheckRowDirs, err error) {
	if err := f.path(isolation, filepath.Join(isolation, "run")); err != nil {
		return r, err
	}
	if err := f.private(isolation, true); err != nil {
		return r, err
	}
	r.Run, err = f.temp(isolation, "run-*")
	if err != nil {
		return r, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, f.cleanup(isolation, r.Run))
		}
	}()
	r.Row = filepath.Join(r.Run, "row")
	if err := f.path(isolation, r.Row); err != nil {
		return r, err
	}
	if err := f.mkdir(r.Row, 0o700); err != nil {
		return r, err
	}
	r.State, r.Stub = filepath.Join(r.Row, "state"), filepath.Join(r.Row, "stub")
	r.Workspace, r.Outside, r.Tmp = filepath.Join(r.Row, "workspace"), filepath.Join(r.Row, "outside"), filepath.Join(r.Row, "tmp")
	for _, path := range []string{r.State, r.Stub, r.Workspace, r.Outside, r.Tmp} {
		if err := f.path(isolation, path); err != nil {
			return r, err
		}
		if err := f.mkdir(path, 0o700); err != nil {
			return r, err
		}
	}
	for _, path := range []string{r.State, r.Stub} {
		if err := f.write(isolation, filepath.Join(path, agyCheckHookSentinel), nil, true, 0o600); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (r agyCheckRowDirs) validate(isolation string, f agyCheckFiles) error {
	for _, path := range []string{r.Run, r.Row, r.State, r.Stub, r.Workspace, r.Outside, r.Tmp} {
		if err := f.path(isolation, path); err != nil {
			return err
		}
		if err := f.private(path, true); err != nil {
			return err
		}
	}
	if filepath.Dir(r.Run) != isolation || r.Row != filepath.Join(r.Run, "row") {
		return errors.New("invalid run layout")
	}
	for name, path := range map[string]string{"state": r.State, "stub": r.Stub, "workspace": r.Workspace, "outside": r.Outside, "tmp": r.Tmp} {
		if path != filepath.Join(r.Row, name) {
			return errors.New("invalid row layout")
		}
	}
	for _, path := range []string{r.State, r.Stub} {
		if err := f.private(filepath.Join(path, agyCheckHookSentinel), false); err != nil {
			return err
		}
	}
	return nil
}

func agyCheckReadLog(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, agyCheckTranscriptMaxBytes+1))
	if len(data) > agyCheckTranscriptMaxBytes {
		return nil, errors.New("transcript exceeds 4 MiB")
	}
	return data, err
}

// Retention truncates oversized logs instead of accepting them as evidence.
func (f agyCheckFiles) readPrefixInside(root, path string, cap int64) (data []byte, err error) {
	if err := f.path(root, path); err != nil {
		return nil, err
	}
	if err := f.private(path, false); err != nil {
		return nil, err
	}
	in, err := f.open(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	return io.ReadAll(io.LimitReader(in, cap))
}
