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
	url, err := git(source, "remote", "get-url", "origin")
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

// git runs in dir with every GIT_* variable removed: GIT_DIR, GIT_WORK_TREE
// and the like override -C, and GIT_CONFIG_* could rewrite the origin URL, so
// an inherited environment could point the check at a review target (Codex
// on #1073).
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
