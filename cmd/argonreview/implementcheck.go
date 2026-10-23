package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Safer-side differences from checks.zsh (#1434): trusted-build check first;
// GIT_NO_LAZY_FETCH=1, GIT_NO_REPLACE_OBJECTS=1 and GIT_OPTIONAL_LOCKS=0;
// Recursive ls-tree distinguishes absence from failure; each reference blob
// must exist. HEAD, index and raw worktree bytes/modes independently match
// the reference. Git never compares worktree content or runs clean filters.
// Failed object-format or merge-base queries stop baseline checks.
// Component-wise O_NOFOLLOW|O_NONBLOCK, regular-only reads cannot follow links
// or block on FIFOs. Every walk/read/hash error is a problem;
// names survive spaces/newlines and all paths, targets and errors are quoted.
// Home names are the union of the two existing sets plus mcp_config.json outside
// its owned location; nested .gemini is refused. MCP must exist and be regular.
// Roots must be real directories and cannot nest. Only --peer-home repeats; paths
// must be absolute and clean; calling HOME must be absolute and nonempty.
// Version exit/status/shape and builtin list syntax,
// duplicates and unknown file types fail closed. Agent entries include dotfiles
// and are compared individually. Reads (including hashes and lists) and process
// stdout are capped at 1 MiB (.git at 64 KiB); processes have time limits and
// output-delivery failure fails the command. Only 200 problems plus a remaining
// count are printed. Refused directories are pruned.
// These checks observe a changing filesystem; they cannot make a later run
// atomic with the inspection, just as the guard cannot.

// implementCheckDeps supplies filesystem and process operations for tests.
type implementCheckDeps struct {
	trust       func() error
	sourceTrust func(string) error
	executable  func() (string, error)
	accountHome string
	readDir     func(string) ([]os.DirEntry, error)
	lstat       func(string) (fs.FileInfo, error)
	readlink    func(string) (string, error)
	version     func(string) ([]byte, error)
	git         func(...string) ([]byte, int, error)
}

func implementCheckDefaults() implementCheckDeps {
	return implementCheckDeps{
		trust: func() error {
			info, ok := debug.ReadBuildInfo()
			source := os.Getenv("RIBBITTO_ARGON_SOURCE")
			_, err := trusted(info, ok, func() (string, error) { return sourceRevision(source, git) })
			return err
		},
		sourceTrust: implementTrustedSource, executable: implementExecutable,
		accountHome: os.Getenv("HOME"), readDir: os.ReadDir, lstat: os.Lstat, readlink: os.Readlink,
		version: func(path string) ([]byte, error) {
			out, status, err := implementProcess(path, []string{"--version"}, os.Environ(), 30*time.Second)
			if err == nil && status != 0 {
				err = fmt.Errorf("exit status %d", status)
			}
			return out, err
		},
	}
}

type implementOnceFlag struct {
	value string
	set   bool
}

func (v *implementOnceFlag) String() string { return v.value }
func (v *implementOnceFlag) Set(s string) error {
	if v.set {
		return errors.New("flag repeated")
	}
	v.value, v.set = s, true
	return nil
}

func implementCheckCommand(args []string, stdout, stderr io.Writer, deps implementCheckDeps) int {
	// Trust precedes even flag parsing, just as probe/prompt check their build.
	if err := deps.trust(); err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview: %q\n", err)
		return 1
	}
	flags := flag.NewFlagSet("implement-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var peers implementPeerHomes
	flags.Var(&peers, "peer-home", "peer home")
	home, worktree, saved, hash, agy := &implementOnceFlag{}, &implementOnceFlag{}, &implementOnceFlag{}, &implementOnceFlag{}, &implementOnceFlag{}
	verify, source := &implementOnceFlag{}, &implementOnceFlag{}
	for _, f := range []struct {
		name  string
		value *implementOnceFlag
	}{
		{"verify", verify}, {"trusted-source", source}, {"home", home}, {"worktree", worktree}, {"git-file", saved}, {"shared-settings-sha256", hash}, {"agy", agy},
	} {
		flags.Var(f.value, f.name, f.name)
	}
	usage := func(reason string) int {
		_, _ = fmt.Fprintf(stderr, "implement-check: %s\n", reason)
		return 2
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		return usage("no positional arguments allowed")
	}
	if err := implementFeatureArguments(verify.value, source.value); err != nil {
		return usage(err.Error())
	}
	if home.value == "" || worktree.value == "" || saved.value == "" || hash.value == "" {
		return usage("--home, --worktree, --git-file and --shared-settings-sha256 are required")
	}
	if !implementMatch(`^[0-9a-fA-F]{64}$`, hash.value) {
		return usage("shared settings sha256 must be 64 hex digits")
	}
	if !agy.set {
		if deps.accountHome == "" || !filepath.IsAbs(deps.accountHome) {
			return usage("HOME must be non-empty and absolute")
		}
		agy.value = filepath.Join(deps.accountHome, ".local/bin/agy")
	}
	for _, value := range []string{home.value, worktree.value, saved.value, agy.value} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return usage(fmt.Sprintf("path must be absolute and clean: %q", value))
		}
	}
	if deps.accountHome == "" || !filepath.IsAbs(deps.accountHome) {
		return usage("HOME must be non-empty and absolute")
	}
	if err := peers.validate(home.value, deps.lstat); err != nil {
		return usage(err.Error())
	}
	c := &implementChecker{deps: deps, out: stdout, verify: verify.value == "on", source: source.value}
	for _, path := range []string{home.value, worktree.value} {
		if err := implementRealDirectory(path, deps.lstat); err != nil {
			c.problem("invalid directory %q: %q", path, err)
		}
	}
	if c.problems == 0 {
		for _, pair := range [][2]string{{home.value, worktree.value}, {worktree.value, home.value}} {
			nested, err := implementContains(pair[0], pair[1], deps.lstat)
			if err != nil {
				c.problem("checking nesting of %q and %q: %q", pair[0], pair[1], err)
			} else if nested {
				c.problem("home %q and worktree %q contain each other", home.value, worktree.value)
				break
			}
		}
	}
	if c.problems == 0 {
		if err := peers.separate(home.value, worktree.value, []string{home.value, worktree.value, saved.value, agy.value}, deps.lstat); err != nil {
			c.problem("peer homes: %q", err)
		}
	}
	if c.problems == 0 {
		if c.deps.git == nil {
			c.deps.git = implementGit(worktree.value)
		}
		if c.verify {
			if err := implementFeatureSource(home.value, worktree.value, c.source, deps.sourceTrust); err != nil {
				c.problem("trusted source: %q", err)
			}
		}
		c.check(home.value, worktree.value, saved.value, strings.ToLower(hash.value), agy.value)
	}
	return c.finish()
}

