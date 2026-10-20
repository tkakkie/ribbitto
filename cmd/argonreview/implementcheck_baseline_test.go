package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func implementCheckTrustTree(t *testing.T, f *implementCheckTestFixture) {
	t.Helper()
	implementCheckGitOK(t, f.tree, "add", "--all")
	implementCheckGitOK(t, f.tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "trusted baseline")
	implementCheckGitOK(t, f.tree, "update-ref", "refs/remotes/origin/main", "HEAD")
}

func TestImplementCheckFilterMarker(t *testing.T) {
	for _, rel := range []string{"AGENTS.md", ".github/trusted"} {
		for _, state := range []string{"modified", "unchanged", "CRLF changed", "CRLF unchanged"} {
			t.Run(rel+"/"+state, func(t *testing.T) {
				f, repo := implementCheckGitFixture(t, true)
				trusted := "baseline\n"
				if strings.HasPrefix(state, "CRLF") {
					trusted = "baseline\r\n"
				}
				implementCheckWrite(t, filepath.Join(f.tree, rel), trusted)
				implementCheckWrite(t, filepath.Join(f.tree, ".gitattributes"), rel+" filter=x -text\n")
				implementCheckTrustTree(t, f)
				marker, script, source := filepath.Join(f.root, "marker"), filepath.Join(f.root, "clean"), filepath.Join(f.root, "trusted")
				implementCheckWrite(t, source, trusted)
				implementCheckWrite(t, script, "#!/bin/sh\nprintf ran > '"+marker+"'\n/bin/cat '"+source+"'\n")
				if err := os.Chmod(script, 0700); err != nil {
					t.Fatal(err)
				}
				implementCheckWrite(t, filepath.Join(repo, ".git/info/attributes"), rel+" filter=x text\n")
				implementCheckGitOK(t, repo, "config", "filter.x.clean", "'"+script+"'")
				implementCheckGitOK(t, repo, "config", "core.autocrlf", "true")
				data := trusted
				changed := state == "modified" || state == "CRLF changed"
				if state == "modified" {
					data = "modified\n"
				}
				if state == "CRLF changed" {
					data = "baseline\n"
				}
				implementCheckWrite(t, filepath.Join(f.tree, rel), data)
				future := time.Now().Add(time.Hour)
				if err := os.Chtimes(filepath.Join(f.tree, rel), future, future); err != nil {
					t.Fatal(err)
				}
				problems, out := implementCheckBaselineRun(f)
				if (problems != 0) != changed {
					t.Errorf("changed %t, problems %d: %s", changed, problems, out)
				}
				if changed && !strings.Contains(out, fmt.Sprintf("%q differs from origin/main", rel)) {
					t.Error(out)
				}
				if _, err := os.Lstat(marker); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("filter executed: %v", err)
				}
			})
		}
	}
}

func TestImplementCheckRawCRLF(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, repo := implementCheckGitFixture(t, true)
			implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "baseline\r\n")
			implementCheckTrustTree(t, f)
			implementCheckGitOK(t, repo, "config", "core.autocrlf", "true")
			if changed {
				implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "baseline\n")
			}
			problems, out := implementCheckBaselineRun(f)
			if (problems != 0) != changed {
				t.Fatal(out)
			}
		})
	}
}

func TestImplementCheckIndexEntries(t *testing.T) {
	for _, state := range []string{"mode", "oid", "stage 1", "stage 2", "stage 3"} {
		t.Run(state, func(t *testing.T) {
			f, _ := implementCheckGitFixture(t, true)
			oid := strings.TrimSpace(implementCheckGitOK(t, f.tree, "rev-parse", "HEAD:AGENTS.md"))
			switch state {
			case "mode":
				implementCheckGitOK(t, f.tree, "update-index", "--cacheinfo", "100755,"+oid+",AGENTS.md")
			case "oid":
				other := strings.TrimSpace(implementCheckGitOK(t, f.tree, "rev-parse", "HEAD:.github/trusted"))
				implementCheckGitOK(t, f.tree, "update-index", "--cacheinfo", "100644,"+other+",AGENTS.md")
			default:
				// Fixed stdin creates conflict stages without shell interpolation.
				implementCheckIndexStage(t, f, oid, strings.TrimPrefix(state, "stage "))
			}
			problems, out := implementCheckBaselineRun(f)
			if problems == 0 || !strings.Contains(out, "index") {
				t.Fatal(out)
			}
		})
	}
}

