package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func generateScriptCommand(dir, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.WaitDelay = dir, time.Second
	return cmd.CombinedOutput()
}

func generateScriptFixture(t *testing.T) (dir, names, selection string) {
	t.Helper()
	const header = "old=/work new=/tmp/work names=/tmp/writeback-names\n"
	if containerGenerateLocations != header || strings.Count(containerGenerateSelect, header) != 1 || strings.Count(containerGenerateScript, header) != 1 {
		t.Fatal("generation locations must be one fixed header line")
	}
	root := t.TempDir()
	old, dir, names := filepath.Join(root, "old"), filepath.Join(root, "copy"), filepath.Join(root, "names")
	mkdir(t, old)
	mkdir(t, dir)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	replacement := "old=" + quote(old) + " new=" + quote(dir) + " names=" + quote(names) + "\n"
	selection = strings.Replace(containerGenerateSelect, header, replacement, 1)
	return dir, names, selection
}

func TestContainerGenerateScriptSyntax(t *testing.T) {
	// Parse the exact production argv element, including both nested shells;
	// syntax checking must never execute the prelude or the generators.
	if out, err := generateScriptCommand(t.TempDir(), "/bin/sh", "-n", "-c", containerGenerateScript); err != nil {
		t.Fatalf("script syntax: %v: %s", err, out)
	}
}

func TestContainerGenerateSelection(t *testing.T) {
	dir, names, selection := generateScriptFixture(t)
	old := filepath.Join(filepath.Dir(dir), "old")
	var want []string
	for _, tc := range []struct{ name, before, after string }{
		{"unchanged", "same", "same"}, {"changed", "old", "new"}, {"new", "", "new"},
		{"with spaces", "old", "new"}, {"-leading", "", "new"}, {"embedded\nnewline", "old", "new"}, {"quote'$\\name", "old", "new"},
	} {
		if tc.before != "" {
			write(t, filepath.Join(old, tc.name), tc.before)
		}
		write(t, filepath.Join(dir, tc.name), tc.after)
		if tc.before != tc.after {
			want = append(want, tc.name)
		}
	}
	// Probe capabilities separately: an error in the selection script itself
	// must fail the test, rather than masquerade as an unsupported host tool.
	if out, err := generateScriptCommand(dir, "cmp", "-s", "--", "unchanged", "unchanged"); err != nil {
		t.Skipf("host cmp lacks -s --: %v: %s", err, out)
	}
	if out, err := generateScriptCommand(dir, "find", ".", "-type", "f", "-exec", "/bin/sh", "-c", "exit 0", "sh", "{}", "+"); err != nil {
		t.Skipf("host find lacks -exec ... {} +: %v: %s", err, out)
	}
	out, err := generateScriptCommand(dir, "/bin/sh", "-c", "set -eu\n"+selection)
	if err != nil || len(out) != 0 {
		t.Fatalf("selection: %v: %q", err, out)
	}
	data, err := os.ReadFile(names)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte{0}) {
		t.Fatalf("listing is not NUL terminated: %q", data)
	}
	got := strings.Split(string(data[:len(data)-1]), "\x00")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("selected %q, want %q", got, want)
	}
	t.Run("GNU tar", func(t *testing.T) {
		version, err := generateScriptCommand(dir, "tar", "--version")
		if err != nil || !bytes.Contains(version, []byte("GNU")) {
			t.Skip("host GNU tar unavailable; selection was checked separately")
		}
		archive := "set -eu\nnames=\"$1\"\n" + containerGenerateArchive
		data, err := generateScriptCommand(dir, "/bin/sh", "-c", archive, "sh", names)
		if err != nil {
			t.Fatalf("tar: %v: %s", err, data)
		}
		// Selection preserves backslashes, but the trusted parser deliberately
		// refuses them. Exercise that refusal before round-tripping valid names.
		if _, err := parseWritebackArchive(bytes.NewReader(data)); err == nil || !strings.Contains(err.Error(), "backslash") {
			t.Fatalf("backslash archive: %v", err)
		}
		valid := slices.DeleteFunc(slices.Clone(want), func(s string) bool { return strings.Contains(s, "\\") })
		if err := os.WriteFile(names, []byte(strings.Join(valid, "\x00")+"\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		data, err = generateScriptCommand(dir, "/bin/sh", "-c", archive, "sh", names)
		if err != nil {
			t.Fatalf("tar valid names: %v: %s", err, data)
		}
		entries, err := parseWritebackArchive(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		var listed []string
		for _, entry := range entries {
			listed = append(listed, entry.name)
			if string(entry.content) != "new" {
				t.Fatalf("wrong content for %q", entry.name)
			}
		}
		slices.Sort(listed)
		if !slices.Equal(listed, valid) {
			t.Fatalf("archived %q, want %q", listed, valid)
		}
	})
}

func TestContainerGenerateEmptyArchive(t *testing.T) {
	dir, names, selection := generateScriptFixture(t)
	if err := os.WriteFile(names, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Run only the production archive fragment with an empty list; this must
	// produce the end blocks without consulting tar or executing generators.
	header, _, _ := strings.Cut(selection, "\n")
	out, err := generateScriptCommand(dir, "/bin/sh", "-c", "set -eu\n"+header+"\n"+containerGenerateArchive)
	if err != nil || !bytes.Equal(out, make([]byte, 1024)) {
		t.Fatalf("empty archive: %v, %d bytes", err, len(out))
	}
	entries, err := parseWritebackArchive(bytes.NewReader(out))
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty archive parsed as %d entries: %v", len(entries), err)
	}
}
