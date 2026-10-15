package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// sharedSettings is the account's own Antigravity settings file. A review
// never loads or changes it; it is read only to hash (#1041).
const sharedSettings = ".gemini/antigravity-cli/settings.json"

// review is one Argon review of a pull request (#1096).
type review struct {
	source, revision, remote string
	number                   int
	home, shared, agy        string
	prepare                  func() error
	read                     func(int) (target, error)
	git                      func(string, ...string) (string, error)
	sup                      supervisor
}

// reviewHere reviews pull request number with this machine's configuration.
// A failing agy's status is kept, as in check: 124, 130 or 143 when the time
// limit or a signal ended it; any other failure is 1.
func reviewHere(source, revision string, number int, out io.Writer) (int, error) {
	limit, err := timeLimit()
	if err != nil {
		return 1, err
	}
	agy, err := exec.LookPath("agy")
	if err != nil {
		return 1, fmt.Errorf("finding agy: %w", err)
	}
	account, err := user.Current()
	if err != nil {
		return 1, fmt.Errorf("finding the account's home: %w", err)
	}
	return withSignals(func(signals <-chan os.Signal) (int, error) {
		return review{
			source: source, revision: revision, remote: ribbittoURL, number: number,
			home: os.Getenv("RIBBITTO_ARGON_HOME"), shared: filepath.Join(account.HomeDir, sharedSettings), agy: agy,
			prepare: func() error { _, err := prepareHere(); return err },
			read:    readPullRequest,
			git:     func(dir string, args ...string) (string, error) { return gitRaw(2*time.Minute, dir, args...) },
			sup:     supervisor{limit, 5 * time.Second, signals, systemCleanup()},
		}.run(out)
	})
}

// run prepares the configuration, builds the workspace, runs agy there and
// writes the report only if the run counts. The workspace is removed before
// anything is written, so a closed stdout cannot skip it.
func (r review) run(out io.Writer) (code int, err error) {
	if err := r.prepare(); err != nil {
		return 1, err
	}
	before, err := hashFile(r.shared)
	if err != nil {
		return 1, err
	}
	t, err := r.read(r.number)
	if err != nil {
		return 1, err
	}
	w, err := newWorkspace(r.source, r.revision, r.remote, t, r.git)
	if err != nil {
		return 1, err
	}
	code, response, err := r.launch(w)
	if after, hashErr := hashFile(r.shared); hashErr != nil || after != before {
		err = errors.Join(err, hashErr, errors.New("the shared Antigravity settings changed during the run"))
	}
	if removeErr := w.remove(); removeErr != nil {
		err = errors.Join(err, removeErr)
	}
	if err != nil {
		return max(code, 1), err
	}
	// The source revision and the target are named apart (#1068).
	_, err = fmt.Fprintf(out, "Argon review of tkakkie/ribbitto#%d at %s\nLauncher built from %s\n\n%s", t.Number, t.Head, r.revision, response)
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// launch runs agy on the workspace under the supervisor and judges its
// output, kept in the workspace directory.
func (r review) launch(w *workspace) (int, string, error) {
	stdout, err := os.Create(filepath.Join(w.dir, "agy.jsonl"))
	if err != nil {
		return 1, "", fmt.Errorf("creating agy's output: %w", err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Create(filepath.Join(w.dir, "agy.err"))
	if err != nil {
		return 1, "", fmt.Errorf("creating agy's error output: %w", err)
	}
	defer func() { _ = stderr.Close() }()
	// agy's own limit ends the turn before the supervisor's does.
	printLimit := r.sup.limit - 2*time.Minute
	if printLimit <= 0 {
		printLimit = r.sup.limit / 2
	}
	args := []string{"-p", w.prompt, "--model", argonModel, "--sandbox",
		"--print-timeout", printLimit.String(), "--output-format", "stream-json"}
	code, err := r.sup.run(r.agy, args, withHome(r.home), w.tree, stdout, stderr)
	if err != nil {
		return max(code, 1), "", err
	}
	transcript, err := os.ReadFile(stdout.Name())
	if err != nil {
		return 1, "", fmt.Errorf("reading agy's output: %w", err)
	}
	diagnostics, err := os.ReadFile(stderr.Name())
	if err != nil {
		return 1, "", fmt.Errorf("reading agy's error output: %w", err)
	}
	response, err := judge(code, transcript, diagnostics)
	return code, response, err
}

// judge applies the failure rules (#1041, #1096): a non-zero exit or the time
// limit, AGY_ERROR on stderr, a transcript that is malformed or incomplete, a
// result that cannot be parsed, has no response, is not a success or lists
// denied_actions, and an empty response. A denial ends a headless run with
// exit 0 and an empty response, so none of these may pass as a review.
func judge(code int, transcript, stderr []byte) (string, error) {
	var reasons []string
	if code != 0 {
		reasons = append(reasons, fmt.Sprintf("agy exited with status %d", code))
	}
	if bytes.Contains(stderr, []byte("AGY_ERROR")) {
		reasons = append(reasons, "agy reported AGY_ERROR")
	}
	response, err := parseTranscript(transcript)
	if err != nil {
		reasons = append(reasons, err.Error())
	} else if strings.TrimSpace(response) == "" {
		reasons = append(reasons, "the response is empty")
	}
	if len(reasons) > 0 {
		return "", fmt.Errorf("the run does not count: %s", strings.Join(reasons, "; "))
	}
	return response, nil
}

// parseTranscript reads agy's stream-json output: one JSON event per line,
// the last one the only result.
func parseTranscript(transcript []byte) (string, error) {
	if len(transcript) == 0 || !bytes.HasSuffix(transcript, []byte("\n")) {
		return "", errors.New("the output is empty or cut off")
	}
	lines := strings.Split(strings.TrimSuffix(string(transcript), "\n"), "\n")
	var result json.RawMessage
	for i, line := range lines {
		var event struct {
			Event  string          `json:"event"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Event == "" {
			return "", fmt.Errorf("line %d is not a stream-json event", i+1)
		}
		if event.Event == "result" {
			if i != len(lines)-1 {
				return "", errors.New("the result is not the last event")
			}
			result = event.Result
		}
	}
	if result == nil {
		return "", errors.New("there is no result event")
	}
	var r struct {
		Status   string          `json:"status"`
		Response *string         `json:"response"`
		Denied   json.RawMessage `json:"denied_actions"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return "", fmt.Errorf("the result cannot be parsed: %w", err)
	}
	switch {
	case len(r.Denied) > 0 && string(r.Denied) != "null":
		return "", fmt.Errorf("the result lists denied_actions %s", r.Denied)
	case r.Response == nil:
		return "", errors.New("the result has no response")
	case r.Status != "SUCCESS":
		return "", fmt.Errorf("the result's status is %q", r.Status)
	}
	return *r.Response, nil
}

// hashFile returns the SHA-256 of path's content, or "absent".
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
