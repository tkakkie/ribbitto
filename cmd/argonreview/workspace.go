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
	dir, tree        string
	prompt           string
}

// newWorkspace builds the workspace for t, fetched from remote, with the
// prompt from the source checkout's trusted revision. On failure it removes
// whatever it made.
func newWorkspace(source, revision, remote string, t target, git func(string, ...string) (string, error)) (w *workspace, err error) {
	if t.Number <= 0 || !commitID.MatchString(t.Base) || !commitID.MatchString(t.Head) {
		return nil, fmt.Errorf("pull request #%d: the snapshot has no valid base and head commits", t.Number)
	}
	dir, err := os.MkdirTemp("", "argonreview-")
	if err != nil {
		return nil, fmt.Errorf("creating the workspace: %w", err)
	}
	w = &workspace{source: source, revision: revision, target: t, dir: dir, tree: filepath.Join(dir, "tree")}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.remove())
			w = nil
		}
	}()
	// The base branch goes into a refspec; check-ref-format refuses ':', a
	// leading '-' and the other characters that could change its meaning.
	if _, err := git(dir, "check-ref-format", "--branch", t.BaseRef); err != nil {
		return w, fmt.Errorf("pull request #%d: invalid base branch %q: %w", t.Number, t.BaseRef, err)
	}
	// No template, so no hooks; global and system configuration are off in
	// gitRaw, so the repository's own configuration is all there is.
	if _, err := git(dir, "init", "--quiet", "--template=", w.tree); err != nil {
		return w, fmt.Errorf("creating the workspace repository: %w", err)
	}
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
	// Refused before any checkout: a link could expose files outside the
	// worktree (keys, tokens) even with writing and the shell denied.
	files, err := git(w.tree, "ls-tree", "-r", "--full-tree", t.Head)
	if err != nil {
		return w, fmt.Errorf("listing %s: %w", t.Head, err)
	}
	for _, line := range strings.Split(files, "\n") {
		if strings.HasPrefix(line, "120000 ") {
			return w, fmt.Errorf("refusing pull request #%d: it has a symlink: %s", t.Number, line[strings.Index(line, "\t")+1:])
		}
	}
	// From the trusted revision, never from the pull request, so a pull
	// request cannot rewrite its own review instructions. cat-file applies no
	// filter or textconv.
	instructions, err := git(source, "cat-file", "blob", revision+":.github/prompts/adversarial.md")
	if err != nil {
		return w, fmt.Errorf("reading the prompt at %s: %w", revision, err)
	}
	if _, err := git(w.tree, "checkout", "--quiet", "--detach", t.Head); err != nil {
		return w, fmt.Errorf("checking out %s: %w", t.Head, err)
	}
	if err := removeAgents(w.tree); err != nil {
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
