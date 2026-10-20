package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const implementRunUsage = "implement-run --home <dir> --worktree <dir> --git-file <file> --prompt-file <file> --log-dir <dir> --shared-settings-sha256 <hex> [--agy <path>] [--model <name>] [--timeout <duration>]"

// Operations are injected at the same boundary as implement-check. The step
// hook only injects failures in tests; production always performs each step.
type implementRunDeps struct {
	trust       func() error
	accountHome string
	executable  func() (string, error)
	check       func([]string, io.Writer, io.Writer) int
	ops         cleanupOps
	step        func(string) error
	mkdirTemp   func(string, string) (string, error)
	withSignals func(func(<-chan os.Signal) (int, error)) (int, error)
}

func implementRunDefaults() implementRunDeps {
	c := implementCheckDefaults()
	return implementRunDeps{
		trust: c.trust, accountHome: c.accountHome,
		executable: func() (string, error) {
			path, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(path)
		},
		check: func(args []string, out, stderr io.Writer) int {
			return implementCheckCommand(args, out, stderr, implementCheckDefaults())
		},
		ops: systemCleanup(), step: func(string) error { return nil },
		mkdirTemp: os.MkdirTemp, withSignals: withSignals,
	}
}

type implementRun struct {
	home, tree, saved, prompt, logs, hash, agy, model string
	limit                                             time.Duration
	settings, hooks, agent, mcp                       []byte
	deps                                              implementRunDeps
}

func implementRunCommand(args []string, stdout, stderr io.Writer, deps implementRunDeps) int {
	if err := deps.trust(); err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview: %q\n", err)
		return 1
	}
	r, err := implementRunArguments(args, stderr, deps)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "implement-run: %q\n", err)
		return 2
	}
	// All validation, including trusted byte comparison, precedes log creation.
	if err := r.validate(); err != nil {
		_, _ = fmt.Fprintf(stderr, "implement-run: %q\n", err)
		return 1
	}
	code, err := r.execute(stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "implement-run: %q\n", err)
	}
	return code
}

func implementRunArguments(args []string, out io.Writer, deps implementRunDeps) (*implementRun, error) {
	flags := flag.NewFlagSet("implement-run", flag.ContinueOnError)
	flags.SetOutput(out)
	names := []string{"home", "worktree", "git-file", "prompt-file", "log-dir", "shared-settings-sha256", "agy", "model", "timeout"}
	values := make([]implementOnceFlag, len(names))
	for i, name := range names {
		flags.Var(&values[i], name, name)
	}
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("no positional arguments allowed")
	}
	for i := range 6 {
		if values[i].value == "" {
			return nil, fmt.Errorf("--%s is required", names[i])
		}
	}
	if !implementMatch(`^[0-9a-fA-F]{64}$`, values[5].value) {
		return nil, errors.New("shared settings sha256 must be 64 hex digits")
	}
	if deps.accountHome == "" || !filepath.IsAbs(deps.accountHome) || filepath.Clean(deps.accountHome) != deps.accountHome {
		return nil, errors.New("HOME must be absolute and clean")
	}
	if !values[6].set {
		values[6].value = filepath.Join(deps.accountHome, ".local/bin/agy")
	}
	if !values[7].set {
		values[7].value = argonModel
	}
	if !values[8].set {
		values[8].value = "20m"
	}
	limit, err := time.ParseDuration(values[8].value)
	if err != nil || limit <= 0 || strings.TrimSpace(values[7].value) == "" {
		return nil, errors.New("timeout must be positive and model nonempty")
	}
	return &implementRun{home: values[0].value, tree: values[1].value, saved: values[2].value,
		prompt: values[3].value, logs: values[4].value, hash: strings.ToLower(values[5].value),
		agy: values[6].value, model: values[7].value, limit: limit, deps: deps}, nil
}

