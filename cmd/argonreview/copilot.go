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

// One GraphQL response binds the PR metadata and review comments together.
// Refuse truncated connections rather than silently omit a finding or combine
// pages read at different heads.
const copilotQuery = `query($number:Int!) {
  repository(owner:"tkakkie",name:"ribbitto") {
    pullRequest(number:$number) {
      title body baseRefName baseRefOid headRefOid
      reviews(first:100) {
        pageInfo { hasNextPage }
        nodes {
          author { login } body url commit { oid }
          comments(first:100) {
            pageInfo { hasNextPage }
            nodes { author { login } body url path line originalLine diffHunk }
          }
        }
      }
    }
  }
}`

type copilotAuthor struct {
	Login string `json:"login"`
}

type copilotPageInfo struct {
	HasNextPage bool `json:"hasNextPage"`
}

type copilotComment struct {
	Author       copilotAuthor `json:"author"`
	Body         string        `json:"body"`
	URL          string        `json:"url"`
	Path         string        `json:"path"`
	Line         *int          `json:"line"`
	OriginalLine *int          `json:"originalLine"`
	DiffHunk     string        `json:"diffHunk"`
}

type copilotComments struct {
	Nodes    []copilotComment `json:"nodes"`
	PageInfo copilotPageInfo  `json:"pageInfo"`
}

type copilotReview struct {
	Author copilotAuthor `json:"author"`
	Body   string        `json:"body"`
	URL    string        `json:"url"`
	Commit struct {
		OID string `json:"oid"`
	} `json:"commit"`
	Comments copilotComments `json:"comments"`
}

type copilotSnapshot struct {
	target
	Reviews []copilotReview `json:"reviews"`
}

func readCopilot(number int) (copilotSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com", "graphql",
		"-f", "query="+copilotQuery, "-F", fmt.Sprintf("number=%d", number)).Output()
	if err != nil {
		return copilotSnapshot{}, fmt.Errorf("reading Copilot snapshot for #%d with gh: %w", number, err)
	}
	return parseCopilot(number, out)
}

func parseCopilot(number int, data []byte) (copilotSnapshot, error) {
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository struct {
				PullRequest *struct {
					target
					Reviews struct {
						Nodes    []copilotReview
						PageInfo copilotPageInfo
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return copilotSnapshot{}, fmt.Errorf("decoding Copilot snapshot: %w", err)
	}
	p := response.Data.Repository.PullRequest
	if len(response.Errors) != 0 || p == nil {
		return copilotSnapshot{}, errors.New("Copilot snapshot is unavailable or has GraphQL errors")
	}
	if p.Reviews.PageInfo.HasNextPage {
		return copilotSnapshot{}, errors.New("Copilot snapshot has truncated reviews")
	}
	snapshot := copilotSnapshot{target: p.target}
	snapshot.Number = number
	for _, review := range p.Reviews.Nodes {
		if review.Comments.PageInfo.HasNextPage {
			return copilotSnapshot{}, errors.New("Copilot snapshot has truncated inline comments")
		}
		if isCopilot(review.Author.Login) {
			snapshot.Reviews = append(snapshot.Reviews, review)
		}
	}
	if len(snapshot.Reviews) == 0 {
		return copilotSnapshot{}, errors.New("Copilot snapshot has no Copilot review")
	}
	return snapshot, nil
}

func isCopilot(login string) bool {
	return strings.EqualFold(login, "copilot-pull-request-reviewer") ||
		strings.EqualFold(login, "copilot-pull-request-reviewer[bot]") || strings.EqualFold(login, "Copilot")
}

func copilotHere(source, revision string, number int, approved string, out io.Writer) (int, error) {
	return here(revision, func() (*workspace, error) {
		snapshot, err := readCopilot(number)
		if err != nil {
			return nil, err
		}
		return copilotWorkspace(source, revision, ribbittoURL, approved, snapshot, workspaceGit)
	}, false, out)
}

func copilotWorkspace(source, revision, remote, approved string, snapshot copilotSnapshot, git func(string, ...string) (string, error)) (w *workspace, err error) {
	if !commitID.MatchString(approved) {
		return nil, errors.New("approved SHA must be a full commit ID")
	}
	w, err = newWorkspace(source, revision, remote, snapshot.target, git)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.remove())
			w = nil
		}
	}()
	// review accepts an older snapshot still on the PR. This check must
	// instead cover the current head fetched from the pinned PR ref.
	head, err := git(w.tree, "rev-parse", "--verify", "refs/argonreview/head^{commit}")
	if err != nil {
		return w, fmt.Errorf("resolving the current pull request head: %w", err)
	}
	if strings.TrimSpace(head) != snapshot.Head {
		return w, fmt.Errorf("pull request #%d: snapshot is not the current head", snapshot.Number)
	}
	if _, err := git(w.tree, "merge-base", "--is-ancestor", approved, snapshot.Head); err != nil {
		return w, fmt.Errorf("approved SHA %s is not an ancestor of head %s: %w", approved, snapshot.Head, err)
	}
	instructions, err := git(source, "cat-file", "blob", revision+":.github/prompts/copilot-check.md")
	if err != nil {
		return w, fmt.Errorf("reading the Copilot prompt at %s: %w", revision, err)
	}
	diff, err := git(w.tree, "diff", "--no-ext-diff", "--no-textconv", approved+".."+snapshot.Head)
	if err != nil {
		return w, fmt.Errorf("computing the Copilot follow-up diff: %w", err)
	}
	payload, err := json.Marshal(map[string]any{"pull_request": snapshot.Number, "title": snapshot.Title,
		"description": snapshot.Body, "approved_commit": approved, "head_commit": snapshot.Head,
		"reviews": snapshot.Reviews, "diff": diff})
	if err != nil {
		return w, fmt.Errorf("encoding Copilot snapshot: %w", err)
	}
	w.prompt = instructions + "\nUNTRUSTED_PAYLOAD_JSON: " + string(payload) + "\n"
	return w, nil
}