type implementChecker struct {
	verify      bool
	source      string
	deps        implementCheckDeps
	out         io.Writer
	problems    int
	outputError bool
}

func (c *implementChecker) problem(format string, args ...any) {
	c.problems++
	if c.problems <= 200 {
		c.write("CHECK: " + fmt.Sprintf(format, args...) + "\n")
	}
}
func (c *implementChecker) write(s string) {
	n, err := io.WriteString(c.out, s)
	if err != nil || n != len(s) {
		c.outputError = true
	}
}
func (c *implementChecker) finish() int {
	if c.problems > 200 {
		c.write(fmt.Sprintf("CHECK: and %d more problems\n", c.problems-200))
	}
	if c.problems == 0 {
		c.write("implement-check: ok\n")
	}
	if c.problems > 0 || c.outputError {
		return 1
	}
	return 0
}
func implementMatch(pattern, s string) bool { return regexp.MustCompile(pattern).MatchString(s) }
func (c *implementChecker) read(path string, limit int64, shared bool) ([]byte, bool) {
	data, err := implementRead(path, limit, shared)
	if err != nil {
		c.problem("reading %q: %q", path, err)
		return nil, false
	}
	return data, true
}
func (c *implementChecker) equal(a, b, label string, limit int64) bool {
	left, ok := c.read(a, limit, false)
	right, other := c.read(b, limit, false)
	if !ok || !other {
		c.problem("%s: cannot compare %q and %q", label, a, b)
		return false
	}
	if !bytes.Equal(left, right) {
		c.problem("%s: %q differs from %q", label, a, b)
		return false
	}
	return true
}
func (c *implementChecker) check(home, worktree, saved, expected, agy string) {
	shared := filepath.Join(c.deps.accountHome, ".gemini/antigravity-cli/settings.json")
	if data, ok := c.read(shared, 1<<20, true); ok {
		if fmt.Sprintf("%x", sha256.Sum256(data)) != expected {
			c.problem("shared settings hash changed: %q", shared)
		}
	}
	implementMCPContract(c, home, worktree)
	c.scan(home, true)
	c.builtin(home, agy)
	c.agent(home)
	gitUnchanged := c.equal(filepath.Join(worktree, ".git"), saved, ".git file changed", 64<<10)
	c.scan(worktree, false)
	// A replaced .git could select attacker-controlled config and clean filters.
	if gitUnchanged {
		c.baseline(worktree)
	}
}

