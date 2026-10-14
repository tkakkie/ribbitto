// Command unchanged runs a command and fails if it changed the worktree's
// uncommitted state, so changes already uncommitted do not fail it. make
// prepush runs make generate css through it (#848): the generators must leave
// committed output as it is. The state is the tracked diff, the list of
// untracked files that are not ignored, and each such file's content. A
// fingerprint that cannot be taken, such as an unreadable untracked file,
// fails the run rather than dropping out of the comparison (Grok on #855).
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: unchanged <repository root> <command> [argument...]")
		os.Exit(2)
	}
	root, name, args := os.Args[1], os.Args[2], os.Args[3:]
	err := check(func() (string, error) { return fingerprint(root) }, func() error {
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = root, os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	})
	var changed errChanged
	if errors.As(err, &changed) {
		fmt.Printf("%s changed files; commit them:\n", join(name, args))
		status := exec.Command("git", "status", "--short")
		status.Dir, status.Stdout, status.Stderr = root, os.Stdout, os.Stderr
		_ = status.Run()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "unchanged:", err)
		os.Exit(1)
	}
}

type errChanged struct{}

func (errChanged) Error() string { return "the command changed files" }

// check takes a fingerprint before and after run; each one's own error is
// checked, so a failed fingerprint cannot pass as an unchanged tree.
func check(fp func() (string, error), run func() error) error {
	before, err := fp()
	if err != nil {
		return fmt.Errorf("fingerprint before: %w", err)
	}
	if err := run(); err != nil {
		return fmt.Errorf("running the command: %w", err)
	}
	after, err := fp()
	if err != nil {
		return fmt.Errorf("fingerprint after: %w", err)
	}
	if after != before {
		return errChanged{}
	}
	return nil
}

// fingerprint hashes the unstaged and the staged diff, so a command that
// edits and stages a tracked file is caught (Copilot on #1024), then the
// NUL-separated list of untracked files, then each of those files. Names stay NUL-separated and are
// never parsed by a shell, so quoted names and a file named "-" count too.
func fingerprint(root string) (string, error) {
	h := sha256.New()
	diff, err := git(root, "diff", "--binary", "--no-ext-diff")
	if err != nil {
		return "", err
	}
	h.Write(diff)
	staged, err := git(root, "diff", "--cached", "--binary", "--no-ext-diff")
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "\x00staged %d\x00", len(staged))
	h.Write(staged)
	list, err := git(root, "ls-files", "-z", "-o", "--exclude-standard")
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "\x00untracked %d\x00", len(list))
	h.Write(list)
	for _, name := range bytes.Split(bytes.TrimSuffix(list, []byte{0}), []byte{0}) {
		if len(name) == 0 {
			continue
		}
		if err := hashFile(h, filepath.Join(root, string(name))); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// hashFile hashes a symlink as its target path, as Git stores it, so a change
// outside the worktree behind the link changes nothing and a link to a
// directory can be hashed (Copilot on #1024).
func hashFile(h io.Writer, path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("reading untracked symlink: %w", err)
		}
		fmt.Fprintf(h, "\x00link %d\x00%s", len(target), target)
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reading untracked file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("reading untracked file: %w", err)
	}
	// The size separates one file's content from the next file's.
	fmt.Fprintf(h, "\x00file %d\x00", info.Size())
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("reading untracked file: %w", err)
	}
	return nil
}

func git(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %v: %w: %s", args, err, stderr.String())
	}
	return out, nil
}

func join(name string, args []string) string {
	s := name
	for _, a := range args {
		s += " " + a
	}
	return s
}
