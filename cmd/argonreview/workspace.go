package main

import (
	"context"
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
)

// target is one gh pr view snapshot of the pull request under review, so the
// prompt, the worktree and the diff all describe the same commits even if the
// pull request is pushed meanwhile (#1068, as in muse-review.sh).
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

// workspace is a temporary detached worktree of the target's head in the
// source checkout, with the prompt a review sends. The source revision and
// the target stay apart, so a report can name both.
type workspace struct {
	source, revision string
	target           target
	git              func(dir string, args ...string) (string, error)
	dir, tree, refs  string
	prompt           string
}

// newWorkspace builds the workspace for t in the source checkout, whose
// trusted revision gives the prompt. On failure it removes whatever it made.
func newWorkspace(source, revision string, t target, git func(string, ...string) (string, error)) (w *workspace, err error) {
	if t.Number <= 0 || !commitID.MatchString(t.Base) || !commitID.MatchString(t.Head) {
		return nil, fmt.Errorf("pull request #%d: the snapshot has no valid base and head commits", t.Number)
	}
	// The base branch goes into a refspec; check-ref-format refuses ':', a
	// leading '-' and the other characters that could change its meaning.
	if _, err := git(source, "check-ref-format", "--branch", t.BaseRef); err != nil {
		return nil, fmt.Errorf("pull request #%d: invalid base branch %q", t.Number, t.BaseRef)
	}
	dir, err := os.MkdirTemp("", "argonreview-")
	if err != nil {
		return nil, fmt.Errorf("creating the workspace: %w", err)
	}
	w = &workspace{source: source, revision: revision, target: t, git: git, dir: dir,
		tree: filepath.Join(dir, "tree"), refs: "refs/argonreview/" + filepath.Base(dir)}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.remove())
			w = nil
		}
	}()
	// Fetched into refs of this run only: --refmap= keeps origin/* (and so
	// the origin/main the build was verified against) where it was.
	if _, err := git(source, "fetch", "--quiet", "--no-tags", "--refmap=", "origin",
		"+refs/heads/"+t.BaseRef+":"+w.refs+"/base", fmt.Sprintf("+refs/pull/%d/head:%s/head", t.Number, w.refs)); err != nil {
		return w, fmt.Errorf("fetching pull request #%d: %w", t.Number, err)
	}
	if _, err := git(source, "merge-base", "--is-ancestor", t.Head, w.refs+"/head"); err != nil {
		return w, fmt.Errorf("%s is not on pull request #%d: %w", t.Head, t.Number, err)
	}
	if _, err := git(source, "cat-file", "-e", t.Base+"^{commit}"); err != nil {
		return w, fmt.Errorf("base %s of pull request #%d is not available: %w", t.Base, t.Number, err)
	}
	// Refused before any checkout: a link could expose files outside the
	// worktree (keys, tokens) even with writing and the shell denied.
	files, err := git(source, "ls-tree", "-r", "--full-tree", t.Head)
	if err != nil {
		return w, fmt.Errorf("listing %s: %w", t.Head, err)
	}
	for _, line := range strings.Split(files, "\n") {
		if strings.HasPrefix(line, "120000 ") {
			return w, fmt.Errorf("refusing pull request #%d: it has a symlink: %s", t.Number, line[strings.Index(line, "\t")+1:])
		}
	}
	// From the trusted revision, never from the pull request, so a pull
	// request cannot rewrite its own review instructions.
	instructions, err := git(source, "show", revision+":.github/prompts/adversarial.md")
	if err != nil {
		return w, fmt.Errorf("reading the prompt at %s: %w", revision, err)
	}
	if _, err := git(source, "worktree", "add", "--quiet", "--detach", w.tree, t.Head); err != nil {
		return w, fmt.Errorf("checking out %s: %w", t.Head, err)
	}
	if err := removeAgents(w.tree); err != nil {
		return w, err
	}
	diff, err := git(source, "diff", t.Base+"..."+t.Head)
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

// removeAgents deletes every .agents entry in the worktree, nested ones
// included, and checks that none is left: a pull request's .agents/hooks.json
// would run shell commands outside the sandbox (#1041).
func removeAgents(tree string) error {
	for _, remove := range []bool{true, false} {
		err := filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.Name() != ".agents" {
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

// remove deletes the worktree, its refs and the temporary directory, and
// reports anything it could not remove.
func (w *workspace) remove() error {
	var errs []error
	if _, err := os.Stat(w.tree); err == nil {
		if _, err := w.git(w.source, "worktree", "remove", "--force", w.tree); err != nil {
			errs = append(errs, fmt.Errorf("removing the worktree %s: %w", w.tree, err))
		}
	}
	if _, err := w.git(w.source, "worktree", "prune"); err != nil {
		errs = append(errs, fmt.Errorf("pruning worktrees: %w", err))
	}
	for _, ref := range []string{w.refs + "/base", w.refs + "/head"} {
		// Absent when the fetch failed; deleting a missing ref succeeds.
		if _, err := w.git(w.source, "update-ref", "-d", ref); err != nil {
			errs = append(errs, fmt.Errorf("deleting %s: %w", ref, err))
		}
	}
	if err := os.RemoveAll(w.dir); err != nil {
		errs = append(errs, fmt.Errorf("removing %s: %w", w.dir, err))
	}
	return errors.Join(errs...)
}

// printPrompt builds the workspace for pull request number, writes the prompt
// a review would send, and removes the workspace again.
func printPrompt(source, revision string, number int, read func(int) (target, error), git func(string, ...string) (string, error), out io.Writer) error {
	t, err := read(number)
	if err != nil {
		return err
	}
	w, err := newWorkspace(source, revision, t, git)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, w.prompt)
	return errors.Join(err, w.remove())
}
