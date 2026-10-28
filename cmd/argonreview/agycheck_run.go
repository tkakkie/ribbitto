package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

const agyCheckRunUsage = "agy-check --home <absolute throwaway home> --agy <absolute executable> [--calls N] [--model <name>]"

// Keep the reviewer's model as the default so the eventual observations use
// the same model as production; operators can explicitly select another.
const agyCheckModel = argonModel

type agyCheckOptions struct {
	home, agy, model string
	calls            int
}

type agyCheckDeps struct {
	current    func() (*user.User, error)
	ids        func() (int, int)
	executable func() (string, error)
	lstat      func(string) (os.FileInfo, error)
	read       func(string, int64) ([]byte, error)
	open       func(string) (io.ReadCloser, error)
	access     func(string, uint32) error
	execution  *agyCheckExecutionDeps
}

func agyCheckDefaults() agyCheckDeps {
	return agyCheckDeps{
		current: user.Current, ids: func() (int, int) { return os.Getuid(), os.Geteuid() },
		executable: os.Executable, lstat: os.Lstat, access: syscall.Access,
		read: func(path string, cap int64) ([]byte, error) { return implementRead(path, cap, false) },
		open: func(path string) (io.ReadCloser, error) { return os.Open(path) },
	}
}

func agyCheckArguments(args []string) (agyCheckOptions, error) {
	o := agyCheckOptions{calls: agyCheckDefaultCalls, model: agyCheckModel}
	f := flag.NewFlagSet("agy-check", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var home, agy, calls, model implementOnceFlag
	for name, value := range map[string]*implementOnceFlag{"home": &home, "agy": &agy, "calls": &calls, "model": &model} {
		f.Var(value, name, name)
	}
	if err := f.Parse(args); err != nil {
		return o, fmt.Errorf("parsing agy-check flags: %w", err)
	}
	if f.NArg() != 0 || !agyCheckCleanPath(home.value) || !agyCheckCleanPath(agy.value) || agyCheckSettingsPath(home.value) || agyCheckSettingsPath(agy.value) {
		return o, errors.New("home and agy must be absolute clean non-root paths; no positional arguments")
	}
	o.home, o.agy = home.value, agy.value
	if calls.set {
		var err error
		o.calls, err = strconv.Atoi(calls.value)
		if err != nil || o.calls <= 0 {
			return o, errors.New("calls must be a positive integer")
		}
	}
	if model.set {
		o.model = model.value
		if strings.TrimSpace(o.model) == "" {
			return o, errors.New("model must be nonempty")
		}
	}
	return o, nil
}

func agyCheckCommand(args []string, stdout, stderr io.Writer, d agyCheckDeps) int {
	o, err := agyCheckArguments(args)
	if err != nil {
		r := agyCheckEvaluate(agyCheckPin{}, agyCheckPin{}, nil, o.calls, err.Error())
		_ = agyCheckRender(stdout, r)
		_, _ = fmt.Fprintln(stderr, "usage: argonreview "+agyCheckRunUsage)
		return agyCheckExitCode(r)
	}
	r := agyCheckExecute(o, d)
	if err := agyCheckRender(stdout, r); err != nil {
		return agyCheckDiffers
	}
	return agyCheckExitCode(r)
}
