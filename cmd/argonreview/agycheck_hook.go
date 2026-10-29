package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
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
	agyCheckHookHold     = 1500 * time.Millisecond
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
	current func() (*user.User, error)
	uid     func() (int, int)
	lstat   func(string) (os.FileInfo, error)
	mkdir   func(string, os.FileMode) error
	create  func(string, int, os.FileMode) (io.WriteCloser, error)
	sleep   func(time.Duration)
	stdin   io.Reader
}

func agyCheckHookDefaults(stdin io.Reader) agyCheckHookDeps {
	return agyCheckHookDeps{
		current: user.Current,
		uid:     func() (int, int) { return os.Getuid(), os.Geteuid() },
		lstat:   os.Lstat, mkdir: os.Mkdir, sleep: time.Sleep, stdin: stdin,
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
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && (i == 0 || c != '-') {
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
	account, err := d.current()
	if err != nil {
		return fmt.Errorf("finding hook process user: %w", err)
	}
	real, effective := d.uid()
	uid, err := agyCheckProcessIdentity(account, real, effective)
	if err != nil {
		return err
	}
	// Walk from / down so no lstat reaches through a symlinked ancestor.
	// The check-then-create window remains. B3 and the preparation page must
	// place state under the marker's isolation directory; this hook has no marker
	// with which to verify that requirement.
	component := string(filepath.Separator)
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		component = filepath.Join(component, part)
		info, err := d.lstat(component)
		if err != nil {
			return fmt.Errorf("examining hook directory component %q: %w", component, err)
		}
		if !info.Mode().IsDir() {
			return fmt.Errorf("hook directory component must be a real directory: %q", component)
		}
	}
	if err := agyCheckHookPrivate(dir, true, uid, d); err != nil {
		return err
	}
	// B3 must prepare a fresh state directory and sentinel per row. Their owner
	// must equal the process uid; agyCheckValidate binds the username to the
	// marker before every agy start (B3). This deliberate preparation prevents
	// accidental production use. Calls stay separate from agent output where
	// possible: the hook writes calls/ and events/ records, never prompts. Missing,
	// contradictory or malformed records are differences in B3, never normal.
	// Reconciliation checks ordinary-agent regressions, not adversarial tampering:
	// an allow-everything agent can write records itself. A match never
	// auto-approves trust of a new version.
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

// Exclusive creation supplies a global order without comparing process clocks.
func agyCheckHookRecord(dir, sub string, cap int, content func(int) string, d agyCheckHookDeps) (int, error) {
	records := filepath.Join(dir, sub)
	if err := d.mkdir(records, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return 0, fmt.Errorf("creating hook records directory: %w", err)
	}
	uid, _ := d.uid()
	if err := agyCheckHookPrivate(records, true, uid, d); err != nil {
		return 0, err
	}
	for n := 1; n <= cap; n++ {
		file, err := d.create(filepath.Join(records, fmt.Sprintf("%06d", n)), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("claiming hook record: %w", err)
		}
		writeErr := agyCheckHookWrite(file, []byte(content(n)))
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return 0, fmt.Errorf("recording hook call: %w", err)
		}
		return n, nil
	}
	return 0, errors.New("hook record limit exceeded")
}

func agyCheckHookClaim(dir, mode, tag string, d agyCheckHookDeps) (int, string, error) {
	if tag == "" {
		tag = "-"
	}
	word := mode
	n, err := agyCheckHookRecord(dir, "calls", agyCheckHookCallCap, func(n int) string {
		if mode == "alternate" {
			word = "deny"
			if n%2 == 1 {
				word = "allow"
			}
		}
		return word + " " + tag + "\n"
	}, d)
	return n, word, err
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
	if err := agyCheckHookState(args[1], d); err != nil {
		return refuse(err)
	}
	n, word, err := agyCheckHookClaim(args[1], mode.name, tag, d)
	if err != nil {
		return refuse(err)
	}
	if mode.name == "alternate" {
		eventTag := tag
		if eventTag == "" {
			eventTag = "-"
		}
		if _, err := agyCheckHookRecord(args[1], "events", 2*agyCheckHookCallCap, func(int) string { return fmt.Sprintf("start %d %s %s\n", n, word, eventTag) }, d); err != nil {
			return refuse(err)
		}
		d.sleep(agyCheckHookHold)
	}

	// Record before reading: never-closing or failing stdin must still leave a
	// claim for the runner, since agy sees a refused hook as a deny. Consume the
	// guard's bounded input; bytes beyond the bound are ignored.
	if _, err := io.Copy(io.Discard, io.LimitReader(d.stdin, maxGuardInput+1)); err != nil {
		return refuse(fmt.Errorf("reading hook input: %w", err))
	}
	if mode.name == "alternate" {
		if _, err := agyCheckHookRecord(args[1], "events", 2*agyCheckHookCallCap, func(int) string { return fmt.Sprintf("end %d\n", n) }, d); err != nil {
			return refuse(err)
		}
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
