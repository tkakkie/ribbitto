package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type agyCheckFake struct {
	Output string
	Code   int
	Home   string
}

type agyCheckPinReader struct {
	io.Reader
	readErr error
	closed  bool
}

func (r *agyCheckPinReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && r.readErr != nil {
		err = r.readErr
	}
	return n, err
}

func (r *agyCheckPinReader) Close() error {
	r.closed = true
	return nil
}

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "agy-check-tool" {
		agyCheckFastRaceExit()
		os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if filepath.Base(os.Args[0]) == "agy-check-fake" {
		agyCheckFastRaceExit()
		data, err := os.ReadFile(os.Args[0] + ".fixture")
		if agyCheckRowsFake(data) {
			return
		}
		if agyCheckScriptedFake(data) {
			return
		}
		var f agyCheckFake
		if err != nil || json.Unmarshal(data, &f) != nil || len(os.Args) != 2 || os.Args[1] != "--version" ||
			os.Getenv("HOME") != f.Home || os.Getenv("TMPDIR") == "" || os.Getenv("AGY_CHECK_FORBIDDEN_ENV") != "" {
			os.Exit(90)
		}
		_, _ = fmt.Print(f.Output)
		os.Exit(f.Code)
	}
	os.Exit(m.Run())
}

// The race runtime's 1 s atexit sleep affects 87 helpers per full check (#1980).
// GORACE is read only at startup, so re-exec and restore the environment before
// any helper code inspects it or starts children.
func agyCheckFastRaceExit() {
	if value, set := os.LookupEnv("GORACE"); set {
		if value == "atexit_sleep_ms=0" {
			_ = os.Unsetenv("GORACE")
		}
		return
	}
	path := os.Args[0]
	if !strings.Contains(path, "/") {
		var err error
		path, err = os.Executable()
		if err != nil {
			return
		}
	}
	_ = syscall.Exec(path, os.Args, append(os.Environ(), "GORACE=atexit_sleep_ms=0"))
}

// Hard links share immutable test-binary bytes while retaining private fixture
// paths and sidecars. The replacement case unlinks its path before writing.
func agyCheckLinkedBinary(t *testing.T, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agyCheckRealTemp(t), name)
	if err := os.Link(self, path); err == nil {
		return path
	}
	// A different filesystem cannot hard-link: fall back to a private copy.
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o500)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("copying fake: %v %v", err, closeErr)
	}
	return path
}

func agyCheckFakeBinary(t *testing.T) string {
	t.Helper()
	return agyCheckLinkedBinary(t, "agy-check-fake")
}

