// Command argonreview runs issue and adversarial reviews and Copilot follow-up
// checks with Gemini 4 Argon in Antigravity (#1041, #1065, #1270).
// verify refuses a binary not built from
// a clean origin/main of the source checkout named by RIBBITTO_ARGON_SOURCE,
// so a pull request's copy never acts as the launcher; prepare runs that
// check, then writes and checks Antigravity's configuration in the dedicated
// RIBBITTO_ARGON_HOME (#1069); check runs prepare, then agy models there under
// a supervisor that leaves nothing behind, and needs Argon listed (#1070);
// prompt builds a pull request's review workspace and prints the prompt a
// review would send (#1068);
// review runs Argon on a pull request there and prints the report only if
// the run passes the failure rules (#1096);
// issue reviews an issue against the trusted revision (#1263);
// copilot-check checks Copilot follow-up changes at the pull request head (#1270);
// probe runs the same pipeline on a local throwaway repository with a
// trusted prompt and prints what agy did (#1071);
// container-check <worktree> <build|vet|test> <package-pattern> copies Git's
// tracked and non-ignored files without protected names or symlinks, then
// runs one fixed Go command in a resource-limited, no-network container.
// It needs the trusted build, the local Docker socket and a provisioned
// /var/cache/argonreview/go-mod cache; skips never verify tests (#1429).
// Race, cgo-dependent, database and browser checks remain CI-only;
// guard is Antigravity's PreToolUse hook, exiting 0 only for an allow
// (#1130), and never checks the build, because Antigravity calls it for every
// tool.
// verify-mcp --trusted-source <abs path> --worktree <abs path> serves one
// MCP tool, verify, over stdio; it runs the container check on the fixed
// worktree (#1430).
// implement-guard <dir> enforces the implementer's file boundary without a build
// check. Optional --allow-verify admits only the fixed, strictly validated verify
// MCP call; it is off by default and also requires matching settings and config.
// implement-config generates and checks the trusted implementer configuration
// in a separate output directory without running the binary (#1433).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "implement-config" {
		return implementConfigCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "verify-mcp" {
		return verifyMCPCommand(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "container-check" {
		return containerCheckCommand(args[1:], stdout, stderr)
	}
	if len(args) == 2 && args[0] == "implement-guard" && !strings.HasPrefix(args[1], "-") {
		return implementGuard(args[1], stdin, stdout, stderr)
	}
	if len(args) == 3 && args[0] == "implement-guard" && !strings.HasPrefix(args[1], "-") && args[2] == "--allow-verify" {
		return implementGuardMode(args[1], true, stdin, stdout, stderr, filepath.WalkDir, os.Lstat)
	}
	round := 1
	approved := ""
	if len(args) == 3 && args[0] == "copilot-check" && commitID.MatchString(args[2]) {
		approved, args = args[2], args[:2]
	}
	if len(args) == 3 && args[0] == "issue" && (args[2] == "1" || args[2] == "2") {
		round, _ = strconv.Atoi(args[2])
		args = args[:2]
	}
	if len(args) == 3 && args[0] == "probe" {
		info, ok := debug.ReadBuildInfo()
		source := os.Getenv("RIBBITTO_ARGON_SOURCE")
		revision, err := trusted(info, ok, func() (string, error) { return sourceRevision(source, git) })
		code := 1
		if err == nil {
			code, err = probeArgs(source, revision, args[1], args[2], stdout)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "argonreview: %v\n", err)
			return code
		}
		return 0
	}
	if len(args) == 2 && (args[0] == "prompt" || args[0] == "review" || args[0] == "issue" || args[0] == "copilot-check" && approved != "") && prNumber.MatchString(args[1]) {
		number, _ := strconv.Atoi(args[1])
		info, ok := debug.ReadBuildInfo()
		source := os.Getenv("RIBBITTO_ARGON_SOURCE")
		revision, err := trusted(info, ok, func() (string, error) { return sourceRevision(source, git) })
		code := 1
		if err == nil && args[0] == "prompt" {
			err = printPrompt(source, revision, ribbittoURL, number, readPullRequest, func(dir string, args ...string) (string, error) {
				return gitRaw(2*time.Minute, dir, args...)
			}, stdout)
		}
		if err == nil && args[0] == "review" {
			code, err = reviewHere(source, revision, number, stdout)
		}
		if err == nil && args[0] == "issue" {
			code, err = issueHere(source, revision, number, round, stdout)
		}
		if err == nil && args[0] == "copilot-check" {
			code, err = copilotHere(source, revision, number, approved, stdout)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "argonreview: %v\n", err)
			return code
		}
		return 0
	}
	if len(args) == 1 {
		switch args[0] {
		case "guard":
			return guard(stdin, stdout, stderr)
		case "verify", "prepare", "check":
			info, ok := debug.ReadBuildInfo()
			resolve := func() (string, error) { return sourceRevision(os.Getenv("RIBBITTO_ARGON_SOURCE"), git) }
			out, err := trusted(info, ok, resolve)
			code := 1
			if err == nil && args[0] == "prepare" {
				out, err = prepareHere()
			}
			if err == nil && args[0] == "check" {
				out, code, err = checkHere()
			}
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "argonreview: %v\n", err)
				return code
			}
			_, _ = fmt.Fprintln(stdout, out)
			return 0
		}
	}
	_, _ = fmt.Fprintln(stderr, "usage: argonreview verify | prepare | check | prompt <pr-number> | review <pr-number> | issue <number> [round: 1|2] | copilot-check <pr-number> <approved-sha> | probe <repository> <prompt-file> | guard | implement-guard <dir> [--allow-verify] | verify-mcp --trusted-source <abs path> --worktree <abs path> | "+implementConfigUsage)
	return 2
}

