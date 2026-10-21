package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func featureFlag(args []string, name, value string) []string {
	result := append([]string{}, args...)
	for i := 0; i < len(result); i += 2 {
		if result[i] == "--"+name {
			if value == "<absent>" {
				return append(result[:i], result[i+2:]...)
			}
			result[i+1] = value
			return result
		}
	}
	if value != "<absent>" {
		result = append(result, "--"+name, value)
	}
	return result
}

func TestImplementFeatureArguments(t *testing.T) {
	for _, command := range []string{"check", "run"} {
		for _, state := range []string{"<absent>", "", "invalid", "on"} {
			t.Run(command+"/"+state, func(t *testing.T) {
				x := newImplementRun(t)
				args := x.args
				if command == "check" {
					args = x.f.args()
				}
				args = featureFlag(args, "verify", state)
				var out, stderr bytes.Buffer
				code := 0
				if command == "check" {
					code = implementCheckCommand(args, &out, &stderr, x.f.deps)
				} else {
					code = implementRunCommand(args, &out, &stderr, x.deps)
				}
				if code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "--verify") {
					t.Fatalf("code=%d out=%s stderr=%s", code, &out, &stderr)
				}
				if _, err := os.Stat(filepath.Join(x.fake, "args")); !os.IsNotExist(err) {
					t.Fatal("started on invalid arguments")
				}
			})
		}
	}
}

func featureOn(t *testing.T, x *implementRunFixture) {
	t.Helper()
	source := filepath.Join(x.f.root, "source")
	implementCheckMkdir(t, source)
	x.set("verify", "on")
	x.set("trusted-source", source)
	x.deps.sourceTrust = func(string) error { return nil }
	x.f.deps.sourceTrust = x.deps.sourceTrust
	x.f.deps.executable = x.deps.executable
}

func TestImplementCheckFeatureOn(t *testing.T) {
	for _, mismatch := range []string{"none", "settings", "hooks", "mcp", "source"} {
		t.Run(mismatch, func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			r, err := implementRunArguments(x.args, io.Discard, x.deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.validate(); err != nil {
				t.Fatal(err)
			}
			for _, file := range r.files()[:3] {
				data := file.data
				if file.name == mismatch {
					data = append(append([]byte{}, data...), ' ')
				}
				write(t, file.path, string(data))
			}
			if mismatch == "source" {
				x.f.deps.sourceTrust = func(string) error { return errors.New("source refused") }
			}
			args := featureFlag(x.f.args(), "verify", "on")
			args = append(args, "--trusted-source", r.source)
			var out bytes.Buffer
			code := implementCheckCommand(args, &out, &out, x.f.deps)
			if (code == 0) != (mismatch == "none") {
				t.Fatalf("code=%d out=%s", code, &out)
			}
			if mismatch != "none" && !strings.Contains(out.String(), "CHECK:") {
				t.Fatal(out.String())
			}
		})
	}
}

