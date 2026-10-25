package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestContainerCopySelection(t *testing.T) {
	tree := containerFixture(t)
	write(t, filepath.Join(tree, ".gitignore"), ".env\nbin/\n*.cache\n")
	for _, name := range []string{".env", "bin/secret", "a.cache", "tracked", "untracked", ".github/workflows/test.yml", "AGENTS.md", "CLAUDE.md", ".gitattributes", ".gitmodules"} {
		write(t, filepath.Join(tree, name), name)
	}
	gitT(t, tree, "add", "tracked", ".github", "AGENTS.md", "CLAUDE.md", ".gitattributes", ".gitmodules")
	// Git must not start a configured fsmonitor hook, or obey inherited Git
	// pointers/configuration, even while examining an untrusted worktree.
	marker := filepath.Join(t.TempDir(), "hooked")
	hook := filepath.Join(tree, "monitor")
	write(t, hook, "#!/bin/sh\ntouch '"+marker+"'\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, tree, "config", "core.fsmonitor", hook)
	t.Setenv("GIT_DIR", "/nonexistent")
	t.Setenv("GIT_WORK_TREE", "/nonexistent")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", hook)
	t.Setenv("HOME", "/nonexistent-caller")
	t.Setenv("PATH", "/nonexistent-caller")
	dest := filepath.Join(t.TempDir(), "tree")
	if err := copyContainerSource(context.Background(), tree, dest, listContainerFiles, nil); err != nil {
		t.Fatal(err)
	}
	var names []string
	err := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(dest, p)
			if err != nil {
				return err
			}
			names = append(names, rel)
		}
		return nil
	})
	want := []string{".gitignore", "check.go", "go.mod", "monitor", "tools/go.mod", "tracked", "untracked"}
	if err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("copied %v, %v; want %v", names, err, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("fsmonitor ran: %v", err)
	}
}

func TestContainerGitGolden(t *testing.T) {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_NO_LAZY_FETCH", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(name, "hostile")
	}
	cmd := containerGit(context.Background(), "/tree", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	want := []string{"/usr/bin/git", "-C", "/tree", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never", "-c", "core.excludesFile=/dev/null", "ls-files", "--cached", "--others", "--exclude-standard", "-z"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("git args %q", cmd.Args)
	}
	if want := []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}; !reflect.DeepEqual(cmd.Env, want) {
		t.Fatalf("git environment %q", cmd.Env)
	}
}

func TestContainerCopyPaths(t *testing.T) {
	for _, tc := range []struct {
		name         string
		omit, refuse bool
	}{
		{"go.mod", false, false}, {"a/b.go", false, false}, {"file with spaces", false, false},
		{".git", true, false}, {".git/config", true, false}, {".GITHUB/workflows/ci.yml", true, false}, {"agents.MD", true, false}, {"CLAUDE.md", true, false}, {".gitattributes", true, false}, {".gitmodules", true, false},
		{"", false, true}, {"/etc/passwd", false, true}, {"../x", false, true}, {"a/../b", false, true}, {"a/./b", false, true}, {"a//b", false, true}, {"a/", false, true}, {"a\\b", false, true}, {"a\x00b", false, true}, {"a\nb", false, true},
		{"a/.git/config", false, true}, {"a/.github/x", false, true}, {"a/AGENTS.md", false, true}, {"a/CLAUDE.md", false, true}, {"a/.gitattributes", false, true}, {"a/.gitmodules", false, true},
		{".github/a/AGENTS.md", true, true}, {".git/a/.git", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			omit, err := containerCopyPath(tc.name)
			if (err != nil) != tc.refuse || err == nil && omit != tc.omit {
				t.Fatalf("%q: omit %v, err %v", tc.name, omit, err)
			}
		})
	}
	for _, name := range []string{".agents", ".agent", "_agents", "_agent", ".gemini", ".claude", ".codex", ".mcp.json", "hooks.json", "plugin.json", "skills.json", "plugins.json", "GEMINI.md", "AGENTS.override.md", "CLAUDE.local.md"} {
		for _, prefix := range []string{"", "nested/", ".github/"} {
			name := prefix + strings.ToUpper(name) + "/file"
			if _, err := containerCopyPath(name); err == nil {
				t.Fatalf("refused name accepted: %q", name)
			}
		}
	}
}