// implementMCPContract keeps the feature switch and all three observed files
// together. Off retains the trusted-copy comparisons and regular empty MCP rule.
func implementMCPContract(c *implementChecker, home, worktree string) {
	if !c.verify {
		c.equal(filepath.Join(home, ".gemini/antigravity-cli/settings.json"), filepath.Join(home, "implementer/settings.json"), "home settings.json differs", 1<<20)
		c.equal(filepath.Join(home, ".gemini/config/hooks.json"), filepath.Join(home, "implementer/hooks.json"), "home hooks.json differs", 1<<20)
		path := filepath.Join(home, ".gemini/config/mcp_config.json")
		if data, ok := c.read(path, 1<<20, false); ok && len(data) != 0 {
			c.problem("mcp_config.json not empty: %q; use explicit --verify on for the feature-on contract", path)
		}
		return
	}
	binary, err := c.deps.executable()
	if err != nil {
		c.problem("finding running binary: %q", err)
		return
	}
	settings, s := c.read(filepath.Join(home, ".gemini/antigravity-cli/settings.json"), 1<<20, false)
	hooks, h := c.read(filepath.Join(home, ".gemini/config/hooks.json"), 1<<20, false)
	mcp, m := c.read(filepath.Join(home, ".gemini/config/mcp_config.json"), 1<<20, false)
	if s && h && m {
		if err := checkImplementerConfig(true, hooks, settings, mcp, binary, c.source, worktree, filepath.Join(home, "guard")); err != nil {
			c.problem("feature-on configuration: %q", err)
		}
	}
}

func (c *implementChecker) agent(home string) {
	dir := filepath.Join(home, ".gemini/config/agents")
	info, err := c.deps.lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		c.problem("checking %q: %q", dir, err)
		return
	}
	bad := func() { c.problem("config/agents is not exactly the trusted agent: %q", dir) }
	if !info.IsDir() {
		bad()
		return
	}
	entries, err := c.deps.readDir(dir)
	if err != nil {
		c.problem("listing %q: %q", dir, err)
		return
	}
	if len(entries) != 1 || entries[0].Name() != "argon-implementer.md" {
		bad()
		return
	}
	data, ok := c.read(filepath.Join(dir, "argon-implementer.md"), 1<<20, false)
	want, other := c.read(filepath.Join(home, "implementer/argon-implementer.md"), 1<<20, false)
	if !ok || !other || !bytes.Equal(data, want) {
		bad()
	}
}

func implementVersion(out []byte) (string, error) {
	line, rest, _ := strings.Cut(string(out), "\n")
	// Unlike checks.zsh's character filter, fail closed so malformed output
	// cannot select a trusted builtin hash list for a different version.
	if rest != "" || !implementMatch(`^[0-9]+(\.[0-9]+)+$`, line) {
		preview, suffix := string(out), ""
		if len(preview) > 64 {
			preview, suffix = preview[:64], " (truncated)"
		}
		return "", fmt.Errorf("invalid agy version %q%s", preview, suffix)
	}
	return line, nil
}

func implementBuiltinList(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return nil, errors.New("empty list at line 1")
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("malformed line %d: missing final newline", bytes.Count(data, []byte{'\n'})+1)
	}
	entries := make(map[string]string)
	for i, line := range strings.Split(string(data[:len(data)-1]), "\n") {
		kind, rel := "", ""
		if strings.HasPrefix(line, "LINK ./") {
			kind, rel = "LINK", strings.TrimPrefix(line, "LINK ./")
		} else if len(line) >= 69 && implementMatch(`^[0-9a-f]{64}  \./`, line) {
			kind, rel = line[:64], line[68:]
		}
		valid := rel != "" && !strings.ContainsAny(rel, "\x00\\")
		for _, part := range strings.Split(rel, "/") {
			if part == "" || part == "." || part == ".." {
				valid = false
			}
		}
		if !valid || kind == "" {
			return nil, fmt.Errorf("malformed line %d", i+1)
		}
		if _, exists := entries[rel]; exists {
			return nil, fmt.Errorf("duplicate path at line %d", i+1)
		}
		entries[rel] = kind
	}
	return entries, nil
}

