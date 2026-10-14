// Command argonreview runs adversarial reviews of pull requests with Gemini 4
// Argon in Antigravity (#1041, #1065). verify refuses a binary not built from
// a clean origin/main of the source checkout named by RIBBITTO_ARGON_SOURCE,
// so a pull request's copy never acts as the launcher;
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
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "guard":
			guard(stdin, stdout)
			return 0
		case "verify":
			info, ok := debug.ReadBuildInfo()
			resolve := func() (string, error) { return sourceRevision(os.Getenv("RIBBITTO_ARGON_SOURCE"), git) }
			revision, err := trusted(info, ok, resolve)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "argonreview: %v\n", err)
				return 1
			}
			_, _ = fmt.Fprintln(stdout, revision)
			return 0
		}
	}
	_, _ = fmt.Fprintln(stderr, "usage: argonreview verify | guard")
	return 2
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
// target pass as the source (Codex on #1073).
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