func TestContainerCopyRefusesTypesAndRaces(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		change      func(*testing.T, string)
	}{
		{"symlink before open", "before-open", func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			symlink(t, "/etc/passwd", p)
		}},
		{"FIFO before open", "before-open", func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory before open", "before-open", func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			mkdir(t, p)
		}},
		{"grows during read", "after-stat", func(t *testing.T, p string) { write(t, p, "longer changed content") }},
		{"shrinks during read", "after-stat", func(t *testing.T, p string) { write(t, p, "") }},
		{"same size rewrite", "after-read", func(t *testing.T, p string) { write(t, p, "NEW") }},
		{"rewrite with restored mtime", "after-read", func(t *testing.T, p string) {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			write(t, p, "NEW")
			if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}},
		{"replacement inode", "after-copy", func(t *testing.T, p string) {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			write(t, p+".new", "old")
			if err := os.Chtimes(p+".new", info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(p+".new", p); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink after read", "after-copy", func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			symlink(t, "/etc/passwd", p)
		}},
		{"size after read", "after-copy", func(t *testing.T, p string) { write(t, p, "new size") }},
		{"mtime after read", "after-copy", func(t *testing.T, p string) {
			changed := time.Now().Add(time.Hour)
			if err := os.Chtimes(p, changed, changed); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := os.MkdirTemp("/tmp", "argon-copy-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(tree) })
			tree = resolved(t, tree)
			write(t, filepath.Join(tree, "file"), "old")
			list := func(context.Context, string) ([]string, error) { return []string{"file"}, nil }
			hook := func(stage, name string) {
				if stage == tc.stage && (name == "file" || stage == "after-copy") {
					tc.change(t, filepath.Join(tree, "file"))
				}
			}
			if err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), list, hook); err == nil {
				t.Fatal("unsafe or changed source accepted")
			}
		})
	}
}

func TestContainerCopyRefusesSymlinkedParent(t *testing.T) {
	tree := resolved(t, t.TempDir())
	outside := resolved(t, t.TempDir())
	write(t, filepath.Join(outside, "file"), "must not copy")
	symlink(t, outside, filepath.Join(tree, "sub"))
	list := func(context.Context, string) ([]string, error) { return []string{"sub/file"}, nil }
	dest := filepath.Join(t.TempDir(), "tree")
	if err := copyContainerSource(context.Background(), tree, dest, list, nil); err == nil {
		t.Fatal("symlinked directory followed")
	}
	if _, err := os.Stat(filepath.Join(dest, "sub/file")); !os.IsNotExist(err) {
		t.Fatalf("outside copied: %v", err)
	}
}

func TestContainerCopyRefusesChangedSet(t *testing.T) {
	for _, change := range []string{"added", "removed", "omitted added"} {
		t.Run(change, func(t *testing.T) {
			tree := containerFixture(t)
			hook := func(stage, _ string) {
				if stage != "after-copy" {
					return
				}
				switch change {
				case "added":
					write(t, filepath.Join(tree, "new"), "new")
				case "removed":
					if err := os.Remove(filepath.Join(tree, "check.go")); err != nil {
						t.Fatal(err)
					}
				case "omitted added":
					write(t, filepath.Join(tree, "AGENTS.md"), "omitted")
				}
			}
			if err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), listContainerFiles, hook); err == nil {
				t.Fatal("changed set accepted")
			}
		})
	}
}