func TestImplementCheckWorktreeEntries(t *testing.T) {
	for _, state := range []string{
		"executable", "nonexecutable", "same size", "symlink file", "FIFO file", "symlink directory", "FIFO github",
		"untracked", "ignored gitignore", "ignored exclude", "absent attributes", "absent modules",
		"empty gemini", "empty agents", "empty workflows", "file becomes directory", "directory becomes file",
		"removed file", "removed directory", "copied extra", "copied mode", "case github", "case agents", "case child",
	} {
		t.Run(state, func(t *testing.T) {
			f, repo := implementCheckGitFixture(t, true)
			implementCheckWrite(t, filepath.Join(f.tree, ".github/workflows/x.yml"), "workflow")
			if state == "nonexecutable" {
				if err := os.Chmod(filepath.Join(f.tree, "AGENTS.md"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			implementCheckTrustTree(t, f)
			agent := filepath.Join(f.tree, "AGENTS.md")
			github := filepath.Join(f.tree, ".github")
			switch state {
			case "executable", "copied mode":
				if state == "copied mode" {
					implementCheckRemove(t, agent)
					implementCheckWrite(t, agent, "baseline")
				}
				if err := os.Chmod(agent, 0700); err != nil {
					t.Fatal(err)
				}
			case "nonexecutable":
				if err := os.Chmod(agent, 0600); err != nil {
					t.Fatal(err)
				}
			case "same size":
				implementCheckWrite(t, agent, "BASELINE")
			case "symlink file":
				implementCheckRemove(t, agent)
				implementCheckLink(t, filepath.Join(repo, "AGENTS.md"), agent)
			case "FIFO file":
				implementCheckRemove(t, agent)
				if err := unix.Mkfifo(agent, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink directory":
				implementCheckRemove(t, filepath.Join(github, "workflows"))
				implementCheckLink(t, repo, filepath.Join(github, "workflows"))
			case "FIFO github":
				if err := unix.Mkfifo(filepath.Join(github, "fifo"), 0600); err != nil {
					t.Fatal(err)
				}
			case "untracked", "ignored gitignore", "ignored exclude":
				implementCheckWrite(t, filepath.Join(github, "extra"), "extra")
				if state == "ignored gitignore" {
					implementCheckWrite(t, filepath.Join(f.tree, ".gitignore"), ".github/extra\n")
				}
				if state == "ignored exclude" {
					implementCheckWrite(t, filepath.Join(repo, ".git/info/exclude"), ".github/extra\n")
				}
			case "absent attributes", "absent modules":
				rel := ".gitattributes"
				if state == "absent modules" {
					rel = ".gitmodules"
				}
				implementCheckWrite(t, filepath.Join(f.tree, rel), "hidden")
				implementCheckWrite(t, filepath.Join(repo, ".git/info/exclude"), rel+"\n")
			case "empty gemini":
				implementCheckMkdir(t, filepath.Join(github, ".gemini"))
			case "empty agents":
				implementCheckMkdir(t, filepath.Join(github, "agents"))
			case "empty workflows":
				implementCheckMkdir(t, filepath.Join(github, "workflows/extra"))
			case "file becomes directory":
				implementCheckRemove(t, agent)
				implementCheckMkdir(t, agent)
			case "directory becomes file":
				implementCheckRemove(t, filepath.Join(github, "workflows"))
				implementCheckWrite(t, filepath.Join(github, "workflows"), "workflow")
			case "removed file":
				implementCheckRemove(t, filepath.Join(github, "trusted"))
			case "removed directory":
				implementCheckRemove(t, filepath.Join(github, "workflows"))
			case "copied extra":
				implementCheckWrite(t, filepath.Join(github, "copy"), "trusted")
			case "case github":
				implementCheckCaseRename(t, github, filepath.Join(f.tree, ".GITHUB"))
			case "case agents":
				implementCheckCaseRename(t, agent, filepath.Join(f.tree, "Agents.md"))
			case "case child":
				implementCheckCaseRename(t, filepath.Join(github, "trusted"), filepath.Join(github, "Trusted"))
			}
			problems, out := implementCheckBaselineRun(f)
			if problems == 0 {
				t.Fatalf("accepted %s: %s", state, out)
			}
		})
	}
}

// Probe actual rename spelling, including on case-insensitive hosts; skip only
// when the filesystem cannot represent the requested distinct spelling.
func implementCheckCaseRename(t *testing.T, from, to string) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "case-probe")
	if err := os.WriteFile(probe, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(probe, filepath.Join(filepath.Dir(probe), "CASE-PROBE")); err != nil {
		t.Skipf("case rename probe unsupported: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(probe))
	if err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, entry := range entries {
		if entry.Name() == "CASE-PROBE" {
			preserved = true
		}
	}
	if !preserved {
		t.Skip("filesystem probe did not preserve case rename spelling")
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}

}

func TestImplementCheckMissingReferenceObject(t *testing.T) {
	for _, rel := range []string{".github/trusted", "AGENTS.md", "tree"} {
		t.Run(rel, func(t *testing.T) {
			f, repo := implementCheckGitFixture(t, true)
			rev := "HEAD:" + rel
			if rel == "tree" {
				rev = "HEAD^{tree}"
			}
			oid := strings.TrimSpace(implementCheckGitOK(t, repo, "rev-parse", rev))
			implementCheckRemove(t, filepath.Join(repo, ".git/objects", oid[:2], oid[2:]))
			problems, out := implementCheckBaselineRun(f)
			if problems == 0 || strings.Contains(out, "does not track") {
				t.Fatal(out)
			}
			want := "checking baseline object"
			if rel == "tree" {
				want = "cannot list baseline"
			}
			if !strings.Contains(out, want) {
				t.Fatal(out)
			}

		})
	}
}

func TestImplementCheckObjectFormat(t *testing.T) {
	for _, format := range []string{"sha256\n", "sha512\n", "", "sha1", "sha256", "sha1\n\n", "error", "wrong length"} {
		t.Run(fmt.Sprintf("%q", format), func(t *testing.T) {
			f := implementCheckBaselineFake(t, true)
			original := f.deps.git
			calls := 0
			data := []byte("baseline")
			blob := append([]byte("blob 8\x00"), data...)
			oid := fmt.Sprintf("%x", sha256.Sum256(blob))
			f.deps.git = func(args ...string) ([]byte, int, error) {
				calls++
				if args[0] == "rev-parse" {
					if format == "error" {
						return nil, -1, fs.ErrPermission
					}
					if format == "wrong length" {
						return []byte("sha256\n"), 0, nil
					}
					return []byte(format), 0, nil
				}
				if args[0] == "merge-base" {
					return []byte(strings.Repeat("a", 64) + "\n"), 0, nil
				}
				if args[len(args)-1] == "AGENTS.md" {
					entryOID := oid
					if format == "wrong length" {
						entryOID = strings.Repeat("a", 40)
					}
					if args[0] == "ls-tree" {
						return []byte("100644 blob " + entryOID + "\tAGENTS.md\x00"), 0, nil
					}
					if args[0] == "ls-files" && args[1] == "-s" {
						return []byte("100644 " + oid + " 0\tAGENTS.md\x00"), 0, nil
					}
				}
				return original(args...)
			}
			problems, out := implementCheckBaselineRun(f)
			if format == "sha256\n" {
				if problems != 0 {
					t.Fatal(out)
				}
			} else {
				if problems == 0 {
					t.Fatalf("accepted object format: %s", out)
				}
				if format == "wrong length" && !strings.Contains(out, "cannot list baseline") {
					t.Fatal(out)
				}
				if format != "wrong length" && calls != 1 {
					t.Fatalf("continued after bad format: %d %s", calls, out)
				}
			}
		})
	}
}

func TestImplementCheckRealSHA256(t *testing.T) {
	f := implementCheckFixture(t)
	repo := filepath.Join(f.root, "repo")
	implementCheckMkdir(t, repo)
	if _, status, err := implementGit(repo)("init", "--object-format=sha256", "--initial-branch=main"); err != nil || status != 0 {
		t.Skipf("SHA-256 init unsupported: %d %v", status, err)
	}
	implementCheckWrite(t, filepath.Join(repo, "AGENTS.md"), "baseline")
	implementCheckGitOK(t, repo, "add", ".")
	implementCheckGitOK(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "baseline")
	implementCheckGitOK(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	implementCheckRemove(t, f.tree)
	implementCheckGitOK(t, repo, "worktree", "add", "--detach", f.tree, "HEAD")
	data, err := os.ReadFile(filepath.Join(f.tree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	implementCheckWrite(t, f.saved, string(data))
	f.deps.git = implementGit(f.tree)
	if problems, out := implementCheckBaselineRun(f); problems != 0 {
		t.Fatal(out)
	}
	implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "modified")
	if problems, out := implementCheckBaselineRun(f); problems == 0 {
		t.Fatal(out)
	}
}

func implementCheckIndexStage(t *testing.T, f *implementCheckTestFixture, oid, stage string) {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", "-C", f.tree, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "update-index", "--index-info")
	cmd.Env = implementGitEnv()
	cmd.Stdin = strings.NewReader("0 " + strings.Repeat("0", len(oid)) + "\tAGENTS.md\n100644 " + oid + " " + stage + "\tAGENTS.md\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("index stage: %v %s", err, out)
	}
}

func TestImplementCheckBaselineFilesystemErrors(t *testing.T) {
	for _, state := range []string{"root listing", "directory listing", "file stat", "absent stat", "read symlink", "read cap"} {
		t.Run(state, func(t *testing.T) {
			f, _ := implementCheckGitFixture(t, true)
			agent := filepath.Join(f.tree, "AGENTS.md")
			switch state {
			case "root listing", "directory listing":
				target := f.tree
				if state == "directory listing" {
					target = filepath.Join(f.tree, ".github")
				}
				f.deps.readDir = func(p string) ([]os.DirEntry, error) {
					entries, err := os.ReadDir(p)
					if p == target {
						return entries, fs.ErrPermission
					}
					return entries, err
				}
			case "file stat", "absent stat":
				target := agent
				if state == "absent stat" {
					target = filepath.Join(f.tree, ".gitmodules")
				}
				f.deps.lstat = func(p string) (fs.FileInfo, error) {
					if p == target {
						return nil, fs.ErrPermission
					}
					return os.Lstat(p)
				}
			case "read symlink":
				source := filepath.Join(f.root, "source")
				implementCheckWrite(t, source, "baseline")
				implementCheckRemove(t, agent)
				implementCheckLink(t, source, agent)
				f.deps.lstat = func(p string) (fs.FileInfo, error) {
					if p == agent {
						return os.Stat(p)
					}
					return os.Lstat(p)
				}
			case "read cap":
				implementCheckWrite(t, agent, strings.Repeat("x", (1<<20)+1))
			}
			problems, out := implementCheckBaselineRun(f)
			if problems == 0 {
				t.Fatal(out)
			}
			want := "permission denied"
			if state == "read symlink" {
				want = "reading baseline"
			}
			if state == "read cap" {
				want = "exceeds"
			}
			if !strings.Contains(out, want) {
				t.Fatal(out)
			}
		})
	}
}

func TestImplementCheckBaselineSets(t *testing.T) {
	for _, layer := range []string{"HEAD", "-s"} {
		for _, change := range []string{"missing", "extra", "mode", "oid"} {
			t.Run(layer+"/"+change, func(t *testing.T) {
				f := implementCheckBaselineFake(t, true)
				original := f.deps.git
				f.deps.git = func(args ...string) ([]byte, int, error) {
					target := "AGENTS.md"
					if change == "extra" {
						target = "CLAUDE.md"
					}
					if implementCheckGitStage(args) != layer || args[len(args)-1] != target {
						return original(args...)
					}
					if change == "missing" {
						return nil, 0, nil
					}
					mode, oid := "100644", implementBlobOID([]byte("baseline"), "sha1")
					if change == "mode" {
						mode = "100755"
					}
					if change == "oid" {
						oid = strings.Repeat("b", 40)
					}
					record := mode + " blob " + oid
					if layer == "-s" {
						record = mode + " " + oid + " 0"
					}
					return []byte(record + "\t" + target + "\x00"), 0, nil
				}
				problems, out := implementCheckBaselineRun(f)
				want := "differs from origin/main in HEAD"
				if layer == "-s" {
					want = "index differs"
					if change == "extra" {
						want = "in the index but origin/main does not track it"
					}
				}
				if problems == 0 || !strings.Contains(out, want) {
					t.Fatal(out)
				}
			})
		}
	}
}

func TestImplementCheckCaseVariants(t *testing.T) {
	for _, layer := range []string{"HEAD", "index"} {
		for _, rel := range []string{"claude.md", "Agents.md", ".GITHUB/workflows/e.yml", ".GitAttributes", ".GitModules", ".github/Workflows/x.yml"} {
			t.Run(layer+"/"+rel, func(t *testing.T) {
				f, repo := implementCheckGitFixture(t, true)
				// Config is shared by the repository and its linked worktrees.
				implementCheckGitOK(t, repo, "config", "core.ignorecase", "true")
				oid := strings.TrimSpace(implementCheckGitOK(t, f.tree, "rev-parse", "HEAD:.github/trusted"))
				// Insert without touching disk, so case-insensitive hosts can keep
				// Agents.md beside the existing, unchanged AGENTS.md baseline.
				implementCheckGitOK(t, f.tree, "update-index", "--add", "--cacheinfo", "100644,"+oid+","+rel)
				base, _, _ := strings.Cut(rel, "/")
				switch strings.ToLower(base) {
				case ".github":
					base = ".github"
				case "agents.md":
					base = "AGENTS.md"
				case "claude.md":
					base = "CLAUDE.md"
				default:
					base = strings.ToLower(base)
				}
				// Pin both supported magic forms against real Git, including a
				// directory's descendants. Exact pathspecs miss root variants.
				literal := implementCheckGitOK(t, f.tree, "ls-files", "-z", "--", ":(icase,literal)"+base)
				icase := implementCheckGitOK(t, f.tree, "ls-files", "-z", "--", ":(icase)"+base)
				if literal != icase || !strings.Contains(literal, rel+"\x00") {
					t.Fatalf("icase pathspecs missed %q: literal %q, icase %q", rel, literal, icase)
				}
				want := "baseline index spelling differs"
				if rel == ".github/Workflows/x.yml" {
					want = `".github" index differs from the merge base`
				}
				if layer == "HEAD" {
					implementCheckGitOK(t, f.tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "case variant")
					want = "baseline HEAD spelling differs"
					if rel == ".github/Workflows/x.yml" {
						want = `".github" differs from origin/main in HEAD`
					}
				}
				problems, out := implementCheckBaselineRun(f)
				if problems == 0 || !strings.Contains(out, want) {
					t.Fatalf("want %q: %s", want, out)
				}
				if layer == "HEAD" {
					// Also isolate HEAD from the index: restore the reference index
					// while leaving the committed variant absent from disk.
					implementCheckGitOK(t, f.tree, "update-index", "--force-remove", "--", rel)
					problems, out = implementCheckBaselineRun(f)
					if problems == 0 || !strings.Contains(out, want) {
						t.Fatalf("HEAD alone, want %q: %s", want, out)
					}
				}
			})
		}
	}
	t.Run("exact baseline", func(t *testing.T) {
		f, repo := implementCheckGitFixture(t, true)
		implementCheckGitOK(t, repo, "config", "core.ignorecase", "true")
		if problems, out := implementCheckBaselineRun(f); problems != 0 {
			t.Fatal(out)
		}
	})
}

func TestImplementCheckSpellingOutput(t *testing.T) {
	oid := strings.Repeat("a", 40)
	root := "100644 blob " + oid + "\tAGENTS.md\x00"
	for _, tc := range []struct{ name, stage, data string }{
		{"root truncated", "HEAD root", strings.TrimSuffix(root, "\x00")},
		{"root missing tab", "HEAD root", strings.ReplaceAll(root, "\t", " ")},
		{"root double space", "HEAD root", strings.Replace(root, " ", "  ", 1)},
		{"root empty name", "HEAD root", strings.ReplaceAll(root, "AGENTS.md", "")},
		{"root dot", "HEAD root", strings.ReplaceAll(root, "AGENTS.md", ".")},
		{"root dotdot", "HEAD root", strings.ReplaceAll(root, "AGENTS.md", "..")},
		{"root slash", "HEAD root", strings.ReplaceAll(root, "AGENTS.md", "dir/file")},
		{"root duplicate", "HEAD root", root + root},
		{"root mode", "HEAD root", strings.ReplaceAll(root, "100644", "100600")},
		{"root type", "HEAD root", strings.ReplaceAll(root, "blob", "tree")},
		{"root oid", "HEAD root", strings.ReplaceAll(root, oid, "short")},
		{"index truncated", "index spelling", "AGENTS.md"},
		{"index empty", "index spelling", "\x00"},
		{"index wrong root", "index spelling", "other\x00"},
		{"index traversal", "index spelling", "AGENTS.md/../other\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := implementCheckBaselineFake(t, true)
			original := f.deps.git
			f.deps.git = func(args ...string) ([]byte, int, error) {
				if implementCheckGitStage(args) == tc.stage && (tc.stage == "HEAD root" || args[len(args)-1] == ":(icase,literal)AGENTS.md") {
					return []byte(tc.data), 0, nil
				}
				return original(args...)
			}
			problems, out := implementCheckBaselineRun(f)
			want := "malformed baseline " + tc.stage + " record"
			if problems == 0 || !strings.Contains(out, want) {
				t.Fatalf("want %q: %s", want, out)
			}
		})
	}
}

func TestImplementCheckBaselineFilenameContract(t *testing.T) {
	for _, tc := range []struct{ name, rel string }{
		{"backslash", `.github/work\flow.yml`},
		{"control", ".github/work\x01flow.yml"},
		{"newline", ".github/work\nflow.yml"},
		{"DEL", ".github/work\x7fflow.yml"},
		{"invalid UTF-8", ".github/work\xffflow.yml"},
		{"nonprintable Unicode", ".github/work\u200bflow.yml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := implementCheckBaselineFake(t, false)
			original := f.deps.git
			f.deps.git = func(args ...string) ([]byte, int, error) {
				if implementCheckGitStage(args) == "reference" && args[len(args)-1] == ".github" {
					return []byte("100644 blob " + strings.Repeat("a", 40) + "\t" + tc.rel + "\x00"), 0, nil
				}
				return original(args...)
			}
			problems, out := implementCheckBaselineRun(f)
			// Require the parsing diagnostic: a later filesystem mismatch must
			// not stand in for the filename contract's refusal.
			if problems == 0 || !strings.Contains(out, `cannot list baseline ".github" (ls-tree -r)`) {
				t.Fatal(out)
			}
		})
	}
}
