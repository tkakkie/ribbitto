package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// argonModel is the model every review runs on (#1041).
const argonModel = "gemini-4-argon-high"

// checkHere runs prepare, then agy models under supervision in the dedicated
// HOME, and succeeds only if Argon is listed. A failing agy's status is kept:
// 124, 130 or 143 when the time limit or a signal ended it. Any other failure
// is 1.
func checkHere() (string, int, error) {
	limit, err := timeLimit()
	if err != nil {
		return "", 1, err
	}
	agy, err := exec.LookPath("agy")
	if err != nil {
		return "", 1, fmt.Errorf("finding agy: %w", err)
	}
	prepare := func() error { _, err := prepareHere(); return err }
	code, err := withSignals(func(signals <-chan os.Signal) (int, error) {
		return check(os.Getenv("RIBBITTO_ARGON_HOME"), agy, prepare, supervisor{limit, 5 * time.Second, signals, systemCleanup()})
	})
	return "agy lists " + argonModel, code, err
}

// withSignals hands INT and TERM to the supervisor instead of letting them
// end this process with agy still running.
func withSignals(f func(<-chan os.Signal) (int, error)) (int, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	return f(signals)
}

// timeLimit reads RIBBITTO_ARGON_TIMEOUT, a Go duration; unset, it is the
// trial runner's 25 minutes (#1041).
func timeLimit() (time.Duration, error) {
	value := os.Getenv("RIBBITTO_ARGON_TIMEOUT")
	if value == "" {
		return 25 * time.Minute, nil
	}
	limit, err := time.ParseDuration(value)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("RIBBITTO_ARGON_TIMEOUT must be a positive duration such as 25m, not %q", value)
	}
	return limit, nil
}

// check runs prepare, then agy models with HOME set to home and home as the
// working directory, so neither the caller's HOME nor a checkout's .agents
// applies.
func check(home, agy string, prepare func() error, s supervisor) (int, error) {
	if err := prepare(); err != nil {
		return 1, err
	}
	out, err := os.CreateTemp("", "argonreview-models-*")
	if err != nil {
		return 1, fmt.Errorf("creating the models listing: %w", err)
	}
	defer func() { _ = out.Close(); _ = os.Remove(out.Name()) }()
	code, err := s.run(agy, []string{"models"}, agyEnvironment(os.Environ(), home, ""), home, out, os.Stderr)
	if err == nil && code != 0 {
		err = fmt.Errorf("agy models exited with status %d", code)
	}
	if err != nil {
		return max(code, 1), err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return 1, fmt.Errorf("reading the models listing: %w", err)
	}
	listing, err := io.ReadAll(io.LimitReader(out, 1<<20))
	if err != nil {
		return 1, fmt.Errorf("reading the models listing: %w", err)
	}
	if !lists(string(listing), argonModel) {
		return 1, fmt.Errorf("agy models does not list %s", argonModel)
	}
	return 0, nil
}

// agyEnvironment keeps operator credentials out of agy; its login token lives
// in the dedicated HOME. Extending this allowlist requires a code change and
// pull request, never an environment setting.
func agyEnvironment(parent []string, home, tmp string) []string {
	// HOME selects the dedicated sign-in and configuration; TMPDIR confines
	// review caches to the workspace. Neither comes from the parent.
	env := []string{"HOME=" + home}
	if tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	for _, value := range parent {
		name, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		switch name {
		case "PATH": // agy and its sandbox helpers need executable lookup.
			env = append(env, value)
		case "LANG", "LC_ALL", "LC_COLLATE", "LC_CTYPE", "LC_MESSAGES", "LC_MONETARY", "LC_NUMERIC",
			"LC_TIME", "LC_ADDRESS", "LC_IDENTIFICATION", "LC_MEASUREMENT", "LC_NAME", "LC_PAPER",
			"LC_TELEPHONE": // Preserve POSIX/glibc locale selection.
			env = append(env, value)
		case "USER", "LOGNAME": // macOS keychain fallback needs the account name.
			env = append(env, value)
		}
	}
	return env
}

// lists reports whether a line of agy models names model; each line is the
// model ID, a tab and its display name.
func lists(listing, model string) bool {
	for _, line := range strings.Split(listing, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == model {
			return true
		}
	}
	return false
}