// Both commands use this production source checker, including the shared
// trusted-build gate. Real Git proves ignored/untracked files count as dirty.
func TestImplementFeatureTrustedSource(t *testing.T) {
	for _, problem := range []string{"none", "other-main", "other-head", "dirty", "untracked", "ignored", "git-error"} {
		t.Run(problem, func(t *testing.T) {
			_, repo := implementCheckGitFixture(t, true)
			implementCheckGitOK(t, repo, "config", "remote.origin.url", ribbittoURL)
			revision := strings.TrimSpace(implementCheckGitOK(t, repo, "rev-parse", "HEAD"))
			info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}}}
			switch problem {
			case "other-main", "other-head":
				write(t, filepath.Join(repo, "extra"), "next")
				implementCheckGitOK(t, repo, "add", "extra")
				implementCheckGitOK(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "next")
				if problem == "other-main" {
					implementCheckGitOK(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
					implementCheckGitOK(t, repo, "reset", "--hard", revision)
				}
			case "dirty":
				write(t, filepath.Join(repo, "AGENTS.md"), "changed")
			case "untracked":
				write(t, filepath.Join(repo, "untracked"), "changed")
			case "ignored":
				write(t, filepath.Join(repo, ".git/info/exclude"), "ignored\n")
				write(t, filepath.Join(repo, "ignored"), "changed")
			}
			read := func(dir string, args ...string) (string, error) {
				if problem == "git-error" && args[0] == "status" {
					return "", errors.New("injected git failure")
				}
				data, code, err := implementGit(dir)(args...)
				if code != 0 {
					err = errors.New("git failed")
				}
				return strings.TrimSpace(string(data)), err
			}
			err := implementSourceRevision(repo, info, true, read)
			if (err == nil) != (problem == "none") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestImplementRunOnSequence(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(fmt.Sprint(tamper), func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			if tamper {
				write(t, filepath.Join(x.fake, "mutate"), `echo changed > "$HOME/.gemini/config/hooks.json"
`)
			}
			want := 0
			if tamper {
				want = 1
			}
			status := x.run(t, want)
			x.cleaned(t)
			marker := filepath.Join(x.f.home, "guard/implement-run.recovery-needed")
			_, err := os.Stat(marker)
			if (err == nil) != tamper {
				t.Fatalf("marker error %v", err)
			}
			post, err := os.ReadFile(filepath.Join(x.logs, "post.txt"))
			if err != nil || (strings.Contains(string(post), "feature-on configuration")) != tamper {
				t.Fatalf("post=%s error=%v", post, err)
			}
			order := strings.Join(x.order, ",")
			if !strings.Contains(order, "post-check,check,restore-settings,restore-hooks,restore-mcp") {
				t.Fatal(order)
			}
			if !strings.Contains(status, "verify=on\n") {
				t.Fatal(status)
			}
			r, err := implementRunArguments(featureFlag(x.args, "log-dir", x.logs+"-unused"), io.Discard, x.deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.validate(); err != nil {
				t.Fatal(err)
			}
			for _, file := range r.files()[:3] {
				if !strings.Contains(status, fmt.Sprintf("generated-%s-sha256=%x\n", file.name, sha256.Sum256(file.data))) {
					t.Fatal("missing generated hash", status)
				}
			}
			if !tamper {
				atRest, err := os.ReadFile(filepath.Join(x.logs, "at-rest.txt"))
				if err != nil || !strings.Contains(string(atRest), "implement-check: ok") {
					t.Fatalf("at rest %s %v", atRest, err)
				}
			}
		})
	}
}

func TestImplementRunInitialAtRest(t *testing.T) {
	for _, damage := range []string{"settings", "hooks", "mcp", "worktree", "shared"} {
		t.Run(damage, func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			path := map[string]string{
				"settings": filepath.Join(x.f.home, ".gemini/antigravity-cli/settings.json"),
				"hooks":    filepath.Join(x.f.home, ".gemini/config/hooks.json"),
				"mcp":      filepath.Join(x.f.home, ".gemini/config/mcp_config.json"),
				"worktree": filepath.Join(x.f.tree, ".git"),
				"shared":   filepath.Join(x.f.account, ".gemini/antigravity-cli/settings.json"),
			}[damage]
			write(t, path, "tampered")
			implementRunRefusedUnchanged(t, x, map[bool]string{true: "existing MCP configuration", false: "initial at-rest check failed"}[damage == "mcp"])
		})
	}
}

