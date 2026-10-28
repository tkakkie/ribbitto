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
		os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if filepath.Base(os.Args[0]) == "agy-check-fake" {
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

func agyCheckFakeBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	path := filepath.Join(agyCheckRealTemp(t), "agy-check-fake")
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

func TestAgyCheckPinRecording(t *testing.T) {
	path := agyCheckFakeBinary(t)
	t.Setenv("AGY_CHECK_FORBIDDEN_ENV", "must be scrubbed")
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
