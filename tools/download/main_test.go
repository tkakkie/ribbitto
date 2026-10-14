package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"tailwind", ".."}, true},
		{[]string{"golangci-lint", ".."}, true},
		{[]string{"-paths"}, true},
		{nil, false},
		{[]string{"tailwind"}, false},
		{[]string{"tailwind", ""}, false},
		{[]string{"unknown", ".."}, false},
		{[]string{"tailwind", "..", "extra"}, false},
		{[]string{"-unknown"}, false},
		{[]string{"-paths", "tailwind", ".."}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			tool, root, paths, err := parse(tc.args)
			if (err == nil) != tc.ok {
				t.Fatalf("parse error = %v, want success = %v", err, tc.ok)
			}
			if tc.ok && ((!paths && (tool != tc.args[0] || root != "..")) || (paths && (tool != "" || root != ""))) {
				t.Fatalf("parse = (%q, %q, %v)", tool, root, paths)
			}
		})
	}
}

func TestSelectAsset(t *testing.T) {
	for _, tc := range []struct{ tool, goos, goarch, suffix, hash, path, member string }{
		{"tailwind", "darwin", "arm64", "v4.3.3/tailwindcss-macos-arm64", "cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d", "./bin/tailwindcss-v4.3.3", ""},
		{"tailwind", "linux", "amd64", "v4.3.3/tailwindcss-linux-x64", "dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a", "./bin/tailwindcss-v4.3.3", ""},
		{"golangci-lint", "darwin", "arm64", "v2.14.0/golangci-lint-2.14.0-darwin-arm64.tar.gz", "5ef5f36a7147e91dc58ef9ef4d11bb7bad5ead0c76eb6c01327a73c641d1dcc3", "./bin/golangci-lint-v2.14.0/golangci-lint", "golangci-lint-2.14.0-darwin-arm64/golangci-lint"},
		{"golangci-lint", "linux", "amd64", "v2.14.0/golangci-lint-2.14.0-linux-amd64.tar.gz", "ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab", "./bin/golangci-lint-v2.14.0/golangci-lint", "golangci-lint-2.14.0-linux-amd64/golangci-lint"},
	} {
		t.Run(tc.tool+"/"+tc.goos, func(t *testing.T) {
			a, err := selectAsset(tc.tool, tc.goos, tc.goarch)
			repository := "tailwindlabs/tailwindcss"
			if tc.tool == "golangci-lint" {
				repository = "golangci/golangci-lint"
			}
			want := asset{"https://github.com/" + repository + "/releases/download/" + tc.suffix, tc.hash, tc.path, tc.member}
			if err != nil || a != want {
				t.Fatalf("selectAsset = %+v, %v; want %+v", a, err, want)
			}
		})
	}
	for _, platform := range [][2]string{{"darwin", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}} {
		for _, tool := range []string{"tailwind", "golangci-lint"} {
			if _, err := selectAsset(tool, platform[0], platform[1]); err == nil {
				t.Errorf("accepted %s on %v", tool, platform)
			}
		}
	}
}

func TestInstall(t *testing.T) {
	const member = "release/golangci-lint"
	binary := []byte("test executable")
	truncated := tarball(t, member, tar.TypeReg)
	truncated = truncated[:len(truncated)/2]
	for _, tc := range []struct {
		name, member, want string
		data               []byte
		mismatch           bool
	}{
		{"raw binary", "", "", binary, false},
		{"hash mismatch", "", "SHA-256 mismatch", binary, true},
		{"archive", member, "", tarball(t, member, tar.TypeReg), false},
		{"archive hash mismatch", member, "SHA-256 mismatch", tarball(t, member, tar.TypeReg), true},
		{"bad gzip", member, "opening gzip", binary, false},
		{"truncated archive", member, "unexpected EOF", truncated, false},
		{"missing member", member, "archive missing", tarball(t, "release/README", tar.TypeReg), false},
		{"traversal", member, "archive missing", tarball(t, "../../golangci-lint", tar.TypeReg), false},
		{"symlink", member, "not a regular file", tarball(t, member, tar.TypeSymlink), false},
	} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%v", tc.name, existing), func(t *testing.T) {
				root := t.TempDir()
				a := asset{hash: fmt.Sprintf("%x", sha256.Sum256(tc.data)), path: "bin/tool", member: tc.member}
				if tc.mismatch {
					a.hash = strings.Repeat("0", 64)
				}
				path := filepath.Join(root, a.path)
				if existing {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				err := install(root, a, tc.data)
				if (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want))) {
					t.Fatalf("install = %v, want %q", err, tc.want)
				}
				got, readErr := os.ReadFile(path)
				if tc.want == "" {
					info, statErr := os.Stat(path)
					if readErr != nil || !bytes.Equal(got, binary) || statErr != nil || info.Mode().Perm() != 0o755 {
						t.Fatalf("installed = %q, %v, %v", got, readErr, statErr)
					}
				} else if existing {
					if readErr != nil || string(got) != "old binary" {
						t.Fatalf("changed existing binary: %q, %v", got, readErr)
					}
				} else if !os.IsNotExist(readErr) {
					t.Fatalf("failed install left a binary: %v", readErr)
				}
				leftovers, err := filepath.Glob(filepath.Join(root, "bin", ".download-*"))
				if err != nil || len(leftovers) != 0 {
					t.Fatalf("temporary files left: %v, %v", leftovers, err)
				}
			})
		}
	}
}

func tarball(t *testing.T, name string, kind byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "release/README", Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	body := ""
	if kind == tar.TypeReg {
		body = "test executable"
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Typeflag: kind, Mode: 0o755, Size: int64(len(body)), Linkname: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
