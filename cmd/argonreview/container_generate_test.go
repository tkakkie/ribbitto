package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContainerGenerateBoundary(t *testing.T) {
	if !reflect.DeepEqual(verifyCheckNames(), []string{"build", "vet", "test", "generated"}) {
		t.Fatal("verify names changed")
	}
	schema := verifyInputSchema()["properties"].(map[string]any)[verifyCheckProperty].(map[string]any)["enum"]
	if !reflect.DeepEqual(schema, verifyCheckNames()) {
		t.Fatal("MCP schema changed")
	}
	for _, check := range []string{"generate", "container-generate", "writeback"} {
		if validateContainerCheck("tree", check, "./...") == nil {
			t.Fatalf("check accepted %s", check)
		}
		args := []byte(`{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"` + check + `","package":"./..."}}`)
		input := append([]byte(`{"toolCall":{"name":"call_mcp_tool","args":`), args...)
		input = append(input, []byte(`}}`)...)
		if implementVerifyCall(input, args) == nil {
			t.Fatal("guard admitted write-back")
		}
	}
	for _, args := range [][]string{{"container-generate"}, {"container-generate", "/tree", "extra"}, {"container-generate", "/tree"}} {
		var out, stderr bytes.Buffer
		want := 2
		if len(args) == 2 {
			want = 1
		}
		if code := run(args, nil, &out, &stderr); code != want || stderr.Len() == 0 {
			t.Fatalf("dispatch %v: %d, %s", args, code, stderr.String())
		}
	}
	want := containerArguments("copy", "name", "vet", "./...")
	want = append(want[:len(want)-4:len(want)-4], containerGenerateScript)
	if got := containerArguments("copy", "name", "writeback", "ignored"); !reflect.DeepEqual(got, want) {
		t.Fatal("generation flags differ")
	}
	for _, fragment := range []string{containerCopyScript, containerTemplCommand + " >&2 || exit 31", containerSQLCCommand + " >&2 || exit 32", containerCSSCommand + " >&2 || exit 33", "find . -type f -exec", `cmp -s -- "$old/$name" "$file"`, `printf "%s\0" "$name"`, "*) exit 34", "tar --format=ustar --null --verbatim-files-from --no-recursion"} {
		if !strings.Contains(containerGenerateScript, fragment) {
			t.Fatalf("missing script contract %q", fragment)
		}
	}
}

func TestContainerGeneratePreDocker(t *testing.T) {
	for _, kind := range []string{"relative", "unclean", "symlink root", "symlink elsewhere", "symlink in git", "sqlc changed", "sqlc missing", "sqlc too big", "sqlc absent at base", "no merge base", "git failure"} {
		t.Run(kind, func(t *testing.T) {
			tree := writebackFixture(t, "sha1")
			switch kind {
			case "relative":
				tree = "."
			case "unclean":
				tree += "/."
			case "symlink root":
				link := filepath.Join(resolved(t, t.TempDir()), "link")
				symlink(t, tree, link)
				tree = link
			case "symlink elsewhere":
				symlink(t, "/unused", filepath.Join(tree, "ignored/link"))
			case "symlink in git":
				symlink(t, "/unused", filepath.Join(tree, ".git/link"))
			case "sqlc changed":
				write(t, filepath.Join(tree, "sqlc.yaml"), "changed")
			case "sqlc missing":
				if err := os.Remove(filepath.Join(tree, "sqlc.yaml")); err != nil {
					t.Fatal(err)
				}
			case "sqlc too big":
				if err := os.Truncate(filepath.Join(tree, "sqlc.yaml"), writebackFileLimit+1); err != nil {
					t.Fatal(err)
				}
			case "sqlc absent at base":
				writebackGitT(t, tree, "rm", "--quiet", "sqlc.yaml")
				writebackGitT(t, tree, "commit", "--quiet", "-m", "absent")
				writebackGitT(t, tree, "update-ref", "refs/remotes/origin/main", "HEAD")
				write(t, filepath.Join(tree, "sqlc.yaml"), "version: 2\n")
			case "no merge base":
				writebackGitT(t, tree, "update-ref", "-d", "refs/remotes/origin/main")
			case "git failure":
				write(t, filepath.Join(tree, ".git/config"), "[broken")
			}
			sweep := func(io.Writer) error { t.Fatal("sweep/Docker reached before refusal"); return nil }
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Fatal("Docker reached")
				return 0, nil
			}}
			report, err := runContainerGenerate(context.Background(), tree, sweep, runner, t.TempDir(), nil)
			if err == nil || !strings.Contains(report, "0 written, 0 identical") || !strings.Contains(report, "refused:") {
				t.Fatalf("gate accepted: %s, %v", report, err)
			}
		})
	}
}

