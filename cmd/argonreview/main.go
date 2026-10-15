// Command argonreview runs adversarial reviews of pull requests with Gemini 4
// Argon in Antigravity (#1041, #1065). verify refuses a binary not built from
// a clean origin/main of the source checkout named by RIBBITTO_ARGON_SOURCE,
// so a pull request's copy never acts as the launcher; prepare runs that
// check, then writes and checks Antigravity's configuration in the dedicated
// RIBBITTO_ARGON_HOME (#1069); check runs prepare, then agy models there under
// a supervisor that leaves nothing behind, and needs Argon listed (#1070);
// prompt builds a pull request's review workspace and prints the prompt a
// review would send (#1068);
// guard is Antigravity's PreToolUse hook and never checks the build, because
// Antigravity calls it for every tool.
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
	if len(args) == 2 && args[0] == "prompt" && prNumber.MatchString(args[1]) {
		number, _ := strconv.Atoi(args[1])
		info, ok := debug.ReadBuildInfo()
		source := os.Getenv("RIBBITTO_ARGON_SOURCE")
		revision, err := trusted(info, ok, func() (string, error) { return sourceRevision(source, git) })
		if err == nil {
			err = printPrompt(source, revision, number, readPullRequest, func(dir string, args ...string) (string, error) {
				return gitRaw(2*time.Minute, dir, args...)
			}, stdout)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "argonreview: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) == 1 {
		switch args[0] {
		case "guard":
			guard(stdin, stdout)
			return 0
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
	_, _ = fmt.Fprintln(stderr, "usage: argonreview verify | prepare | check | prompt <pr-number> | guard")
	return 2
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
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	return string(out), err
}
