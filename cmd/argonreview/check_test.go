package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		status, code int
	}{
		{"listed", "Fetching available models...\ngemini-4-argon-medium\tGemini 4 Argon (Medium)\n" + argonModel + "\tGemini 4 Argon (High)\n", 0, 0},
		{"not listed", "gemini-4-argon-medium\tGemini 4 Argon (Medium)\n", 0, 1},
		{"only a longer ID", argonModel + "-preview\tPreview\n", 0, 1},
		{"only a display name", "other\t" + argonModel + "\n", 0, 1},
		{"agy failed", argonModel + "\tGemini 4 Argon (High)\n", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, bin := t.TempDir(), t.TempDir()
			agy := filepath.Join(bin, "agy")
			write(t, filepath.Join(bin, "listing"), tc.output)
			// The fake records where it ran and with which HOME.
			script := "#!/bin/sh\necho \"$HOME $(pwd -P) $*\" > \"$HOME/seen\"\ncat '" + filepath.Join(bin, "listing") + "'\nexit " + strconv.Itoa(tc.status) + "\n"
			write(t, agy, script)
			if err := os.Chmod(agy, 0o755); err != nil {
				t.Fatal(err)
			}
			home = resolved(t, home)
			code, err := check(home, agy, supervisor{10 * time.Second, time.Second, nil, systemCleanup()})
			if code != tc.code || (err == nil) != (tc.code == 0) {
				t.Fatalf("code = %d, err = %v; want %d", code, err, tc.code)
			}
			seen, err := os.ReadFile(filepath.Join(home, "seen"))
			if err != nil || string(seen) != home+" "+home+" models\n" {
				t.Fatalf("agy ran as %q, %v", seen, err)
			}
		})
	}
}

func TestTimeLimit(t *testing.T) {
	for value, want := range map[string]time.Duration{"": 25 * time.Minute, "90s": 90 * time.Second, "x": 0, "0": 0, "-1s": 0} {
		t.Setenv("RIBBITTO_ARGON_TIMEOUT", value)
		if got, err := timeLimit(); got != want || (err == nil) != (want > 0) {
			t.Fatalf("%q: %v, %v", value, got, err)
		}
	}
}

// check runs only after the trusted-build check: an unstamped test binary
// is refused before prepare or agy run.
func TestCheckChecksTheBuildFirst(t *testing.T) {
	home, bin := resolved(t, t.TempDir()), t.TempDir()
	t.Setenv("RIBBITTO_ARGON_HOME", home)
	// An agy on PATH, so that prepare would write and agy would run if check
	// came first.
	write(t, filepath.Join(bin, "agy"), "#!/bin/sh\ntouch \"$HOME/ran\"\n")
	if err := os.Chmod(filepath.Join(bin, "agy"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stderr bytes.Buffer
	if code := run([]string{"check"}, nil, &bytes.Buffer{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "argonreview: ") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("HOME holds %v, %v", entries, err)
	}
}