func TestImplementRunOnFailures(t *testing.T) {
	steps := []string{"mark-in-progress", "tmp", "place-settings", "place-hooks", "place-mcp", "place-worktree", "place-agent", "place-brain", "pre-check", "start", "post-check", "restore-settings", "restore-hooks", "restore-mcp", "empty-brain", "remove-tmp", "remove-agent", "remove-agents", "at-rest-check", "save-status", "remove-marker", "output"}
	for _, fail := range steps {
		t.Run(fail, func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			var visited []string
			x.deps.step = func(name string) error {
				visited = append(visited, name)
				if name == fail {
					return errors.New("injected " + name)
				}
				return nil
			}
			switch fail {
			case "tmp":
				x.deps.mkdirTemp = func(string, string) (string, error) { return "", errors.New("injected tmp") }
			case "start":
				write(t, filepath.Join(x.fake, "agy"), "#!/missing/interpreter\n")
			case "save-status":
				x.deps.saveStatus = func(*os.File, string) error { return errors.New("injected status") }
			}
			var out, stderr bytes.Buffer
			var writer io.Writer = &out
			if fail == "output" {
				writer = implementRunFailWriter{}
			}
			code := implementRunCommand(x.args, writer, &stderr, x.deps)
			if code != 1 {
				t.Fatalf("code=%d out=%s stderr=%s", code, &out, &stderr)
			}
			if fail != "start" && fail != "output" && fail != "save-status" && !strings.Contains(stderr.String(), "injected "+fail) {
				t.Fatalf("lost step failure %s: %s", fail, &stderr)
			}
			_, err := os.Stat(filepath.Join(x.f.home, "guard/implement-run.recovery-needed"))
			if (err == nil) != (fail != "mark-in-progress") {
				t.Fatalf("marker at %s: %v", fail, err)
			}
			if fail == "mark-in-progress" {
				if _, err := os.Stat(filepath.Join(x.fake, "args")); !os.IsNotExist(err) {
					t.Fatal("started despite marker failure")
				}
			}
			// All unaffected cleanup actions must run even after a failed action.
			if fail != "mark-in-progress" && fail != "tmp" {
				for _, name := range []string{"restore-settings", "restore-hooks", "restore-mcp", "empty-brain", "remove-tmp", "at-rest-check"} {
					if !strings.Contains(strings.Join(visited, ","), name) {
						t.Fatalf("missed %s: %v", name, visited)
					}
				}
			}
			for _, file := range []struct{ name, path string }{
				{"settings", ".gemini/antigravity-cli/settings.json"},
				{"hooks", ".gemini/config/hooks.json"},
				{"mcp", ".gemini/config/mcp_config.json"},
			} {
				data, err := os.ReadFile(filepath.Join(x.f.home, file.path))
				want, other := os.ReadFile(filepath.Join(x.f.home, "implementer", file.name+".json"))
				if file.name == "mcp" {
					want, other = nil, nil
				}
				if err != nil || other != nil {
					t.Fatalf("read restored %s: %v %v", file.name, err, other)
				}
				if bytes.Equal(data, want) == (fail == "restore-"+file.name) {
					t.Fatalf("restoration %s after %s: %s", file.name, fail, data)
				}
			}
			if !strings.HasPrefix(fail, "restore-") && fail != "remove-agent" && fail != "remove-agents" && fail != "remove-tmp" && fail != "empty-brain" {
				x.cleaned(t)
			}
		})
	}
}