func (c *implementChecker) builtin(home, agy string) {
	out, err := c.deps.version(agy)
	if err != nil {
		c.problem("running %q --version: %q", agy, err)
		return
	}
	version, err := implementVersion(out)
	if err != nil {
		c.problem("version from %q: %q", agy, err)
		return
	}
	list := filepath.Join(home, "implementer", "builtin.agy-"+version+".txt")
	data, err := implementRead(list, 1<<20, false)
	if err != nil {
		c.problem("no trusted builtin/ hash list for agy %q: %q: %q", version, list, err)
		return
	}
	expected, err := implementBuiltinList(data)
	if err != nil {
		c.problem("untrusted builtin/ hash list %q: %q", list, err)
		return
	}
	root := filepath.Join(home, ".gemini/antigravity-cli/builtin")
	info, err := c.deps.lstat(root)
	if err != nil {
		c.problem("checking builtin directory %q: %q", root, err)
		return
	}
	if !info.IsDir() {
		c.problem("builtin is not a directory: %q", root)
		return
	}
	actual := make(map[string]string)
	var visit func(string, string)
	visit = func(dir, prefix string) {
		entries, err := c.deps.readDir(dir)
		if err != nil {
			c.problem("listing builtin directory %q: %q", dir, err)
		}
		for _, entry := range entries {
			path, rel := filepath.Join(dir, entry.Name()), prefix+entry.Name()
			info, err := c.deps.lstat(path)
			if err != nil {
				c.problem("checking builtin entry %q: %q", path, err)
				continue
			}
			switch {
			case info.IsDir():
				visit(path, rel+"/")
			case info.Mode().IsRegular():
				data, err := implementRead(path, 1<<20, false)
				switch {
				case errors.Is(err, errImplementReadCap):
					c.problem("builtin file %q is larger than the 1 MiB read cap; builtin files are hashed whole, so a larger file needs a code change", path)
				case err != nil:
					c.problem("reading %q: %q", path, err)
				default:
					actual[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
				}
			case info.Mode()&os.ModeSymlink != 0:
				// LINK records still fail the home-wide symlink check, as in checks.zsh.
				actual[rel] = "LINK"
			default:
				c.problem("unknown file type in builtin/: %q (%s)", path, info.Mode().Type())
			}
		}
	}
	visit(root, "")
	// Iterate directory/list order deterministically for reviewable diagnostics.
	for _, rel := range implementSortedKeys(actual) {
		want, exists := expected[rel]
		if !exists {
			c.problem("extra builtin/ entry: %q", rel)
		} else if want != actual[rel] {
			c.problem("builtin/ entry differs: %q", rel)
		}
	}
	for _, rel := range implementSortedKeys(expected) {
		if _, exists := actual[rel]; !exists {
			c.problem("missing builtin/ entry: %q", rel)
		}
	}
}

func implementSortedKeys(entries map[string]string) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Do not embed bytes.Buffer: io.Copy could use its ReaderFrom to bypass Write.
// A writer error makes truncated stdout unusable, even if the child exits 0.
type implementOutput struct {
	buffer bytes.Buffer
}

func (out *implementOutput) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-out.buffer.Len() {
		return 0, errors.New("stdout exceeds 1 MiB")
	}
	return out.buffer.Write(p)
}

// implementProcess returns a status only for a normal exit. nil Stdin is
// /dev/null. Killing the process group also covers descendants holding pipes.
func implementProcess(binary string, args, env []string, limit time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, -1, err
	}
	defer func() { _ = pipe.Close() }()
	if err := cmd.Start(); err != nil {
		return nil, -1, err
	}
	// Own the copy so even a simultaneous nonzero process exit cannot hide
	// its error. Closing the pipe on timeout also handles escaped descendants
	// holding stdout open after the process group has been killed.
	stop := context.AfterFunc(ctx, func() { _ = pipe.Close() })
	defer stop()
	var out implementOutput
	_, copyErr := io.Copy(&out, pipe)
	if copyErr != nil {
		_ = cmd.Cancel()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	if copyErr != nil {
		return nil, -1, copyErr
	}
	if err == nil {
		return out.buffer.Bytes(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return out.buffer.Bytes(), exit.ExitCode(), nil
	}
	return nil, -1, err
}

func implementGit(worktree string) func(...string) ([]byte, int, error) {
	return func(args ...string) ([]byte, int, error) {
		binary := ""
		for _, path := range []string{"/usr/bin/git", "/bin/git"} {
			if _, err := os.Stat(path); err == nil {
				binary = path
				break
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, -1, err
			}
		}
		if binary == "" {
			return nil, -1, errors.New("no git in /usr/bin or /bin")
		}
		command := append([]string{"--no-pager", "-C", worktree, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.askPass=/usr/bin/false", "-c", "credential.helper=", "-c", "core.sshCommand=/usr/bin/false", "-c", "protocol.allow=never"}, args...)
		return implementProcess(binary, command, implementGitEnv(), 60*time.Second)
	}
}

func implementGitEnv() []string {
	// Missing objects in an untrusted partial clone must not trigger a fetch
	// that executes a program selected by repository configuration. Git 2.x
	// honours GIT_ASKPASS before core.askPass; override both, and reset the
	// credential.helper list. The empty GIT_ALLOW_PROTOCOL allowlist overrides
	// even per-protocol repository config; SSH refusal also fences lazy fetch.
	return []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "GIT_PROTOCOL_FROM_USER=0", "GIT_ALLOW_PROTOCOL="}
}