// probeArgs resolves probe's repository to an absolute path.
func probeArgs(source, revision, repo, promptFile string, out io.Writer) (int, error) {
	repo, err := filepath.Abs(repo)
	if err != nil {
		return 1, fmt.Errorf("resolving %s: %w", repo, err)
	}
	return probeHere(source, revision, repo, promptFile, out)
}

// prepareHere runs prepare for RIBBITTO_ARGON_HOME, with the account's home
// from the user database rather than HOME, which a caller can change, and
// this binary as the guard.
func prepareHere() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("finding the account's home: %w", err)
	}
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return "", fmt.Errorf("finding this binary: %w", err)
	}
	return prepare(os.Getenv("RIBBITTO_ARGON_HOME"), account.HomeDir, self)
}

// sourceRevision resolves origin/main in the launcher's source checkout, never
// in the current directory or a review target, so neither a pull request nor
// a probed repository can change what the build is compared with (the
// maintainer on #1065). The documented invocation fetches before it builds.
func sourceRevision(source string, git func(dir string, args ...string) (string, error)) (string, error) {
	if source == "" || !filepath.IsAbs(source) {
		return "", errors.New("RIBBITTO_ARGON_SOURCE must be the absolute path of a ribbitto checkout")
	}
	// The raw local value: remote get-url applies url.*.insteadOf, which the
	// checkout's own .git/config could use to dress a fork up as ribbitto
	// (Copilot on #1073).
	url, err := git(source, "config", "--local", "--get", "remote.origin.url")
	if err != nil {
		return "", fmt.Errorf("reading the source checkout's origin: %w", err)
	}
	if !isRibbitto(url) {
		return "", fmt.Errorf("the source checkout's origin is %q, not github.com/tkakkie/ribbitto", url)
	}
	return git(source, "rev-parse", "--verify", "--quiet", "origin/main^{commit}")
}

var prNumber = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

func isRibbitto(url string) bool {
	url = strings.TrimSuffix(url, ".git")
	for _, prefix := range []string{"https://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		if url == prefix+"tkakkie/ribbitto" {
			return true
		}
	}
	return false
}

// git runs in dir with every GIT_* variable removed, because GIT_DIR,
// GIT_WORK_TREE and the like override -C. Global and system configuration are
// off too, so nothing outside the checkout (HOME, XDG_CONFIG_HOME) changes
// what it reads. Otherwise an inherited environment could make a review
// target pass as the source (Codex on #1073). The output is trimmed.
func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(10*time.Second, dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git with its output untouched and a time limit of its own, for a
// fetch or a diff that git's ten seconds may not cover.
func gitRaw(limit time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	// No replace refs either: a refs/replace/* entry in the source would
	// make the trusted revision read as other content, such as another
	// review prompt (the maintainer on #1097).
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	out, err := cmd.Output()
	return string(out), err
}