// The child stops during installation, before any supervisor process starts.
// SIGKILL cannot run defers; the durable marker must be the remaining refusal.
func TestImplementRunInstallCrash(t *testing.T) {
	if os.Getenv("RIBBITTO_INSTALL_CRASH_CHILD") == "1" {
		x := newImplementRun(t)
		featureOn(t, x)
		write(t, os.Getenv("RIBBITTO_INSTALL_CRASH_PATH"), x.f.root)
		x.deps.step = func(name string) error {
			if name == "place-mcp" {
				if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				select {}
			}
			return nil
		}
		x.run(t, 0)
		t.Fatal("child survived")
	}
	root := resolved(t, t.TempDir())
	path := filepath.Join(root, "child-root")
	cmd := exec.Command(os.Args[0], "-test.run=^TestImplementRunInstallCrash$")
	cmd.Env = append(os.Environ(), "RIBBITTO_INSTALL_CRASH_CHILD=1", "RIBBITTO_INSTALL_CRASH_PATH="+path)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child did not crash: %s", output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("child root: %v: %s", err, output)
	}
	child := string(data)
	defer func() { _ = os.RemoveAll(child) }()
	home := filepath.Join(child, "home")
	onHooks, err := os.ReadFile(filepath.Join(home, ".gemini/config/hooks.json"))
	if err != nil || !strings.Contains(string(onHooks), "--allow-verify") {
		t.Fatalf("child did not die inside on installation: %s %v", onHooks, err)
	}
	mcp, err := os.ReadFile(filepath.Join(home, ".gemini/config/mcp_config.json"))
	if err != nil || len(mcp) != 0 {
		t.Fatalf("expected incomplete MCP installation: %s %v", mcp, err)
	}
	r := &implementRun{home: home, tree: filepath.Join(child, "tree")}
	if err := r.checkRecovery(); err == nil {
		t.Fatal("crash lost marker")
	}
	lock, err := implementRunLock(home)
	if err != nil {
		t.Fatalf("crash retained flock: %v", err)
	}
	_ = lock.Close()
	// No session can exist: the child died before launch. Recovery then
	// restores off files, checks at rest and only afterwards removes the marker.
	if _, err := os.Stat(filepath.Join(child, "fake/args")); !os.IsNotExist(err) {
		t.Fatal("child launched worker")
	}
	for _, file := range []struct{ from, to string }{
		{"implementer/settings.json", ".gemini/antigravity-cli/settings.json"},
		{"implementer/hooks.json", ".gemini/config/hooks.json"},
	} {
		data, err := os.ReadFile(filepath.Join(home, file.from))
		if err != nil {
			t.Fatal(err)
		}
		if err := implementRunWrite(filepath.Join(home, file.to), data); err != nil {
			t.Fatal(err)
		}
	}
	if err := implementRunWrite(filepath.Join(home, ".gemini/config/mcp_config.json"), nil); err != nil {
		t.Fatal(err)
	}
	f := implementCheckFixture(t)
	f.home, f.tree, f.saved, f.account = home, r.tree, filepath.Join(child, "saved-git"), filepath.Join(child, "account")
	f.deps.accountHome = f.account
	if code, out, stderr := f.run(); code != 0 {
		t.Fatalf("recovery off check: %d %s %s", code, out, stderr)
	}
	if err := r.checkRecovery(); err == nil {
		t.Fatal("check removed marker")
	}
	if err := r.clearRecovery(); err != nil {
		t.Fatal(err)
	}
	if err := r.checkRecovery(); err != nil {
		t.Fatal(err)
	}
	// The first subsequent run must pass the untouched off check, install on
	// again, finish cleanly and remove its own marker.
	deps := implementRunDefaults()
	deps.trust = func() error { return nil }
	deps.accountHome = f.account
	deps.executable = func() (string, error) { return "/trusted/argonreview", nil }
	deps.sourceTrust = func(string) error { return nil }
	f.deps.executable, f.deps.sourceTrust = deps.executable, deps.sourceTrust
	deps.check = func(args []string, out, stderr io.Writer) int {
		return implementCheckCommand(args, out, stderr, f.deps)
	}
	deps.ops.scan = func(int) ([]int, error) { return nil, nil }
	args := []string{"--verify", "on", "--trusted-source", filepath.Join(child, "source"),
		"--home", home, "--worktree", r.tree, "--git-file", f.saved,
		"--prompt-file", filepath.Join(child, "prompt"), "--log-dir", filepath.Join(child, "recovered-logs"),
		"--shared-settings-sha256", f.hash, "--agy", filepath.Join(child, "fake/agy"), "--timeout", "2s"}
	var out bytes.Buffer
	if code := implementRunCommand(args, &out, &out, deps); code != 0 {
		t.Fatalf("first recovered run: %d %s", code, &out)
	}
	if err := r.checkRecovery(); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := f.run(); code != 0 {
		t.Fatalf("after recovered run: %d %s %s", code, out, stderr)
	}
}