func TestContainerGenerateResults(t *testing.T) {
	for _, kind := range []string{"success", "empty", "nonzero", "templ", "sqlc", "css", "runner", "cancel", "late cancel", "overflow", "cleanup", "source cleanup", "bad tar", "stderr tar", "outside path", "sweep", "trust"} {
		t.Run(kind, func(t *testing.T) {
			tree := writebackFixture(t, "sha1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entries := []writebackEntry{{writebackX, []byte(writebackBanner + "new x")}, {writebackY, []byte(writebackBanner + "new y")}}
			if kind == "empty" {
				entries = nil
			}
			if kind == "outside path" {
				entries = append(entries, writebackEntry{"AGENTS.md", []byte("evil")})
			}
			data := writebackTestArchive(t, entries, tar.FormatUSTAR, nil)
			if kind == "bad tar" {
				data = []byte("not tar")
			}
			var calls []string
			copyDir := ""
			runner := fakeContainerRunner{func(_ context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
				calls = append(calls, args[0])
				if !reflect.DeepEqual(env, dockerEnvironment()) {
					t.Fatal("environment differs")
				}
				if args[0] != "run" {
					writebackContentT(t, tree, writebackX, "old x") // No writes before confirmed removal.
					if args[0] == "rm" && kind == "late cancel" {
						cancel()
					}
					if kind == "cleanup" {
						return 1, errors.New("cleanup failed")
					}
					return 0, nil
				}
				for _, arg := range args {
					if strings.HasSuffix(arg, ",dst=/work,readonly") {
						copyDir = strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
					}
				}
				if args[len(args)-1] != containerGenerateScript {
					t.Fatal("wrong script")
				}
				if kind == "source cleanup" {
					if err := os.Chmod(copyDir, 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(copyDir, 0o700); _ = os.RemoveAll(filepath.Dir(copyDir)) })
				}
				if kind == "stderr tar" {
					_, _ = stderr.Write(data)
				} else {
					_, _ = stdout.Write(data)
				}
				_, _ = io.WriteString(stderr, "untrusted diagnostic\x1b")
				switch kind {
				case "overflow":
					_, _ = io.WriteString(stderr, strings.Repeat("x", containerOutputLimit))
				case "runner":
					return -1, errors.New("runner failed")
				case "cancel":
					cancel()
				case "nonzero":
					return 1, nil
				case "templ":
					return 31, nil
				case "sqlc":
					return 32, nil
				case "css":
					return 33, nil
				}
				return 0, nil
			}}
			sweep := func(io.Writer) error {
				if kind == "sweep" {
					return errors.New("sweep failed")
				}
				return nil
			}
			var report string
			var err error
			if kind == "trust" {
				var out, stderr bytes.Buffer
				if code := startContainerGenerate(tree, &out, &stderr, errors.New("untrusted"), func(io.Writer) error { t.Fatal("sweep reached"); return nil }, runner, t.TempDir(), nil); code != 1 {
					t.Fatal("trust accepted")
				}
				report, err = out.String(), errors.New("untrusted")
			} else {
				report, err = runContainerGenerate(ctx, tree, sweep, runner, t.TempDir(), nil)
			}
			success := kind == "success" || kind == "empty"
			if (err == nil) != success || strings.Contains(report, "untrusted diagnostic") || strings.ContainsRune(report, '\x1b') {
				t.Fatalf("%s: %s, %v", kind, report, err)
			}
			if kind == "trust" || kind == "sweep" {
				if len(calls) != 0 {
					t.Fatalf("Docker calls %v", calls)
				}
			} else if !reflect.DeepEqual(calls, []string{"run", "stop", "rm"}) {
				t.Fatalf("cleanup calls %v", calls)
			}
			if kind == "success" {
				for _, e := range entries {
					writebackContentT(t, tree, e.name, string(e.content))
				}
				// A following generated run must copy exactly the written content.
				next := fakeContainerRunner{func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
					if args[0] == "run" {
						for _, arg := range args {
							if strings.HasSuffix(arg, ",dst=/work,readonly") {
								copy := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
								for _, e := range entries {
									writebackContentT(t, copy, e.name, string(e.content))
								}
							}
						}
					}
					return 0, nil
				}}
				if result, err := runContainerCheck(ctx, tree, "generated", "./...", next, t.TempDir()); err != nil || !strings.HasPrefix(result, "verified\n") {
					t.Fatalf("following generated: %s, %v", result, err)
				}
			} else {
				writebackContentT(t, tree, writebackX, "old x")
				writebackContentT(t, tree, writebackY, "old y")
			}
			if kind != "source cleanup" && copyDir != "" {
				if _, err := os.Stat(filepath.Dir(copyDir)); !os.IsNotExist(err) {
					t.Fatalf("source left: %v", err)
				}
			}
		})
	}
}
