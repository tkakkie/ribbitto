package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	agyCheckHookUsage    = "agy-check hook <mode> <dir> [<tag>]"
	agyCheckHookSentinel = ".argonreview-agy-check-hook"
	agyCheckHookTimeout  = 20
	agyCheckHookWait     = 60 * time.Second
	agyCheckHookCallCap  = 10000
	agyCheckHookReason   = "agy-check fixture denial"
)

type agyCheckHookMode struct {
	name, decision string
	code           int
}

// Parsing and configuration generation share this fixed vocabulary.
var agyCheckHookModes = []agyCheckHookMode{
	{"allow", "allow", 0}, {"deny", "deny", 0}, {"exit2", "deny", 2},
	{"exit1", "", 1}, {"unparseable", "", 0}, {"silent", "", 0},
	{"hang", "allow", 0}, {"alternate", "", 0},
}

type agyCheckHookDeps struct {
	uid    func() (int, int)
	lstat  func(string) (os.FileInfo, error)
	mkdir  func(string, os.FileMode) error
	create func(string, int, os.FileMode) (io.WriteCloser, error)
	sleep  func(time.Duration)
	stdin  io.Reader
}

func agyCheckHookDefaults(stdin io.Reader) agyCheckHookDeps {
	return agyCheckHookDeps{
		uid:   func() (int, int) { return os.Getuid(), os.Geteuid() },
		lstat: os.Lstat, mkdir: os.Mkdir, sleep: time.Sleep, stdin: stdin,
		create: func(path string, flags int, mode os.FileMode) (io.WriteCloser, error) {
			return os.OpenFile(path, flags, mode)
		},
	}
}

func agyCheckHookFindMode(name string) (agyCheckHookMode, bool) {
	for _, mode := range agyCheckHookModes {
		if name == mode.name {
			return mode, true
		}
	}
	return agyCheckHookMode{}, false
}

func agyCheckHookTag(tag string) bool {
	if len(tag) < 1 || len(tag) > 40 {
		return false
	}
	for i, c := range []byte(tag) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && c == '-') {
			return false
		}
	}
	return true
}

func agyCheckHookPrivate(path string, directory bool, uid int, d agyCheckHookDeps) error {
	info, err := d.lstat(path)
	if err != nil {
		return fmt.Errorf("examining hook state: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		return errors.New("hook state must be uid-owned")
	}
	want := os.FileMode(0o600)
	if directory {
		want = os.ModeDir | 0o700
	}
	if info.Mode() != want {
		return errors.New("hook state must be real, private and have the exact mode")
	}
	return nil
}

func agyCheckHookState(dir string, d agyCheckHookDeps) error {
	if !agyCheckCleanPath(dir) || agyCheckSettingsPath(dir) {
		return errors.New("hook directory must be absolute, clean, non-root and not a settings path")
	}
	uid, effective := d.uid()
	if effective == 0 {
		return errors.New("hook must not run as uid 0")
	}
	if err := agyCheckHookPrivate(dir, true, uid, d); err != nil {
		return err
	}
	// B3 must create this sentinel in a fresh state directory per row. Requiring
	// deliberate preparation prevents accidental use as a production home hook.
	return agyCheckHookPrivate(filepath.Join(dir, agyCheckHookSentinel), false, uid, d)
}

func agyCheckHookWrite(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("writing hook output: %w", err)
	}
	return nil
}

func agyCheckHookClaim(dir, mode, tag string, d agyCheckHookDeps) (string, error) {
	calls := filepath.Join(dir, "calls")
	if err := d.mkdir(calls, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("creating hook calls directory: %w", err)
	}
	uid, _ := d.uid()
	if err := agyCheckHookPrivate(calls, true, uid, d); err != nil {
		return "", err
	}
	if tag == "" {
		tag = "-"
	}
	// An allow-everything agent can itself write this evidence. It describes
	// the default agent only; it is not tamper-proof production evidence.
	for n := 1; n <= agyCheckHookCallCap; n++ {
		file, err := d.create(filepath.Join(calls, fmt.Sprintf("%06d", n)), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("claiming hook call: %w", err)
		}
		word := mode
		if mode == "alternate" {
			word = "deny"
			if n%2 == 1 {
				word = "allow"
			}
		}
		// Exclusive creation makes parity independent of process scheduling.
		// Close before output or sleep so reconciliation sees every invocation.
		writeErr := agyCheckHookWrite(file, []byte(word+" "+tag+"\n"))
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return "", fmt.Errorf("recording hook call: %w", err)
		}
		return word, nil
	}
	return "", errors.New("hook call limit exceeded")
}

func agyCheckHook(args []string, stdout, stderr io.Writer, d agyCheckHookDeps) int {
	usage := func() int {
		var names []string
		for _, mode := range agyCheckHookModes {
			names = append(names, mode.name)
		}
		_, _ = fmt.Fprintf(stderr, "usage: argonreview %s (modes: %s)\n", agyCheckHookUsage, strings.Join(names, ", "))
		return 2
	}
	if len(args) < 2 || len(args) > 3 {
		return usage()
	}
	mode, ok := agyCheckHookFindMode(args[0])
	if !ok {
		return usage()
	}
	tag := ""
	if len(args) == 3 {
		tag = args[2]
		if !agyCheckHookTag(tag) {
			return usage()
		}
	}
	refuse := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "agy-check hook: %s\n", agyCheckPrintable(err.Error()))
		return 1
	}
	// Consume the production guard's bounded input even though fixture decisions
	// do not depend on its contents. Bytes beyond the bound are ignored.
	if _, err := io.Copy(io.Discard, io.LimitReader(d.stdin, maxGuardInput+1)); err != nil {
		return refuse(fmt.Errorf("reading hook input: %w", err))
	}
	if err := agyCheckHookState(args[1], d); err != nil {
		return refuse(err)
	}
	word, err := agyCheckHookClaim(args[1], mode.name, tag, d)
	if err != nil {
		return refuse(err)
	}
	decision := mode.decision
	if mode.name == "alternate" {
		decision = word
	}
	if mode.name == "hang" {
		d.sleep(agyCheckHookWait)
	}
	var output []byte
	switch {
	case decision != "":
		reply := struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason,omitempty"`
		}{Decision: decision}
		if decision == "deny" {
			reply.Reason = agyCheckHookReason
		}
		output, err = json.Marshal(reply)
		if err != nil {
			return refuse(fmt.Errorf("encoding hook decision: %w", err))
		}
		output = append(output, '\n')
	case mode.name == "unparseable":
		output = []byte("not JSON\n")
	}
	if len(output) != 0 {
		if err := agyCheckHookWrite(stdout, output); err != nil {
			return refuse(err)
		}
	}
	if mode.code != 0 {
		_, _ = fmt.Fprintln(stderr, agyCheckHookReason)
	}
	return mode.code
}
