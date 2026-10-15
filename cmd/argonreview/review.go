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
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// sharedSettings is the account's own Antigravity settings file. A review
// never loads or changes it; it is read only to hash (#1041).
const sharedSettings = ".gemini/antigravity-cli/settings.json"

// review is one Argon run: a review of a pull request (#1096) or a probe of
// a throwaway repository (#1071), which differ only in what build makes.
type review struct {
	revision          string
	home, shared, agy string
	prepare           func() error
	build             func() (*workspace, error)
	sup               supervisor
	// stop ends the catching of INT and TERM before the report is written;
	// runCatching sets it.
	stop func()
	// probe prints agy's output and the verdict whether the run counts or
	// not, since a probe expects a denial.
	probe bool
}

// here is a run with this machine's configuration: the time limit, agy on
// PATH, the dedicated HOME and the account's shared settings.
func here(revision string, build func() (*workspace, error), probe bool, out io.Writer) (int, error) {
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
	return review{
		revision: revision, home: os.Getenv("RIBBITTO_ARGON_HOME"), shared: filepath.Join(account.HomeDir, sharedSettings), agy: agy,
		prepare: func() error { _, err := prepareHere(); return err },
		build:   build,
		sup:     supervisor{limit, 5 * time.Second, nil, systemCleanup()},
		probe:   probe,
	}.runCatching(out)
}

// workspaceGit is git for workspace builds: two minutes, untrimmed output.
func workspaceGit(dir string, args ...string) (string, error) {
	return gitRaw(2*time.Minute, dir, args...)
}

// reviewHere reviews pull request number. A failing agy's status is kept, as
// in check: 124, 130 or 143 when the time limit or a signal ended it; any
// other failure is 1.
func reviewHere(source, revision string, number int, out io.Writer) (int, error) {
	return here(revision, func() (*workspace, error) {
		t, err := readPullRequest(number)
		if err != nil {
			return nil, err
		}
		return newWorkspace(source, revision, ribbittoURL, t, workspaceGit)
	}, false, out)
}

// probeHere runs the review pipeline on the HEAD of repo with a trusted
// prompt file, and prints what agy did (#1071).
func probeHere(source, revision, repo, promptFile string, out io.Writer) (int, error) {
	return here(revision, func() (*workspace, error) {
		return probeWorkspace(source, revision, repo, promptFile, workspaceGit)
	}, true, out)
}

// runCatching runs r with INT and TERM handed to its supervisor, and back to
// their default, ending this process, once the report is about to be written.
func (r review) runCatching(out io.Writer) (int, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	r.sup.signals = signals
	r.stop = func() { signal.Stop(signals) }
	return r.run(out)
}

// run prepares the configuration, builds the workspace, runs agy there and
// writes the report only if the run counts; a probe writes agy's output and
// the verdict either way. The workspace is removed before anything is
// written, so a closed stdout cannot skip it.
func (r review) run(out io.Writer) (code int, err error) {
	if err := r.prepare(); err != nil {
		return 1, err
	}
	before, err := hashFile(r.shared)
	if err != nil {
		return 1, err
	}
	w, err := r.build()
	var ref *refused
	if err != nil && r.probe && errors.As(err, &ref) {
		// A probe records a refusal before launch with the target's
		// identity, apart from tool denials (#1071, Codex on #1117).
		return r.finish(out, ref.name, ref.head, 1, nil, nil, "", err)
	}
	if err != nil {
		return 1, err
	}
	// The sanitised tree as agy gets it; any change during the run fails it,
	// including a recreated .agents (#1071).
	baseline, err := record(w.tree)
	if err != nil {
		return 1, errors.Join(err, w.remove())
	}
	code, transcript, diagnostics, err := r.launch(w)
	var response string
	if err == nil {
		response, err = judge(code, transcript, diagnostics)
	}
	if after, recordErr := record(w.tree); recordErr != nil {
		err = errors.Join(err, recordErr)
	} else if path := changed(baseline, after); path != "" {
		err = errors.Join(err, fmt.Errorf("the run changed the worktree: %s", strings.TrimPrefix(path, w.tree+"/")))
	}
	if after, hashErr := hashFile(r.shared); hashErr != nil || after != before {
		err = errors.Join(err, hashErr, errors.New("the shared Antigravity settings changed during the run"))
	}
	if removeErr := w.remove(); removeErr != nil {
		err = errors.Join(err, removeErr)
	}
	return r.finish(out, w.name, w.target.Head, code, transcript, diagnostics, response, err)
}

