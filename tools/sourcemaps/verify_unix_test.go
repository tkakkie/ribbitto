//go:build unix

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestVerifyFilesystemRefusals(t *testing.T) {
	now, sha := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), shaOf(1)
	s := &mockStore{token: "tok"}
	srv := s.server(t)
	defer srv.Close()
	mapDir, _, _ := writeMapDir(t, sha, "10")
	if err := runStore(t.Context(), testCfg(srv.URL, "tok", now, io.Discard), mapDir, sha, "10", "1"); err != nil {
		t.Fatal(err)
	}
	escaped := "x\x1b[31m\n.js"
	for _, tc := range []struct {
		name, file, want string
		create           func(string) error
	}{
		{"directory", "b.js", "not a regular file", func(p string) error { return os.Mkdir(p, 0755) }},
		{"FIFO", "x.js", "not a regular file", func(p string) error { return syscall.Mkfifo(p, 0600) }},
		{"stat error", escaped, "checking distributed bundle", func(p string) error { return os.Symlink(p+".missing", p) }},
		{"read error", escaped, "reading distributed bundle", func(p string) error { return os.WriteFile(p, []byte("x"), 0000) }},
		{"mismatch", escaped, "does not match " + safeQuote(s.releases[0].Name), func(p string) error { return os.WriteFile(p, []byte("x"), 0644) }},
		{"missing directory", "", "reading distribution directory", os.Remove},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "read error" && os.Geteuid() == 0 {
				t.Skip("root can read mode-000 files")
			}
			dir := filepath.Join(t.TempDir(), "bundles\x1b[31m\n")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := tc.create(filepath.Join(dir, tc.file)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			var out bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- runVerify(ctx, testCfg(srv.URL, "tok", now, &out), dir, sha, "10", "1") }()
			select {
			case err := <-done:
				if err == nil || out.Len() != 0 {
					t.Fatalf("must refuse without a receipt: err=%v output=%s", err, out.String())
				}
				if msg := err.Error(); !strings.Contains(msg, tc.want) || strings.ContainsAny(msg, "\x1b\n") || (tc.file != "" && !strings.Contains(msg, safeQuote(tc.file))) || (tc.name != "mismatch" && !strings.Contains(msg, safeQuote(dir))) {
					t.Fatalf("unexpected or unescaped error: %q", msg)
				}
			case <-ctx.Done():
				t.Fatal("verification blocked on a filesystem entry")
			}
		})
	}
}