func TestContainerCopyLimits(t *testing.T) {
	for _, tc := range []string{"count", "total size", "combined size"} {
		t.Run(tc, func(t *testing.T) {
			tree := resolved(t, t.TempDir())
			files := []string{"a"}
			if tc == "count" {
				files = make([]string, containerFileLimit+1)
				for i := range files {
					files[i] = fmt.Sprintf("file%d", i)
				}
			} else {
				size := int64(containerCopyLimit + 1)
				if tc == "combined size" {
					size = int64(containerCopyLimit/2 + 1)
					files = append(files, "b")
				}
				for _, name := range files {
					p := filepath.Join(tree, name)
					write(t, p, "")
					if err := os.Truncate(p, size); err != nil {
						t.Fatal(err)
					}
				}
			}
			list := func(context.Context, string) ([]string, error) { return files, nil }
			if err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), list, nil); err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("cap: %v", err)
			}
		})
	}
}

func TestContainerCopyModes(t *testing.T) {
	tree := containerFixture(t)
	for _, tc := range []struct {
		name         string
		source, want fs.FileMode
	}{
		{"plain", 0o400 | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky, 0o666},
		{"sub/exec", 0o711 | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky, 0o777},
	} {
		write(t, filepath.Join(tree, tc.name), "content")
		if err := os.Chmod(filepath.Join(tree, tc.name), tc.source); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "tree")
	if err := copyContainerSource(context.Background(), tree, dest, listContainerFiles, nil); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]fs.FileMode{"plain": 0o666, "sub/exec": 0o777, "sub": fs.ModeDir | 0o777} {
		info, err := os.Stat(filepath.Join(dest, name))
		if err != nil || info.Mode() != want {
			t.Fatalf("%s mode: %v, %v; want %v", name, info, err, want)
		}
	}
}

func TestContainerCopyRejectsNestedRepositoryAndSubdirectory(t *testing.T) {
	for _, tc := range []string{"nested repository", "subdirectory"} {
		t.Run(tc, func(t *testing.T) {
			tree := containerFixture(t)
			sub := filepath.Join(tree, "sub")
			write(t, filepath.Join(sub, "file"), "nested")
			if tc == "nested repository" {
				gitT(t, sub, "init", "--quiet")
				gitT(t, sub, "add", "file")
				gitT(t, sub, "commit", "--quiet", "-m", "nested")
			} else {
				tree = sub
			}
			if err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), listContainerFiles, nil); err == nil {
				t.Fatal("nested repository or subdirectory accepted")
			}
		})
	}
}

func TestContainerCopyRefusesHiddenNames(t *testing.T) {
	for _, name := range []string{"empty/.git", "ignored/.GiT", "ignored/.AGENT", ".github/nested/AGENTS.md"} {
		t.Run(name, func(t *testing.T) {
			tree := containerFixture(t)
			write(t, filepath.Join(tree, ".gitignore"), "ignored/\n")
			mkdir(t, filepath.Join(tree, name))
			if err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), listContainerFiles, nil); err == nil {
				t.Fatal("hidden refused name accepted")
			}
		})
	}
}

func TestContainerCopyRefusesRootSwap(t *testing.T) {
	for _, stage := range []string{"after-copy", "after-final-list"} {
		t.Run(stage, func(t *testing.T) {
			tree := containerFixture(t)
			listed, err := listContainerFiles(context.Background(), tree)
			if err != nil {
				t.Fatal(err)
			}
			// The replacement has the same names and bytes. Only the root identity
			// separates it from the held directory whose descriptors were copied.
			hook := func(now, _ string) {
				if now != stage {
					return
				}
				if err := os.Rename(tree, tree+".old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(tree + ".old") })
				mkdir(t, tree)
				for _, name := range listed {
					data, err := os.ReadFile(filepath.Join(tree+".old", name))
					if err != nil {
						t.Fatal(err)
					}
					write(t, filepath.Join(tree, name), string(data))
				}
			}
			// Freeze the listing so the test isolates root identity from Git metadata.
			list := func(context.Context, string) ([]string, error) { return listed, nil }
			if err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), list, hook); err == nil || !strings.Contains(err.Error(), "root changed") {
				t.Fatalf("root replacement: %v", err)
			}
		})
	}
}