// finish writes what the run produced, after the workspace is gone.
func (r review) finish(out io.Writer, name, head string, code int, transcript, diagnostics []byte, response string, err error) (int, error) {
	// The supervisor only reads signals while agy runs. Catching stops here,
	// so one arriving during the write ends the process as usual; one that
	// arrived before, during the supervisor's cleanup or the checks above,
	// ends the review without a report (Codex on #1113).
	if r.stop != nil {
		r.stop()
	}
	select {
	case sig := <-r.sup.signals:
		return 128 + int(sig.(syscall.Signal)), errors.Join(err, fmt.Errorf("interrupted by %v", sig))
	default:
	}
	// The source revision and the target are named apart (#1068).
	header := fmt.Sprintf("Argon %s of %s at %s\nLauncher built from %s\n\n", map[bool]string{false: "review", true: "probe"}[r.probe], name, head, r.revision)
	if r.probe {
		verdict := "counts"
		var ref *refused
		switch {
		case errors.As(err, &ref):
			verdict = "refused before launch: " + err.Error()
		case err != nil:
			verdict = "does not count: " + err.Error()
		}
		if _, writeErr := fmt.Fprintf(out, "%sVerdict: %s\n\n--- agy stdout\n%s--- agy stderr\n%s", header, verdict, transcript, diagnostics); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}
	if err != nil {
		return max(code, 1), err
	}
	if !r.probe {
		if _, err := io.WriteString(out, header+response); err != nil {
			return 1, err
		}
	}
	return 0, nil
}

// launch runs agy on the workspace under the supervisor, with its output kept
// in the workspace directory, and returns its status and output.
func (r review) launch(w *workspace) (int, []byte, []byte, error) {
	stdout, err := os.Create(filepath.Join(w.dir, "agy.jsonl"))
	if err != nil {
		return 1, nil, nil, fmt.Errorf("creating agy's output: %w", err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Create(filepath.Join(w.dir, "agy.err"))
	if err != nil {
		return 1, nil, nil, fmt.Errorf("creating agy's error output: %w", err)
	}
	defer func() { _ = stderr.Close() }()
	// agy's own limit ends the turn before the supervisor's does.
	printLimit := r.sup.limit - 2*time.Minute
	if printLimit <= 0 {
		printLimit = r.sup.limit / 2
	}
	args := []string{"-p", w.prompt, "--model", argonModel, "--sandbox",
		"--print-timeout", printLimit.String(), "--output-format", "stream-json"}
	// agy writes its own temporary files (a feature-flag cache, seen live);
	// a TMPDIR inside the workspace removes them with it (#1071).
	tmp := filepath.Join(w.dir, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return 1, nil, nil, fmt.Errorf("creating agy's temporary directory: %w", err)
	}
	env := []string{"TMPDIR=" + tmp}
	for _, value := range withHome(r.home) {
		if !strings.HasPrefix(value, "TMPDIR=") {
			env = append(env, value)
		}
	}
	code, err := r.sup.run(r.agy, args, env, w.tree, stdout, stderr)
	if err != nil {
		return max(code, 1), nil, nil, err
	}
	transcript, err := os.ReadFile(stdout.Name())
	if err != nil {
		return 1, nil, nil, fmt.Errorf("reading agy's output: %w", err)
	}
	diagnostics, err := os.ReadFile(stderr.Name())
	if err != nil {
		return 1, nil, nil, fmt.Errorf("reading agy's error output: %w", err)
	}
	return code, transcript, diagnostics, nil
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
		if err := distinctKeys([]byte(line)); err != nil {
			return "", fmt.Errorf("line %d is not a stream-json event: %w", i+1, err)
		}
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
	if err := distinctKeys(result); err != nil {
		return "", fmt.Errorf("the result cannot be parsed: %w", err)
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

// distinctKeys refuses a JSON object with two keys that Go's decoder would
// treat as one field: the later value replaces the earlier, so a duplicate
// denied_actions, status or result could hide failure evidence. The decoder
// matches names case-insensitively, hence EqualFold (Codex on #1113).
func distinctKeys(object []byte) error {
	d := json.NewDecoder(bytes.NewReader(object))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	var keys []string
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return err
		}
		key, _ := t.(string)
		for _, k := range keys {
			if strings.EqualFold(k, key) {
				return fmt.Errorf("duplicate key %q", key)
			}
		}
		keys = append(keys, key)
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return err
		}
	}
	return nil
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