func (r *implementRun) validate() error {
	// The unchanged guard strips whitespace/information separators from its
	// marker. Such a worktree name could otherwise select a different root.
	guardRoot := strings.TrimFunc(r.tree, func(r rune) bool {
		return unicode.IsSpace(r) || r >= '\x1c' && r <= '\x1f'
	})
	if guardRoot != r.tree || !utf8.ValidString(r.tree) {
		return errors.New("worktree path cannot be represented by the implementer guard")
	}
	for _, path := range []string{r.home, r.tree, r.saved, r.prompt, r.logs, r.agy} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("path must be absolute and clean: %q", path)
		}
	}
	for _, path := range []string{r.home, r.tree, filepath.Dir(r.logs)} {
		if err := implementRealDirectory(path, os.Lstat); err != nil {
			return fmt.Errorf("directory %q: %w", path, err)
		}
	}
	for _, pair := range [][2]string{{r.home, r.tree}, {r.tree, r.home}, {r.tree, r.saved}, {r.home, r.saved}, {r.tree, r.prompt}, {r.tree, r.logs}, {r.home, r.logs}} {
		// A new log directory is compared via its existing parent.
		child := pair[1]
		if child == r.logs {
			child = filepath.Dir(child)
		}
		yes, err := implementContains(pair[0], child, os.Lstat)
		if err != nil || yes {
			return fmt.Errorf("unsafe containment %q / %q: %v", pair[0], pair[1], err)
		}
	}
	if info, err := os.Lstat(r.logs); err == nil {
		if err := implementRealDirectory(r.logs, os.Lstat); err != nil {
			return err
		}
		for _, child := range []string{r.tree, r.home} {
			inside, err := implementContains(r.logs, child, os.Lstat)
			if err != nil || inside {
				return errors.New("log directory contains home or worktree")
			}
		}
		entries, err := os.ReadDir(r.logs)
		if err != nil || len(entries) != 0 || !info.IsDir() {
			return errors.New("log directory must be a real empty directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := implementRead(r.saved, 64<<10, false); err != nil {
		return fmt.Errorf("saved .git: %w", err)
	}
	if err := implementRunSingleLink(r.saved); err != nil {
		return err
	}
	prompt, err := implementRead(r.prompt, 1<<20, false)
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	r.prompt = string(prompt)
	if err := implementRunTarget(r.agy, false); err != nil {
		return fmt.Errorf("agy: %w", err)
	}
	if info, err := os.Lstat(r.agy); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("agy must be an existing regular file: %v", err)
	}
	binary, err := r.deps.executable()
	if err != nil {
		return err
	}
	r.hooks, err = implementerHooks(false, binary, filepath.Join(r.home, "guard"))
	if err != nil {
		return err
	}
	r.settings = implementerSettings(false)
	r.mcp, err = implementerMCPConfig(false, binary, "", r.tree)
	if err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"settings.json", r.settings}, {"hooks.json", r.hooks}} {
		data, err := implementRead(filepath.Join(r.home, "implementer", file.name), 1<<20, false)
		if err != nil || !bytes.Equal(data, file.data) {
			return fmt.Errorf("trusted %s does not match generated bytes: %v", file.name, err)
		}
	}
	r.agent, err = implementRead(filepath.Join(r.home, "implementer/argon-implementer.md"), 1<<20, false)
	if err != nil {
		return err
	}
	for _, file := range r.files() {
		if err := implementRunTarget(file.path, false); err != nil {
			return err
		}
	}
	return implementRunTarget(r.brain(), true)
}

type implementRunFile struct {
	name, path string
	data       []byte
}

func (r *implementRun) files() []implementRunFile {
	return []implementRunFile{
		{"settings", filepath.Join(r.home, ".gemini/antigravity-cli/settings.json"), r.settings},
		{"hooks", filepath.Join(r.home, ".gemini/config/hooks.json"), r.hooks},
		{"mcp", filepath.Join(r.home, ".gemini/config/mcp_config.json"), r.mcp},
		{"worktree", filepath.Join(r.home, "guard/implementer-worktree"), []byte(r.tree + "\n")},
		{"agent", filepath.Join(r.home, ".gemini/config/agents/argon-implementer.md"), r.agent},
	}
}

func (r *implementRun) brain() string {
	return filepath.Join(r.home, ".gemini/antigravity-cli/brain")
}
