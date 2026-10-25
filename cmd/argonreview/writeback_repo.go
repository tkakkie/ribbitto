package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type diskWritebackRepo struct {
	ctx                context.Context
	tree, base, format string
	root, hexLen       int
}

// Git metadata remains trusted by the post-run saved-.git checks. Bound every
// plumbing call; never run filters, hooks, lazy fetches or caller-selected Git.
func (r diskWritebackRepo) git(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(r.ctx, containerGitTimeout)
	defer cancel()
	if err := checkContainerRoot(r.tree, r.root); err != nil {
		return nil, err
	}
	out := containerOutput{cancel: cancel}
	cmd := containerGit(ctx, r.tree, args...)
	cmd.Stdout, cmd.Stderr = out.stdoutWriter(), out.stderrWriter()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("reading write-back baseline (%s): %w", args[0], errors.Join(err, ctx.Err()))
	}
	if out.over {
		return nil, errors.New("write-back Git output limit exceeded")
	}
	if err := checkContainerRoot(r.tree, r.root); err != nil {
		return nil, err
	}
	return out.stdout.Bytes(), nil
}

func (r diskWritebackRepo) baselineEntry(rel string) (implementBaselineEntry, bool, error) {
	out, err := r.git("ls-tree", "-z", r.base, "--", ":(literal)"+rel)
	if err != nil {
		return implementBaselineEntry{}, false, err
	}
	entries, valid := implementTreeEntry(out, rel, r.hexLen, false)
	entry, exists := entries[rel]
	if !valid || len(entries) > 1 || len(entries) == 1 && !exists {
		return entry, false, errors.New("malformed write-back baseline listing")
	}
	return entry, exists, nil
}

func (r diskWritebackRepo) trackedAtBase(rel string) (bool, error) {
	_, exists, err := r.baselineEntry(rel)
	return exists, err
}

// Fstatat alone would follow intermediate symlinks. Open each parent first.
func (r diskWritebackRepo) fact(rel string, kind uint32) (bool, error) {
	fd, err := openWritebackParent(r.root, rel)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("opening fact parent: %w", err)
	}
	defer closeFD(fd)
	var st unix.Stat_t
	err = unix.Fstatat(fd, path.Base(rel), &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("examining repository fact: %w", err)
	}
	return uint32(st.Mode)&unix.S_IFMT == kind, nil
}
func (r diskWritebackRepo) regularFile(rel string) (bool, error) { return r.fact(rel, unix.S_IFREG) }
func (r diskWritebackRepo) directory(rel string) (bool, error)   { return r.fact(rel, unix.S_IFDIR) }

// These gates precede even the sweep, which itself calls Docker.
func prepareWritebackRepo(ctx context.Context, tree string) (*diskWritebackRepo, error) {
	if err := implementRealDirectory(tree, os.Lstat); err != nil {
		return nil, fmt.Errorf("checking write-back worktree: %w", err)
	}
	if reason := implementWorktreeSymlinks(tree, filepath.WalkDir, os.Lstat); reason != "" {
		return nil, errors.New(reason)
	}
	root, err := walk(tree)
	if err != nil {
		return nil, fmt.Errorf("opening write-back root: %w", err)
	}
	r := &diskWritebackRepo{ctx: ctx, tree: tree, root: root, hexLen: 40}
	if err := r.baseline(); err != nil {
		closeFD(root)
		return nil, err
	}
	return r, nil
}

func (r *diskWritebackRepo) baseline() error {
	out, err := r.git("rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	r.format = strings.TrimSuffix(string(out), "\n")
	if string(out) != r.format+"\n" || r.format != "sha1" && r.format != "sha256" {
		return errors.New("invalid Git object format")
	}
	if r.format == "sha256" {
		r.hexLen = 64
	}
	out, err = r.git("merge-base", "HEAD", "refs/remotes/origin/main")
	if err != nil {
		return err
	}
	if !implementMatch(fmt.Sprintf(`^[0-9a-f]{%d}\n$`, r.hexLen), string(out)) {
		return errors.New("invalid merge base with origin/main")
	}
	r.base = strings.TrimSuffix(string(out), "\n")
	entry, exists, err := r.baselineEntry("sqlc.yaml")
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("sqlc.yaml is absent at the merge base")
	}
	f, err := openContainerSource(r.root, "sqlc.yaml")
	if err != nil {
		return fmt.Errorf("opening sqlc.yaml: %w", err)
	}
	before, statErr := stampContainerSource(f)
	var data []byte
	if statErr == nil && before.info.Size() <= writebackFileLimit {
		data, err = io.ReadAll(io.LimitReader(f, writebackFileLimit+1))
	} else {
		err = errors.Join(statErr, errors.New("sqlc.yaml is not a bounded regular file"))
	}
	after, afterErr := stampContainerSource(f)
	closeErr := f.Close()
	if err := errors.Join(err, afterErr, closeErr); err != nil {
		return fmt.Errorf("reading sqlc.yaml: %w", err)
	}
	if len(data) > writebackFileLimit || !sameContainerSource(before, after) || implementBlobOID(data, r.format) != entry.oid {
		return errors.New("sqlc.yaml differs from the merge base")
	}
	return nil
}