func TestImplementRunMarkerExclusive(t *testing.T) {
	x := newImplementRun(t)
	implementCheckMkdir(t, filepath.Join(x.f.home, "guard"))
	r := &implementRun{home: x.f.home, tree: x.f.tree}
	if err := r.markRecovery(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(r.recoveryMarker())
	if err := r.markRecovery(); err == nil {
		t.Fatal("overwrote existing marker")
	}
	after, _ := os.ReadFile(r.recoveryMarker())
	if !bytes.Equal(before, after) {
		t.Fatal("changed marker")
	}
	if !strings.Contains(string(after), "time=") {
		t.Fatal(string(after))
	}
	stamp := strings.Split(strings.Split(string(after), "time=")[1], " ")[0]
	if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestImplementRunMarkerDurability(t *testing.T) {
	for _, fail := range []string{"none", "file", "directory"} {
		t.Run(fail, func(t *testing.T) {
			x := newImplementRun(t)
			implementCheckMkdir(t, filepath.Join(x.f.home, "guard"))
			r := &implementRun{home: x.f.home, tree: x.f.tree}
			var order []string
			err := r.markRecoveryDurable(func(f *os.File) error {
				order = append(order, "file")
				data, err := os.ReadFile(f.Name())
				if err != nil || !strings.Contains(string(data), x.f.tree) {
					t.Fatalf("sync before write: %s %v", data, err)
				}
				if fail == "file" {
					return errors.New("injected file sync")
				}
				return f.Sync()
			}, func(dir int) error {
				order = append(order, "directory")
				var st unix.Stat_t
				if err := unix.Fstat(dir, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
					t.Fatalf("not the open parent directory: %v", err)
				}
				if fail == "directory" {
					return errors.New("injected directory sync")
				}
				return unix.Fsync(dir)
			})
			if strings.Join(order, ",") != "file,directory" {
				t.Fatalf("durability order: %v", order)
			}
			if (err == nil) != (fail == "none") {
				t.Fatalf("durability error: %v", err)
			}
			if err := r.checkRecovery(); err == nil {
				t.Fatal("lost partial marker")
			}
		})
	}
}

func TestImplementRunMarkerCompletion(t *testing.T) {
	for _, failRemove := range []bool{false, true} {
		t.Run(fmt.Sprint(failRemove), func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			marker := filepath.Join(x.f.home, "guard/implement-run.recovery-needed")
			saved := false
			x.deps.saveStatus = func(f *os.File, status string) error {
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("marker gone before status", err)
				}
				if x.checks != 4 {
					t.Fatal("status before all checks", x.checks)
				}
				saved = true
				return implementRunSaveStatus(f, status)
			}
			step := x.deps.step
			x.deps.step = func(name string) error {
				if name == "place-settings" {
					if _, err := os.Stat(marker); err != nil {
						t.Fatal("installation before marker", err)
					}
					if x.checks != 1 {
						t.Fatal("installation before initial check")
					}
				}
				if name == "remove-marker" {
					if !saved {
						t.Fatal("removal before status")
					}
					if failRemove {
						if err := os.Remove(marker); err != nil {
							t.Fatal(err)
						}
						implementCheckMkdir(t, marker)
					}
				}
				return step(name)
			}
			want := 0
			if failRemove {
				want = 1
			}
			x.run(t, want)
			data, err := os.ReadFile(filepath.Join(x.logs, "status"))
			if err != nil || strings.Contains(string(data), "counts=true") == failRemove {
				t.Fatalf("status=%s err=%v", data, err)
			}
			_, err = os.Stat(marker)
			if (err == nil) != failRemove {
				t.Fatal("marker after completion", err)
			}
		})
	}
}

func TestImplementRunFeatureSourceRefusal(t *testing.T) {
	x := newImplementRun(t)
	featureOn(t, x)
	x.deps.sourceTrust = func(string) error { return errors.New("source refused") }
	implementRunRefusedUnchanged(t, x, "source refused")
}

func TestImplementFeatureSourcePaths(t *testing.T) {
	for _, kind := range []string{"valid", "symlink", "missing", "home", "tree", "nested-home", "nested-tree"} {
		t.Run(kind, func(t *testing.T) {
			x := newImplementRun(t)
			source := filepath.Join(x.f.root, "source")
			implementCheckMkdir(t, source)
			switch kind {
			case "symlink":
				link := filepath.Join(x.f.root, "source-link")
				symlink(t, source, link)
				source = link
			case "missing":
				source += "/absent"
			case "home":
				source = x.f.home
			case "tree":
				source = x.f.tree
			case "nested-home":
				source = filepath.Join(x.f.home, "source")
				implementCheckMkdir(t, source)
			case "nested-tree":
				source = filepath.Join(x.f.tree, "source")
				implementCheckMkdir(t, source)
			}
			err := implementFeatureSource(x.f.home, x.f.tree, source, func(string) error { return nil })
			if (err == nil) != (kind == "valid") {
				t.Fatalf("source path accepted=%v: %v", err == nil, err)
			}
		})
	}
}