func TestContainerCopyRefusesHiddenTypes(t *testing.T) {
	for _, kind := range []string{"symlink", "FIFO", "socket", "device"} {
		for _, name := range []string{".github/x", "AGENTS.md", "bin/x", ".git"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				// Short paths also fit Darwin's Unix socket pathname limit.
				tree, err := os.MkdirTemp("/tmp", "argon-type-*")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(tree) })
				tree = resolved(t, tree)
				gitT(t, tree, "init", "--quiet", "--template=")
				write(t, filepath.Join(tree, ".gitignore"), "bin/\n")
				p := filepath.Join(tree, name)
				if name == ".git" {
					if err := os.RemoveAll(p); err != nil {
						t.Fatal(err)
					}
				}
				mkdir(t, filepath.Dir(p))
				switch kind {
				case "symlink":
					symlink(t, t.TempDir(), p)
				case "FIFO":
					if err := unix.Mkfifo(p, 0o600); err != nil {
						t.Fatal(err)
					}
				case "socket":
					fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer closeFD(fd)
					if err := unix.Bind(fd, &unix.SockaddrUnix{Name: p}); err != nil {
						t.Fatal(err)
					}
				case "device":
					if err := unix.Mknod(p, unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))); err != nil {
						if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
							t.Skipf("device creation requires privileges unavailable to this test: %v", err)
						}
						t.Fatal(err)
					}
				}
				if err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), listContainerFiles, nil); err == nil || !strings.Contains(err.Error(), "not a regular file or directory") {
					t.Fatalf("special source entry accepted or wrong refusal: %v", err)
				}
			})
		}
	}
}

func TestContainerCopyRootGitTypes(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			tree := resolved(t, t.TempDir())
			p := filepath.Join(tree, ".git")
			if kind == "file" {
				write(t, p, "gitdir: /unused\n")
			} else {
				// The metadata directory is exempt from recursive inspection.
				symlink(t, "/unused", filepath.Join(p, "link"))
			}
			list := func(context.Context, string) ([]string, error) { return nil, nil }
			dest := filepath.Join(t.TempDir(), "tree")
			if err := copyContainerSource(context.Background(), tree, dest, list, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
				t.Fatalf("root .git copied: %v", err)
			}
		})
	}
}

