package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// issueSnapshot keeps the title, body and comments from a single gh read.
// Raw comments preserve their metadata while remaining inside the JSON payload.
type issueSnapshot struct {
	Number   int               `json:"number"`
	Title    string            `json:"title"`
	Body     string            `json:"body"`
	Comments []json.RawMessage `json:"comments"`
}

func readIssue(number int) (issueSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "issue", "view", fmt.Sprint(number), "-R", "github.com/tkakkie/ribbitto",
		"--json", "title,body,comments").Output()
	if err != nil {
		return issueSnapshot{}, fmt.Errorf("reading issue #%d with gh: %w", number, err)
	}
	t := issueSnapshot{Number: number}
	if err := json.Unmarshal(out, &t); err != nil {
		return issueSnapshot{}, fmt.Errorf("reading issue #%d: %w", number, err)
	}
	return t, nil
}

func issueHere(source, revision string, number, round int, out io.Writer) (int, error) {
	return here(revision, func() (*workspace, error) {
		t, err := readIssue(number)
		if err != nil {
			return nil, err
		}
		return issueWorkspace(source, revision, t, round, workspaceGit)
	}, false, out)
}

// issueWorkspace checks out the trusted revision, never a pull request or the
// source's working tree. Checkout uses the same symlink and .agents checks.
func issueWorkspace(source, revision string, t issueSnapshot, round int, git func(string, ...string) (string, error)) (w *workspace, err error) {
	if t.Number <= 0 || !commitID.MatchString(revision) || (round != 1 && round != 2) {
		return nil, errors.New("invalid issue number, trusted revision or review round")
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
	w.name, w.target.Head = fmt.Sprintf("issue tkakkie/ribbitto#%d", t.Number), revision
	if err := w.shareObjects(git); err != nil {
		return w, err
	}
	instructions, err := git(source, "cat-file", "blob", revision+":.github/prompts/issue-review.md")
	if err != nil {
		return w, fmt.Errorf("reading the issue prompt at %s: %w", revision, err)
	}
	if err := w.checkout(revision, git); err != nil {
		return w, err
	}
	// Marshal the entire snapshot so embedded newlines or prompt markers in
	// any issue field cannot escape the one untrusted payload line.
	payload, err := json.Marshal(t)
	if err != nil {
		return w, fmt.Errorf("encoding issue #%d: %w", t.Number, err)
	}
	w.prompt = strings.ReplaceAll(instructions, "{{ROUND}}", fmt.Sprint(round)) + "\nUNTRUSTED_PAYLOAD_JSON: " + string(payload) + "\n"
	return w, nil
}
