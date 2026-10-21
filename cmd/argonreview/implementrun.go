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

const implementRunUsage = "implement-run --verify off|on [--trusted-source <abs>] --home <dir> --worktree <dir> --git-file <file> --prompt-file <file> --log-dir <dir> --shared-settings-sha256 <hex> [--agy <path>] [--model <name>] [--timeout <duration>]"

// Operations are injected at the same boundary as implement-check. The step
// hook only injects failures in tests; production always performs each step.
type implementRunDeps struct {
	trust       func() error
	sourceTrust func(string) error
	accountHome string
	executable  func() (string, error)
	check       func([]string, io.Writer, io.Writer) int
	ops         cleanupOps
	step        func(string) error
	mkdirTemp   func(string, string) (string, error)
	environ     func() []string
	saveStatus  func(*os.File, string) error
	syncLog     func(*os.File) error
	withSignals func(func(<-chan os.Signal) (int, error)) (int, error)
}

func implementRunDefaults() implementRunDeps {
	c := implementCheckDefaults()
	return implementRunDeps{
		trust: c.trust, accountHome: c.accountHome,
		sourceTrust: c.sourceTrust, executable: c.executable,
		check: func(args []string, out, stderr io.Writer) int {
			return implementCheckCommand(args, out, stderr, implementCheckDefaults())
		},
		ops: systemCleanup(), step: func(string) error { return nil },
		mkdirTemp: os.MkdirTemp, withSignals: withSignals,
		environ: os.Environ, saveStatus: implementRunSaveStatus,
		syncLog: func(f *os.File) error { return f.Sync() },
	}
}

type implementRun struct {
	home, tree, saved, prompt, logs, hash, agy, model string
	verify, source                                    string
	offSettings, offHooks, offMCP                     []byte
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
	if err := r.checkRecovery(); err != nil {
		_, _ = fmt.Fprintf(stderr, "implement-run: %q\n", err)
		return 1
	}
	// Check the untouched home before even creating its lock file.
	var atRest bytes.Buffer
	if r.checkState("off", &atRest) != 0 {
		_, _ = fmt.Fprintf(stderr, "implement-run: initial at-rest check failed: %s", atRest.String())
		return 1
	}
	lock, err := implementRunLock(r.home)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "implement-run: %q\n", err)
		return 1
	}
	defer func() { _ = lock.Close() }()
	if err := r.checkRecovery(); err != nil {
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
	names := []string{"home", "worktree", "git-file", "prompt-file", "log-dir", "shared-settings-sha256", "agy", "model", "timeout", "verify", "trusted-source"}
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
	if err := implementFeatureArguments(values[9].value, values[10].value); err != nil {
		return nil, err
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
		agy: values[6].value, model: values[7].value, limit: limit, deps: deps, verify: values[9].value, source: values[10].value}, nil
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
	if err := r.separateHome(); err != nil {
		return err
	}
	for _, root := range []string{r.tree, r.home, r.logs} {
		if err := r.agyOutside(root); err != nil {
			return err
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
	if info, err := os.Lstat(r.agy); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("agy must be an existing regular executable file: %v", err)
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
	data, err := implementRead(r.files()[2].path, 1<<20, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("existing MCP configuration: %w", err)
	}
	if len(data) != 0 {
		return errors.New("existing MCP configuration must be absent or empty for a feature-off run")
	}
	if err := implementRunTarget(filepath.Join(r.home, "guard"), true); err != nil {
		return err
	}
	r.offSettings, r.offHooks, r.offMCP = r.settings, r.hooks, r.mcp
	if r.verify == "on" {
		if err := implementFeatureSource(r.home, r.tree, r.source, r.deps.sourceTrust); err != nil {
			return err
		}
		r.hooks, r.settings, r.mcp, err = implementConfigFiles(true, binary, filepath.Join(r.home, "guard"), r.source, r.tree)
		if err != nil {
			return err
		}
	}
	return implementRunTarget(r.brain(), true)
}

// A new log directory need not exist yet, so compare clean path components.
func implementRunInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *implementRun) separateHome() error {
	account := r.deps.accountHome
	gemini := filepath.Join(account, ".gemini")
	if implementRunInside(r.home, account) || implementRunInside(r.home, gemini) || implementRunInside(gemini, r.home) {
		return errors.New("implementer home must not equal or contain calling account home, or overlap shared .gemini")
	}
	shared, err := openShared(account)
	defer shared.close()
	if err != nil {
		return fmt.Errorf("checking implementer home separation: %w", err)
	}
	root, err := walk(r.home)
	if err != nil {
		return fmt.Errorf("opening implementer home: %w", err)
	}
	defer closeFD(root)
	// The review launcher's identity chains also protect shared directories
	// relocated by symlinks and aliases on case-insensitive filesystems.
	return separate(root, shared)
}

func (r *implementRun) agyOutside(root string) error {
	if implementRunInside(root, r.agy) {
		return fmt.Errorf("agy must be outside Argon-writable location %q", root)
	}
	// Compare identities as well, including on case-insensitive filesystems.
	if _, err := os.Lstat(root); err == nil {
		inside, err := implementContains(root, r.agy, os.Lstat)
		if err != nil || inside {
			return fmt.Errorf("unsafe agy location %q: %v", root, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (r *implementRun) environment(tmp string) []string {
	parent := make([]string, 0)
	for _, entry := range r.deps.environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			parent = append(parent, entry)
		}
	}
	parent = append(parent, "PATH=/usr/bin:/bin:/usr/sbin:/sbin:"+filepath.Join(r.deps.accountHome, ".local/bin"))
	return agyEnvironment(parent, r.home, tmp)
}

type implementRunFile struct {
	name, path string
	data       []byte
	mode       os.FileMode
}

func (r *implementRun) files() []implementRunFile {
	return []implementRunFile{
		{"settings", filepath.Join(r.home, ".gemini/antigravity-cli/settings.json"), r.settings, 0o600},
		{"hooks", filepath.Join(r.home, ".gemini/config/hooks.json"), r.hooks, 0o600},
		{"mcp", filepath.Join(r.home, ".gemini/config/mcp_config.json"), r.mcp, 0o600},
		{"worktree", filepath.Join(r.home, "guard/implementer-worktree"), []byte(r.tree + "\n"), 0o600},
		{"agent", filepath.Join(r.home, ".gemini/config/agents/argon-implementer.md"), r.agent, 0o400},
	}
}

func (r *implementRun) brain() string {
	return filepath.Join(r.home, ".gemini/antigravity-cli/brain")
}