func TestImplementRunMarkerCreationFailure(t *testing.T) {
	x := newImplementRun(t)
	featureOn(t, x)
	x.deps.step = func(name string) error {
		if name == "mark-in-progress" {
			// Race an entry in after the initial refusal check, forcing the
			// real exclusive create to fail, rather than mocking its result.
			implementCheckMkdir(t, filepath.Join(x.f.home, "guard/implement-run.recovery-needed"))
		}
		if strings.HasPrefix(name, "place-") {
			t.Fatal("installed despite failed marker creation")
		}
		return nil
	}
	status := x.run(t, 1)
	if !strings.Contains(status, "writing recovery marker") {
		t.Fatal(status)
	}
	if _, err := os.Stat(filepath.Join(x.fake, "args")); !os.IsNotExist(err) {
		t.Fatal("started despite marker creation failure")
	}
	x.cleaned(t)
}

func TestImplementRunOnUnconfirmed(t *testing.T) {
	x := newImplementRun(t)
	featureOn(t, x)
	x.deps.ops.held = func(int) bool { return false }
	x.deps.ops.scan = func(int) ([]int, error) { return nil, errors.New("cannot scan") }
	status := x.run(t, 1)
	hooks, err := os.ReadFile(filepath.Join(x.f.home, ".gemini/config/hooks.json"))
	if err != nil || !strings.Contains(string(hooks), "--allow-verify") {
		t.Fatalf("unconfirmed files restored: %s %v", hooks, err)
	}
	if x.checks != 2 || strings.Contains(strings.Join(x.order, ","), "restore-") || !strings.Contains(status, "stop-confirmed=false") {
		t.Fatal(status, x.order)
	}
	if _, err := os.Stat(filepath.Join(x.f.home, "guard/implement-run.recovery-needed")); err != nil {
		t.Fatal("unconfirmed run lost marker", err)
	}
}

func TestImplementFeatureSourceArguments(t *testing.T) {
	for _, command := range []string{"check", "run"} {
		for _, source := range []string{"relative", "/unclean/../source", "off-source"} {
			t.Run(command+"/"+source, func(t *testing.T) {
				x := newImplementRun(t)
				featureOn(t, x)
				args := x.args
				if command == "check" {
					args = featureFlag(x.f.args(), "verify", "on")
				}
				value := source
				if source == "off-source" {
					args = featureFlag(args, "verify", "off")
					value = filepath.Join(x.f.root, "source")
				}
				args = featureFlag(args, "trusted-source", value)
				var out, stderr bytes.Buffer
				code := 0
				if command == "check" {
					code = implementCheckCommand(args, &out, &stderr, x.f.deps)
				} else {
					code = implementRunCommand(args, &out, &stderr, x.deps)
				}
				if code != 2 || out.Len() != 0 || stderr.Len() == 0 {
					t.Fatalf("code=%d out=%s stderr=%s", code, &out, &stderr)
				}
			})
		}
	}
}

func TestImplementRunOnCheckResults(t *testing.T) {
	for _, phase := range []string{"pre", "post", "at-rest"} {
		t.Run(phase, func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			check := x.deps.check
			x.deps.check = func(args []string, out, stderr io.Writer) int {
				code := check(args, out, stderr)
				want := map[string]int{"pre": 2, "post": 3, "at-rest": 4}[phase]
				if x.checks == want {
					return 1
				}
				return code
			}
			status := x.run(t, 1)
			if !strings.Contains(status, phase+"-check failed") && phase != "at-rest" {
				t.Fatal(status)
			}
			if phase == "at-rest" && !strings.Contains(status, "at-rest check failed") {
				t.Fatal(status)
			}
			if _, err := os.Stat(filepath.Join(x.f.home, "guard/implement-run.recovery-needed")); err != nil {
				t.Fatal("failed check lost marker", err)
			}
			x.cleaned(t)
		})
	}
}

func TestImplementRunMarkerRemovalSync(t *testing.T) {
	x := newImplementRun(t)
	implementCheckMkdir(t, filepath.Join(x.f.home, "guard"))
	r := &implementRun{home: x.f.home, tree: x.f.tree}
	if err := r.markRecovery(); err != nil {
		t.Fatal(err)
	}
	err := r.clearRecoveryDurable(func(int) error { return errors.New("injected removal sync") })
	if err == nil || !strings.Contains(err.Error(), "injected removal sync") {
		t.Fatalf("ignored removal sync: %v", err)
	}
	if err := r.checkRecovery(); err == nil {
		t.Fatal("failed removal sync left no refusal marker")
	}
}
