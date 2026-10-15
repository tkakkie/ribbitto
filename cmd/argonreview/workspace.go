package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// target is one gh pr view snapshot of the pull request under review, so the
// prompt, the worktree and the diff all describe the same commits even if the
// pull request is pushed meanwhile (#1068).
type target struct {
	Number  int    `json:"-"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	BaseRef string `json:"baseRefName"`
	Base    string `json:"baseRefOid"`
	Head    string `json:"headRefOid"`
}

// readPullRequest takes the snapshot with gh. The host is named, so GH_HOST
// cannot point the review at another server.
func readPullRequest(number int) (target, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "pr", "view", fmt.Sprint(number), "-R", "github.com/tkakkie/ribbitto",
		"--json", "title,body,baseRefName,baseRefOid,headRefOid").Output()
	if err != nil {
		return target{}, fmt.Errorf("reading pull request #%d with gh: %w", number, err)
	}
	t := target{Number: number}
	if err := json.Unmarshal(out, &t); err != nil {
		return target{}, fmt.Errorf("reading pull request #%d: %w", number, err)
	}
	return t, nil
}

var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ribbittoURL is where pull requests are fetched from: pinned, never the
// source's origin, which a local url.*.insteadOf could redirect (Copilot on
// #1097).
const ribbittoURL = "https://github.com/tkakkie/ribbitto.git"

// workspace is a temporary repository holding the target's head, with the
// prompt a review sends. It is a repository of its own, not a worktree of the
// source checkout, so nothing in the source's configuration (hooks, insteadOf,
// external diff or textconv commands, filters, worktree configuration) applies
// to the pull request's tree, and nothing is added to the source (Copilot on
// #1097, the maintainer's choice). The source revision and the target stay
// apart, so a report can name both.
type workspace struct {
	source, revision string
	target           target
	name             string // what a report calls the target
	dir, tree        string
	prompt           string
}

// emptyWorkspace makes the temporary directory and its repository. No
// template, so no hooks; global and system configuration are off in gitRaw,
// so the repository's own configuration is all there is.
func emptyWorkspace(source, revision string, git func(string, ...string) (string, error)) (*workspace, error) {
	dir, err := os.MkdirTemp("", "argonreview-")
	if err != nil {
		return nil, fmt.Errorf("creating the workspace: %w", err)
	}
	w := &workspace{source: source, revision: revision, dir: dir, tree: filepath.Join(dir, "tree")}
	if _, err := git(dir, "init", "--quiet", "--template=", w.tree); err != nil {
		return w, fmt.Errorf("creating the workspace repository: %w", err)
	}
	return w, nil
}

// checkout refuses a tree with a symlink, before any checkout: a link could
// expose files outside the worktree (keys, tokens) even with writing and the
// shell denied. Then it checks head out and removes every .agents entry.
func (w *workspace) checkout(head string, git func(string, ...string) (string, error)) error {
	files, err := git(w.tree, "ls-tree", "-r", "--full-tree", head)
	if err != nil {
		return fmt.Errorf("listing %s: %w", head, err)
	}
	for _, line := range strings.Split(files, "\n") {
		if strings.HasPrefix(line, "120000 ") {
			return fmt.Errorf("refusing %s: it has a symlink: %s", w.name, line[strings.Index(line, "\t")+1:])
		}
	}
	if _, err := git(w.tree, "checkout", "--quiet", "--detach", head); err != nil {
		return fmt.Errorf("checking out %s: %w", head, err)
	}
	return removeAgents(w.tree)
}

// newWorkspace builds the workspace for t, fetched from remote, with the
// prompt from the source checkout's trusted revision. On failure it removes
// whatever it made.
func newWorkspace(source, revision, remote string, t target, git func(string, ...string) (string, error)) (w *workspace, err error) {
	if t.Number <= 0 || !commitID.MatchString(t.Base) || !commitID.MatchString(t.Head) {
		return nil, fmt.Errorf("pull request #%d: the snapshot has no valid base and head commits", t.Number)
	}
	// The base branch goes into a refspec; check-ref-format refuses ':', a
	// leading '-' and the other characters that could change its meaning.
	if _, err := git(os.TempDir(), "check-ref-format", "--branch", t.BaseRef); err != nil {
		return nil, fmt.Errorf("pull request #%d: invalid base branch %q: %w", t.Number, t.BaseRef, err)
	}
	w, err = emptyWorkspace(source, revision, git)
	defer func() {
		if err != nil && w != nil {
			err = errors.Join(err, w.remove())
			w = nil
		}
	}()
	if err != nil {
		return w, err
	}
	w.target, w.name = t, fmt.Sprintf("tkakkie/ribbitto#%d", t.Number)
	// Borrowing the source's objects saves fetching the history again; only
	// objects are read from there.
	objects, err := git(source, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return w, fmt.Errorf("finding the source checkout's objects: %w", err)
	}
	if err := os.WriteFile(filepath.Join(w.tree, ".git/objects/info/alternates"), []byte(objects), 0o600); err != nil {
		return w, fmt.Errorf("sharing the source checkout's objects: %w", err)
	}
	if _, err := git(w.tree, "fetch", "--quiet", "--no-tags", remote,
		"+refs/heads/"+t.BaseRef+":refs/argonreview/base", fmt.Sprintf("+refs/pull/%d/head:refs/argonreview/head", t.Number)); err != nil {
		return w, fmt.Errorf("fetching pull request #%d: %w", t.Number, err)
	}
	if _, err := git(w.tree, "merge-base", "--is-ancestor", t.Head, "refs/argonreview/head"); err != nil {
		return w, fmt.Errorf("%s is not on pull request #%d: %w", t.Head, t.Number, err)
	}
	if _, err := git(w.tree, "cat-file", "-e", t.Base+"^{commit}"); err != nil {
		return w, fmt.Errorf("base %s of pull request #%d is not available: %w", t.Base, t.Number, err)
	}
	// From the trusted revision, never from the pull request, so a pull
	// request cannot rewrite its own review instructions. cat-file applies no
	// filter or textconv.
	instructions, err := git(source, "cat-file", "blob", revision+":.github/prompts/adversarial.md")
	if err != nil {
		return w, fmt.Errorf("reading the prompt at %s: %w", revision, err)
	}
	if err := w.checkout(t.Head, git); err != nil {
		return w, err
	}
	// The workspace configures no external diff or textconv; the options
	// keep it so, as in .github/prompts/audit.md's read-only git forms.
	diff, err := git(w.tree, "diff", "--no-ext-diff", "--no-textconv", t.Base+"..."+t.Head)
	if err != nil {
		return w, fmt.Errorf("computing the diff of pull request #%d: %w", t.Number, err)
	}
	// Everything the pull request controls is one JSON object on the last
	// line: escaping means no title, description or diff can end the payload
	// early or add text after it. The diff is the original one, so .agents/
	// changes stay visible to the reviewer.
	payload, err := json.Marshal(map[string]any{"pull_request": t.Number, "base_commit": t.Base, "head_commit": t.Head,
		"title": t.Title, "description": t.Body, "diff": diff})
	if err != nil {
		return w, fmt.Errorf("encoding pull request #%d: %w", t.Number, err)
	}
	w.prompt = instructions + "\nUNTRUSTED_PAYLOAD_JSON: " + string(payload) + "\n"
	return w, nil
}

// probeWorkspace builds a workspace from the HEAD of repo, a local throwaway
// repository, with the trusted prompt written for the probe (#1071). It
// takes the same checkout path as a review.
func probeWorkspace(source, revision, repo, promptFile string, git func(string, ...string) (string, error)) (w *workspace, err error) {
	instructions, err := os.ReadFile(promptFile)
	if err != nil {
		return nil, fmt.Errorf("reading the probe prompt: %w", err)
	}
	w, err = emptyWorkspace(source, revision, git)
	defer func() {
		if err != nil && w != nil {
			err = errors.Join(err, w.remove())
			w = nil
		}
	}()
	if err != nil {
		return w, err
	}
	w.name, w.prompt = repo, string(instructions)
	if _, err := git(w.tree, "fetch", "--quiet", "--no-tags", repo, "+HEAD:refs/argonreview/head"); err != nil {
		return w, fmt.Errorf("fetching %s: %w", repo, err)
	}
	head, err := git(w.tree, "rev-parse", "--verify", "refs/argonreview/head^{commit}")
	if err != nil {
		return w, fmt.Errorf("resolving the HEAD of %s: %w", repo, err)
	}
	w.target.Head = strings.TrimSpace(head)
	if err := w.checkout(w.target.Head, git); err != nil {
		return w, &refused{repo, w.target.Head, err}
	}
	return w, nil
}

// refused is a probe target refused before launch, such as a tree with a
// symlink, with what a probe report names.
type refused struct {
	name, head string
	err        error
}

func (r *refused) Error() string { return r.err.Error() }
func (r *refused) Unwrap() error { return r.err }

// entry is what the record keeps of a path: its type, inode, size and change
// time, and a regular file's size and content hash.
type entry struct {
	meta string
	size int64
	sum  [sha256.Size]byte
}

// metadata is an entry's type, inode, size and change time, with its size
// and file type.
func metadata(path string) (string, int64, uint32, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return "", 0, 0, err
	}
	return fmt.Sprintf("%o %d %d %d.%09d", st.Mode, st.Ino, st.Size, st.Ctim.Sec, st.Ctim.Nsec), st.Size, uint32(st.Mode) & unix.S_IFMT, nil
}

// hashUpTo hashes at most limit+1 bytes of a regular file, without following
// a link or blocking on a FIFO swapped in. The extra byte makes a file that
// grew past its recorded size hash differently.
func hashUpTo(path string, limit int64) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.CopyN(h, f, limit+1); err != nil && !errors.Is(err, io.EOF) {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// record keeps every entry of the sanitised tree, tracked, untracked and
// ignored alike, before agy starts (#1071). Content is hashed as well as
// metadata: a change time can stay put within a coarse timestamp (Codex on
// #1117).
func record(tree string) (map[string]entry, error) {
	entries := map[string]entry{}
	err := filepath.WalkDir(tree, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		meta, size, kind, err := metadata(path)
		if err != nil {
			return err
		}
		e := entry{meta: meta, size: size}
		if kind == unix.S_IFREG {
			if e.sum, err = hashUpTo(path, size); err != nil {
				return err
			}
		}
		entries[path] = e
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recording the worktree: %w", err)
	}
	return entries, nil
}

// verify compares the tree with its record while walking it and names the
// first difference, or returns "". Nothing outside the record is kept or
// read: directories are listed a chunk at a time and an unknown name ends the
// walk at once, and a file is read only up to its recorded size, so memory
// and time stay within the record's own size whatever a run left behind
// (Codex on #1117).
func verify(tree string, recorded map[string]entry) (string, error) {
	return verifyWith(tree, recorded, listNames)
}

// listNames reads a directory's next names, a chunk at a time.
func listNames(dir *os.File) ([]string, error) { return dir.Readdirnames(256) }

// verifyWith is verify with the directory listing passed in, so a test can
// count what it reads.
func verifyWith(tree string, recorded map[string]entry, list func(*os.File) ([]string, error)) (string, error) {
	seen := 0
	var walk func(path string) (string, error)
	walk = func(path string) (string, error) {
		want, ok := recorded[path]
		if !ok {
			return path, nil
		}
		meta, _, kind, err := metadata(path)
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if meta != want.meta {
			return path, nil
		}
		seen++
		if kind == unix.S_IFREG {
			// Bounded by the recorded size, never by what is there now.
			sum, err := hashUpTo(path, want.size)
			if err != nil {
				return "", err
			}
			if sum != want.sum {
				return path, nil
			}
			return "", nil
		}
		if kind != unix.S_IFDIR {
			return "", nil
		}
		dir, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer func() { _ = dir.Close() }()
		for {
			names, err := list(dir)
			for _, name := range names {
				if changed, walkErr := walk(filepath.Join(path, name)); changed != "" || walkErr != nil {
					return changed, walkErr
				}
			}
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
		}
	}
	changed, err := walk(tree)
	if changed != "" || err != nil {
		return changed, err
	}
	if seen != len(recorded) {
		for path := range recorded {
			_, err := os.Lstat(path)
			if errors.Is(err, fs.ErrNotExist) {
				return path, nil
			}
			if err != nil {
				return "", err
			}
		}
		return tree, nil
	}
	return "", nil
}

// removeAgents deletes every .agents entry in the worktree, nested ones,
// files and any letter case included, and checks that none is left: a pull
// request's .agents/hooks.json would run shell commands outside the sandbox
// (#1041), and on a case-insensitive filesystem .AGENTS is .agents (Codex on
// #1097).
func removeAgents(tree string) error {
	for _, remove := range []bool{true, false} {
		err := filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !strings.EqualFold(d.Name(), ".agents") {
				return err
			}
			if !remove {
				return fmt.Errorf("%s is still there", path)
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("removing .agents from the worktree: %w", err)
		}
	}
	return nil
}

// remove deletes the workspace. Nothing of it is in the source checkout.
func (w *workspace) remove() error {
	if err := os.RemoveAll(w.dir); err != nil {
		return fmt.Errorf("removing %s: %w", w.dir, err)
	}
	return nil
}

// printPrompt builds the workspace for pull request number, removes it again,
// and only then writes the prompt a review would send: a closed stdout can end
// the process on SIGPIPE during the write (Codex on #1097).
func printPrompt(source, revision, remote string, number int, read func(int) (target, error), git func(string, ...string) (string, error), out io.Writer) error {
	t, err := read(number)
	if err != nil {
		return err
	}
	w, err := newWorkspace(source, revision, remote, t, git)
	if err != nil {
		return err
	}
	if err := w.remove(); err != nil {
		return err
	}
	_, err = io.WriteString(out, w.prompt)
	return err
}