func TestContainerGitPromisor(t *testing.T) {
	tree := containerFixture(t)
	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(t.TempDir(), "uploadpack")
	write(t, script, "#!/bin/sh\ntouch '"+marker+"'\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	// A local URL keeps even a regressed fixture independent of the network.
	write(t, filepath.Join(tree, ".git/config"), "[core]\n repositoryformatversion = 1\n[extensions]\n partialClone = origin\n[remote \"origin\"]\n url = "+tree+"\n promisor = true\n partialclonefilter = blob:none\n uploadpack = "+script+"\n")
	if _, err := listContainerFiles(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("promisor uploadpack executed: %v", err)
	}
}

func TestContainerGitTimeout(t *testing.T) {
	tree := containerFixture(t)
	fifo := filepath.Join(tree, ".git/info/exclude")
	mkdir(t, filepath.Dir(fifo))
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Release a regressed, unbounded Git so the assertion fails without
	// hanging the test process. No writer exists before the short deadline.
	release := time.AfterFunc(time.Second, func() {
		fd, err := unix.Open(fifo, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err == nil {
			closeFD(fd)
		}
	})
	defer release.Stop()
	_, err := listContainerFilesWithin(context.Background(), tree, 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "listing source files") {
		t.Fatalf("hanging git did not fail with a listing deadline: %v", err)
	}
	if containerGitTimeout != 30*time.Second {
		t.Fatal("trusted Git deadline changed")
	}
}

func TestContainerModulePaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow bool
	}{
		{"go.mod", true}, {"go.sum", true}, {"GO.MOD", true}, {"GO.SUM", true},
		{"tools/go.mod", true}, {"tools/go.sum", true}, {"tools/tbls/go.mod", true}, {"tools/tbls/go.sum", true},
		{"go.work", false}, {"go.work.sum", false}, {"sub/go.work", false}, {"sub/go.work.sum", false},
		{"GO.WORK", false}, {"sub/GO.WORK.SUM", false},
		{"sub/go.mod", false}, {"sub/go.sum", false}, {"sub/GO.MOD", false}, {"sub/GO.SUM", false},
		{"Tools/go.mod", false}, {"Tools/go.sum", false}, {"tools/GO.MOD", false}, {"tools/GO.SUM", false},
		{"tools/x/go.mod", false}, {"tools/x/go.sum", false}, {"tools/tbls/sub/go.mod", false}, {"tools/tbls/sub/go.sum", false},
		{".github/go.work", false}, {".github/go.work.sum", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := containerCopyPath(tc.name)
			if (err == nil) != tc.allow {
				t.Fatalf("path %q: %v; allow %v", tc.name, err, tc.allow)
			}
		})
	}
}

func TestContainerEntryAllowed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  uint32
		allow bool
	}{
		{"character device", unix.S_IFCHR, false}, {"block device", unix.S_IFBLK, false},
		{"FIFO", unix.S_IFIFO, false}, {"socket", unix.S_IFSOCK, false}, {"symlink", unix.S_IFLNK, false},
		{"regular", unix.S_IFREG, true}, {"directory", unix.S_IFDIR, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := containerEntryAllowed(tc.mode | 0o755); got != tc.allow {
				t.Fatalf("mode %o: allowed %v; want %v", tc.mode, got, tc.allow)
			}
		})
	}
}

func TestContainerNamesRefuseHardLinks(t *testing.T) {
	for _, outside := range []bool{false, true} {
		for _, name := range []string{"listed", "AGENTS.md", "bin/x", ".git"} {
			t.Run(fmt.Sprintf("outside=%v/%s", outside, name), func(t *testing.T) {
				tree := resolved(t, t.TempDir())
				write(t, filepath.Join(tree, ".gitignore"), "bin/\n")
				original := filepath.Join(tree, "original")
				if outside {
					original = filepath.Join(t.TempDir(), "original")
				}
				write(t, original, "must not copy")
				linked := filepath.Join(tree, name)
				mkdir(t, filepath.Dir(linked))
				if err := os.Link(original, linked); err != nil {
					t.Fatal(err)
				}
				list := func(context.Context, string) ([]string, error) { return []string{name}, nil }
				err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), list, nil)
				if err == nil || !strings.Contains(err.Error(), "hard links") {
					t.Fatalf("hard-linked source accepted or wrong refusal: %v", err)
				}
			})
		}
	}
}

func TestContainerStampRefusesHardLinks(t *testing.T) {
	for _, stage := range []string{"before-open", "after-stat", "after-copy"} {
		t.Run(stage, func(t *testing.T) {
			tree := resolved(t, t.TempDir())
			p := filepath.Join(tree, "file")
			write(t, p, "content")
			link := filepath.Join(t.TempDir(), "link")
			list := func(context.Context, string) ([]string, error) { return []string{"file"}, nil }
			hook := func(now, _ string) {
				if now == stage {
					if err := os.Link(p, link); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := copyContainerSource(context.Background(), tree, filepath.Join(t.TempDir(), "tree"), list, hook)
			if err == nil || !strings.Contains(err.Error(), "hard links") {
				t.Fatalf("hard link added during copy accepted or wrong refusal: %v", err)
			}
		})
	}
}