func TestAgyCheckPinRecording(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, second string
		code         int
		hashChange   bool
		invalid      bool
	}{
		{"stable", "agy 1.4.7 build 9.9", 0, false, false},
		{"version changed", "agy 1.4.8", 0, false, true},
		{"hash changed", "agy 1.4.7", 0, true, true},
		{"version failed", "agy 1.4.7", 3, false, true},
		{"version malformed", "agy unknown", 0, false, true},
		{"hash failed", "agy 1.4.7", 0, false, true},
		{"hash open failed", "agy 1.4.7", 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := agyCheckFakeBinary(t)
			isolation := t.TempDir()
			o := agyCheckOptions{home: "/fake-home", agy: path}
			ops := systemCleanup()
			// The helper never forks. Avoid ps, which the Codex sandbox forbids;
			// retain the real process start/wait and process-group supervisor.
			ops.scan = func(int) ([]int, error) { return nil, nil }
			d := agyCheckDefaults()
			reads := 0
			var opened []*agyCheckPinReader
			d.open = func(p string) (io.ReadCloser, error) {
				if p != path {
					t.Fatalf("pin read outside executable: %s", p)
				}
				reads++
				if tc.name == "hash open failed" && reads == 2 {
					return nil, os.ErrPermission
				}
				data := "executable"
				if tc.hashChange && reads == 2 {
					data = "changed executable"
				}
				r := &agyCheckPinReader{Reader: strings.NewReader(data)}
				if reads == 2 {
					if tc.name == "hash failed" {
						r.readErr = os.ErrPermission
					}
				}
				opened = append(opened, r)
				return r, nil
			}
			writeFixture := func(output string, code int) {
				t.Helper()
				data, err := json.Marshal(agyCheckFake{output, code, o.home})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path+".fixture", data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeFixture("agy 1.4.7 build 9.9", 0)
			before, err := agyCheckRecordPin(context.Background(), o, isolation, d, ops)
			wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte("executable")))
			if err != nil || before.Version != "1.4.7" || before.ExecutableSHA256 != wantHash {
				t.Fatalf("first pin: %+v %v", before, err)
			}
			writeFixture(tc.second, tc.code)
			after, err := agyCheckRecordPin(context.Background(), o, isolation, d, ops)
			if (err != nil) != (tc.code != 0 || strings.HasSuffix(tc.name, "failed")) {
				t.Fatalf("version command failure lost: %v", err)
			}
			for _, r := range opened {
				if !r.closed {
					t.Fatal("pin executable left open")
				}
			}
			r := agyCheckEvaluate(before, after, nil, 30, "")
			if (r.InvalidReason != "") != tc.invalid {
				t.Fatalf("second pin not checked: %+v %+v: %+v", before, after, r)
			}
			if entries, err := os.ReadDir(isolation); err != nil || len(entries) != 0 {
				t.Fatalf("pin files left behind: %v %v", entries, err)
			}
			if tc.invalid {
				var out strings.Builder
				if err := agyCheckRender(&out, r); err != nil || agyCheckExitCode(r) != 1 || !strings.HasPrefix(out.String(), "RESULT INVALID:") {
					t.Fatalf("invalid pin report: %q %v", out.String(), err)
				}
			}
		})
	}
}

// Environment mutation stays in a sequential test; pin fixtures only share
// immutable executable bytes and can run concurrently with command fixtures.
func TestAgyCheckPinScrubsEnvironment(t *testing.T) {
	path := agyCheckFakeBinary(t)
	t.Setenv("AGY_CHECK_FORBIDDEN_ENV", "must be scrubbed")
	home := "/fake-home"
	ops := systemCleanup()
	ops.scan = func(int) ([]int, error) { return nil, nil }
	before := agyCheckPin{Version: "1.4.7", ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("executable")))}
	for _, tc := range []struct {
		name, output, version, executable string
		code                              int
	}{
		{"stable", "agy 1.4.7", "1.4.7", "executable", 0},
		{"nonzero", "agy 1.4.7", "", "executable", 3},
		{"unparseable", "agy unknown", "", "executable", 0},
		{"hash-change", "agy 1.4.7", "1.4.7", "changed executable", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(agyCheckFake{tc.output, tc.code, home})
			if err != nil {
				t.Fatal(err)
			}
			agyCheckTestWrite(t, path+".fixture", data, 0o600)
			d := agyCheckDefaults()
			d.open = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(tc.executable)), nil }
			pin, err := agyCheckRecordPin(context.Background(), agyCheckOptions{home: home, agy: path}, t.TempDir(), d, ops)
			if tc.code != 0 {
				if err == nil || !strings.Contains(err.Error(), "version command exited 3") {
					t.Fatalf("pin environment was not scrubbed: %+v %v", pin, err)
				}
				return
			}
			wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(tc.executable)))
			if err != nil || pin.Version != tc.version || pin.ExecutableSHA256 != wantHash {
				t.Fatalf("pin environment was not scrubbed: %+v %v", pin, err)
			}
			if got := agyCheckEvaluate(before, pin, nil, 30, ""); (got.InvalidReason != "") != (tc.name != "stable") {
				t.Fatalf("scrubbed pin outcome changed: %+v", got)
			}
		})
	}
}
