package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"
)

func TestImplementRunStraggler(t *testing.T) {
	x := newImplementRun(t)
	pidFile := filepath.Join(x.fake, "child")
	ready := filepath.Join(x.fake, "child-ready")
	// The child publishes readiness from Perl itself, so the leader cannot
	// exit before exec has established the command line we want to observe.
	write(t, filepath.Join(x.fake, "mutate"), `/usr/bin/perl -e 'open my $ready, ">", $ARGV[0] or die $!; close $ready; sleep 1511' '`+ready+`' &
echo $! > '`+pidFile+`'
while [ ! -f '`+ready+`' ]; do sleep 0.01; done
`)
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		data, _ := os.ReadFile(pidFile)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	x.deps.ops = systemCleanup()
	status := x.run(t, 0)
	stopped = true
	var command string
	for _, line := range strings.Split(status, "\n") {
		if value, ok := strings.CutPrefix(line, "straggler="); ok {
			var err error
			command, err = strconv.Unquote(value)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if strings.Count(status, "straggler=") != 1 || !strings.Contains(command, "sleep 1511") || !strings.Contains(command, ready) || !strings.Contains(status, "stop-confirmed=true\nsession-left=0\n") {
		t.Fatalf("missing surviving child's command line: %s", status)
	}
	data, err := os.ReadFile(filepath.Join(x.logs, "status"))
	if err != nil || !strings.Contains(string(data), "straggler="+strconv.Quote(command)+"\n") {
		t.Fatalf("straggler not saved: %s %v", data, err)
	}
	x.cleaned(t)
}

func TestImplementRunStragglerStatus(t *testing.T) {
	x := newImplementRun(t)
	command := "worker \"quoted\"\nargument"
	captured := map[int]bool{}
	scans := 0
	x.deps.ops.scan = func(int) ([]int, error) {
		scans++
		if scans <= 2 {
			return []int{101, 102}, nil
		}
		return nil, nil
	}
	x.deps.ops.commandLine = func(pid int) string {
		if captured[pid] {
			t.Fatal("captured the same process twice")
		}
		captured[pid] = true
		return command
	}
	x.deps.ops.killMember = func(pid int, _ *child) {
		if !captured[pid] {
			t.Fatal("stopped process before capturing command line")
		}
	}
	status := x.run(t, 0)
	want := "straggler=" + strconv.Quote(command) + "\n"
	data, err := os.ReadFile(filepath.Join(x.logs, "status"))
	if err != nil || strings.Count(status, want) != 2 || strings.Count(string(data), want) != 2 || len(captured) != 2 {
		t.Fatalf("missing per-process quoted status: %s %v", status, err)
	}
	x.cleaned(t)
}

func TestImplementRunStragglerStatusBound(t *testing.T) {
	exact := strings.Repeat("a", 4093) + "\"\n\\"
	for _, tt := range []struct {
		name, command, want string
	}{
		{"exact", exact, exact},
		{"long", exact + "tail", exact + "...(truncated)"},
		{"multibyte", strings.Repeat("a", 4095) + "界tail", strings.Repeat("a", 4095) + "...(truncated)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &implementRun{home: t.TempDir()}
			status := r.status(0, nil, 0, 0, true, nil, []string{tt.command}, "")
			count := 0
			for _, line := range strings.Split(status, "\n") {
				if !strings.HasPrefix(line, "straggler=") {
					continue
				}
				count++
				command, err := strconv.Unquote(strings.TrimPrefix(line, "straggler="))
				if err != nil || !utf8.ValidString(command) || command != tt.want {
					t.Fatalf("straggler has %d bytes, valid UTF-8=%t, unquote error=%v; want %d bytes with matching content", len(command), utf8.ValidString(command), err, len(tt.want))
				}
			}
			if count != 1 {
				t.Fatalf("got %d straggler lines, want 1", count)
			}
		})
	}
}

type implementRunFixture struct {
	f      *implementCheckTestFixture
	deps   implementRunDeps
	args   []string
	fake   string
	logs   string
	order  []string
	checks int
}

func newImplementRun(t *testing.T) *implementRunFixture {
	t.Helper()
	f := implementCheckFixture(t)
	x := &implementRunFixture{f: f, fake: filepath.Join(f.root, "fake"), logs: filepath.Join(f.root, "logs")}
	hooks, err := implementerHooks(false, false, "/trusted/argonreview", filepath.Join(f.home, "guard"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"implementer/settings.json", ".gemini/antigravity-cli/settings.json"} {
		write(t, filepath.Join(f.home, path), string(implementVerifySettings(t, false, false)))
	}
	for _, path := range []string{"implementer/hooks.json", ".gemini/config/hooks.json"} {
		write(t, filepath.Join(f.home, path), string(hooks))
	}
	write(t, filepath.Join(f.home, "implementer/argon-implementer.md"), "trusted agent\n")
	write(t, filepath.Join(f.root, "prompt"), "Implement the approved issue.\n")
	write(t, filepath.Join(x.fake, "stdout"), okResult)
	write(t, filepath.Join(x.fake, "stderr"), "")
	write(t, filepath.Join(x.fake, "status"), "0")
	write(t, filepath.Join(x.fake, "mutate"), "")
	script := `#!/bin/sh
printf '%s\n' "$@" > '` + x.fake + `/args'
printf '%s\n' "$TMPDIR" > '` + x.fake + `/tmp'
pwd -P > '` + x.fake + `/cwd'
/usr/bin/env > '` + x.fake + `/env'
[ -z "$(cat)" ] || exit 11
echo cache > "$TMPDIR/cache"
cat '` + x.fake + `/stdout'
cat '` + x.fake + `/stderr' >&2
sh '` + x.fake + `/mutate'
exit "$(cat '` + x.fake + `/status')"
`
	agy := filepath.Join(x.fake, "agy")
	write(t, agy, script)
	if err := os.Chmod(agy, 0o700); err != nil {
		t.Fatal(err)
	}
	x.deps = implementRunDefaults()
	x.deps.trust = func() error { x.order = append(x.order, "trust"); return nil }
	x.deps.accountHome = f.account
	x.deps.executable = func() (string, error) { return "/trusted/argonreview", nil }
	x.deps.step = func(name string) error { x.order = append(x.order, name); return nil }
	x.deps.ops.scan = func(int) ([]int, error) { x.order = append(x.order, "stop"); return nil, nil }
	x.deps.withSignals = func(run func(<-chan os.Signal) (int, error)) (int, error) { return run(nil) }
	x.deps.check = func(args []string, out, stderr io.Writer) int {
		x.checks++
		x.order = append(x.order, "check")
		if log, ok := out.(*os.File); ok && filepath.Base(log.Name()) == "post.txt" {
			order := strings.Join(x.order, ",")
			if strings.Contains(order, "restore-") || strings.Contains(order, "place-settings") && !strings.Contains(order, "post-check,check") {
				t.Errorf("post-check must precede restoration: %v", x.order)
			}
		}
		placed := strings.Contains(strings.Join(x.order, ","), "place-agent")
		if x.checks == 1 {
			if _, err := os.Stat(filepath.Join(x.fake, "args")); !os.IsNotExist(err) {
				t.Error("agy ran before initial at-rest check")
			}
		}
		if placed && !strings.Contains(strings.Join(x.order, ","), "remove-agent") {
			for _, file := range []string{".gemini/config/agents/argon-implementer.md", "guard/implementer-worktree"} {
				if _, err := os.Stat(filepath.Join(f.home, file)); err != nil {
					t.Errorf("check missing %s: %v", file, err)
				}
			}
			marker, err := os.ReadFile(filepath.Join(f.home, "guard/implementer-worktree"))
			if err != nil || string(marker) != f.tree+"\n" {
				t.Errorf("guard worktree marker %q: %v", marker, err)
			}
		}
		return implementCheckCommand(args, out, stderr, f.deps)
	}
	x.args = []string{"--verify", "off", "--home", f.home, "--worktree", f.tree, "--git-file", f.saved, "--prompt-file", filepath.Join(f.root, "prompt"), "--log-dir", x.logs, "--shared-settings-sha256", f.hash, "--agy", agy, "--timeout", "2s"}
	return x
}

func (x *implementRunFixture) run(t *testing.T, want int) string {
	t.Helper()
	var out, stderr bytes.Buffer
	code := implementRunCommand(x.args, &out, &stderr, x.deps)
	if code != want {
		t.Fatalf("exit %d, want %d; stderr %s; status %s; order %v", code, want, stderr.String(), out.String(), x.order)
	}
	if want != 0 && stderr.Len() == 0 {
		t.Fatal("failure had no diagnostic")
	}
	return out.String() + stderr.String()
}

func (x *implementRunFixture) set(name, value string) {
	for i := 0; i < len(x.args); i += 2 {
		if x.args[i] == "--"+name {
			x.args[i+1] = value
			return
		}
	}
	x.args = append(x.args, "--"+name, value)
}

func (x *implementRunFixture) cleaned(t *testing.T) {
	t.Helper()
	for _, name := range []string{"settings.json", "hooks.json"} {
		actual := ".gemini/config/" + name
		if name == "settings.json" {
			actual = ".gemini/antigravity-cli/" + name
		}
		want, _ := os.ReadFile(filepath.Join(x.f.home, "implementer", name))
		got, err := os.ReadFile(filepath.Join(x.f.home, actual))
		if err != nil || !bytes.Equal(want, got) {
			t.Errorf("%s was not restored: %q, %v", name, got, err)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(x.f.home, ".gemini/antigravity-cli/brain")); !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
		t.Errorf("brain remains: %v, %v", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(x.f.home, ".gemini/config/agents")); !os.IsNotExist(err) {
		t.Errorf("agents directory remains: %v", err)
	}
	matches, err := filepath.Glob(x.f.tree + ".tmp-*")
	if err != nil || len(matches) != 0 {
		t.Errorf("TMPDIR remains: %v %v", matches, err)
	}
}

func TestImplementRun(t *testing.T) {
	x := newImplementRun(t)
	wantEnv := testAgyParentEnvironment(t)
	wantEnv["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin:" + filepath.Join(x.f.account, ".local/bin")
	t.Setenv("GITHUB_TOKEN", "dummy-excluded")
	t.Setenv("SSH_AUTH_SOCK", "dummy-excluded")
	write(t, filepath.Join(x.f.home, ".gemini/antigravity-cli/brain/stale"), "previous conversation")
	x.set("shared-settings-sha256", strings.ToUpper(x.f.hash))
	status := x.run(t, 0)
	if strings.Contains(status, "straggler=") {
		t.Fatalf("clean run reported stragglers: %s", status)
	}
	for _, want := range []string{"exit=0\ncounts=true\n", "pre-check=0\npost-check=0\nstop-confirmed=true\nsession-left=0\n", "cleanup-remove-agents=true"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q: %s", want, status)
		}
	}
	args, _ := os.ReadFile(filepath.Join(x.fake, "args"))
	wantArgs := "-p\nImplement the approved issue.\n\n--model\n" + argonModel + "\n--agent\nargon-implementer\n--sandbox\n--print-timeout\n1s\n--output-format\nstream-json\n"
	if string(args) != wantArgs || strings.Contains(string(args), "--dangerously-skip-permissions") {
		t.Fatalf("argv %q, want %q", args, wantArgs)
	}
	cwd, _ := os.ReadFile(filepath.Join(x.fake, "cwd"))
	if string(cwd) != x.f.tree+"\n" {
		t.Errorf("cwd %q", cwd)
	}
	tmp, _ := os.ReadFile(filepath.Join(x.fake, "tmp"))
	wantEnv["HOME"], wantEnv["TMPDIR"] = x.f.home, strings.TrimSpace(string(tmp))
	assertFakeAgyEnvironment(t, filepath.Join(x.fake, "env"), wantEnv)
	if filepath.Dir(wantEnv["TMPDIR"]) != filepath.Dir(x.f.tree) || !strings.HasPrefix(wantEnv["TMPDIR"], x.f.tree+".tmp-") {
		t.Fatalf("TMPDIR not beside worktree: %s", tmp)
	}
	wantOrder := []string{"trust", "check", "mark-in-progress", "place-settings", "place-hooks", "place-mcp", "place-worktree", "place-agent", "place-brain", "pre-check", "check", "stop", "post-check", "check", "restore-settings", "restore-hooks", "restore-mcp", "empty-brain", "remove-tmp", "remove-agent", "remove-agents", "at-rest-check", "check", "remove-marker"}
	if !reflect.DeepEqual(x.order, wantOrder) {
		t.Errorf("order %v, want %v", x.order, wantOrder)
	}
	mcp, err := os.ReadFile(filepath.Join(x.f.home, ".gemini/config/mcp_config.json"))
	if err != nil || len(mcp) != 0 {
		t.Fatalf("MCP is not empty: %q, %v", mcp, err)
	}
	x.cleaned(t)
	// A second run in the same worktree gets a new exclusive directory.
	x.logs += "-2"
	x.set("log-dir", x.logs)
	x.checks, x.order = 0, nil
	if err := os.Remove(filepath.Join(x.fake, "args")); err != nil {
		t.Fatal(err)
	}
	x.run(t, 0)
	other, _ := os.ReadFile(filepath.Join(x.fake, "tmp"))
	if bytes.Equal(tmp, other) {
		t.Fatal("TMPDIR reused")
	}
	x.cleaned(t)
}

func TestImplementRunFailureRules(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, exit, mutate, want string
		code                                     int
	}{
		{"nonzero", okResult, "", "7", "", "status 7", 7},
		{"timeout", okResult, "", "0", "exec sleep 30", "status 124", 124},
		{"AGY_ERROR", okResult, "AGY_ERROR: quota", "0", "", "AGY_ERROR", 1},
		{"malformed", "bad\n", "", "0", "", "stream-json", 1},
		{"incomplete", strings.TrimSuffix(okResult, "\n"), "", "0", "", "cut off", 1},
		{"no-result", "{\"event\":\"init\"}\n", "", "0", "", "no result", 1},
		{"denied", "{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"x\",\"denied_actions\":[\"tool\"]}}\n", "", "0", "", "denied_actions", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newImplementRun(t)
			write(t, filepath.Join(x.fake, "stdout"), tc.stdout)
			write(t, filepath.Join(x.fake, "stderr"), tc.stderr)
			write(t, filepath.Join(x.fake, "status"), tc.exit)
			write(t, filepath.Join(x.fake, "mutate"), tc.mutate)
			if tc.name == "timeout" {
				x.set("timeout", "100ms")
			}
			status := x.run(t, tc.code)
			if !strings.Contains(status, tc.want) || !strings.Contains(status, "counts=false") {
				t.Fatalf("missing reason %q: %s", tc.want, status)
			}
			x.cleaned(t)
		})
	}
}

func TestImplementRunDefaultsAndModel(t *testing.T) {
	x := newImplementRun(t)
	args := x.args[:len(x.args)-4] // Exercise both optional agy and timeout defaults.
	r, err := implementRunArguments(args, &bytes.Buffer{}, x.deps)
	if err != nil {
		t.Fatal(err)
	}
	if r.agy != filepath.Join(x.f.account, ".local/bin/agy") || r.limit.String() != "20m0s" || r.model != argonModel {
		t.Fatalf("defaults: agy=%q limit=%v model=%q", r.agy, r.limit, r.model)
	}
	x.set("model", "maintainer-chosen-model")
	x.set("timeout", "20m")
	x.run(t, 0)
	data, _ := os.ReadFile(filepath.Join(x.fake, "args"))
	if !strings.Contains(string(data), "--model\nmaintainer-chosen-model\n") || !strings.Contains(string(data), "--print-timeout\n18m0s\n") {
		t.Fatalf("override/normal print limit: %s", data)
	}
	x.cleaned(t)
}

func TestImplementRunCheckFailures(t *testing.T) {
	for _, phase := range []string{"pre", "post", "shared-before", "shared-during"} {
		t.Run(phase, func(t *testing.T) {
			x := newImplementRun(t)
			check := x.deps.check
			x.deps.check = func(args []string, out, stderr io.Writer) int {
				code := check(args, out, stderr)
				if phase == "pre" && x.checks == 2 || phase == "post" && x.checks == 3 {
					return 1
				}
				return code
			}
			if phase == "shared-before" {
				x.set("shared-settings-sha256", strings.Repeat("0", 64))
			}
			if phase == "shared-during" {
				write(t, filepath.Join(x.fake, "mutate"), "echo changed >> '"+filepath.Join(x.f.account, ".gemini/antigravity-cli/settings.json")+"'\n")
			}
			status := x.run(t, 1)
			if phase == "shared-before" {
				if x.checks != 1 || !strings.Contains(status, "initial at-rest check failed") {
					t.Fatal(status)
				}
				return
			}
			if !strings.Contains(status, "counts=false") || x.checks != 4 {
				t.Fatalf("checks %d status %s", x.checks, status)
			}
			if phase == "pre" || phase == "shared-before" {
				if _, err := os.Stat(filepath.Join(x.fake, "args")); !os.IsNotExist(err) {
					t.Fatal("agy ran after failed pre-check")
				}
			}
			x.cleaned(t)
		})
	}
}

func TestImplementRunFailuresAtEachStep(t *testing.T) {
	for _, fail := range []string{"place-settings", "place-hooks", "place-mcp", "place-worktree", "place-agent", "place-brain", "restore-settings", "restore-hooks", "empty-brain", "remove-tmp", "remove-agent", "remove-agents"} {
		t.Run(fail, func(t *testing.T) {
			x := newImplementRun(t)
			x.deps.step = func(name string) error {
				x.order = append(x.order, name)
				if name == fail {
					return errors.New("injected " + name)
				}
				return nil
			}
			x.deps.check = func(_ []string, _, _ io.Writer) int {
				x.order = append(x.order, "check")
				x.checks++
				return 0
			}
			// Leave actual damage for failed restoration/emptying to expose.
			if !strings.HasPrefix(fail, "place-") {
				write(t, filepath.Join(x.fake, "mutate"), `echo changed > "$HOME/.gemini/antigravity-cli/settings.json"
echo changed > "$HOME/.gemini/config/hooks.json"
mkdir -p "$HOME/.gemini/antigravity-cli/brain"
echo conversation > "$HOME/.gemini/antigravity-cli/brain/file"
`)
			}
			status := x.run(t, 1)
			if !strings.Contains(status, "injected "+fail) || !strings.Contains(status, "counts=false") {
				t.Fatalf("missing failed cleanup/placement: %s", status)
			}
			for _, name := range []string{"restore-settings", "restore-hooks", "empty-brain", "remove-tmp"} {
				if !strings.Contains(strings.Join(x.order, ","), name) {
					t.Errorf("step skipped: %s, %v", name, x.order)
				}
			}
			if x.checks < 1 {
				t.Fatal("post-check skipped")
			}
			order := strings.Join(x.order, ",")
			postIndex, agentIndex := strings.Index(order, "check,restore-settings"), strings.Index(order, "remove-agent")
			if agentIndex >= 0 && (postIndex < 0 || postIndex >= agentIndex) {
				t.Fatalf("agent removed before post-check: %s", order)
			}
			if strings.HasPrefix(fail, "place-") {
				x.cleaned(t)
			} else {
				for _, target := range []struct{ step, path string }{
					{"restore-settings", filepath.Join(x.f.home, ".gemini/antigravity-cli/settings.json")},
					{"restore-hooks", filepath.Join(x.f.home, ".gemini/config/hooks.json")},
				} {
					data, _ := os.ReadFile(target.path)
					if strings.Contains(string(data), "changed") != (fail == target.step) {
						t.Errorf("incorrect restoration for %s: %s", target.step, data)
					}
				}
				brain, _ := os.ReadDir(filepath.Join(x.f.home, ".gemini/antigravity-cli/brain"))
				if (len(brain) > 0) != (fail == "empty-brain") {
					t.Errorf("brain after %s: %v", fail, brain)
				}
				tmp, _ := filepath.Glob(x.f.tree + ".tmp-*")
				if (len(tmp) > 0) != (fail == "remove-tmp") {
					t.Errorf("tmp after %s: %v", fail, tmp)
				}
				_, err := os.Stat(filepath.Join(x.f.home, ".gemini/config/agents/argon-implementer.md"))
				if (err == nil) != (fail == "remove-agent") {
					t.Errorf("agent after %s: %v", fail, err)
				}
			}
		})
	}
}

func TestImplementRunStartAndSignals(t *testing.T) {
	for _, phase := range []string{"start", "INT", "TERM", "cleanup-signal"} {
		t.Run(phase, func(t *testing.T) {
			x := newImplementRun(t)
			want := 1
			if phase == "start" {
				// Validation now refuses non-executable files; keep exercising a
				// genuine start failure with an executable missing its interpreter.
				write(t, filepath.Join(x.fake, "agy"), "#!/missing/implement-run-interpreter\n")
			} else {
				signals := make(chan os.Signal, 1)
				x.deps.withSignals = func(run func(<-chan os.Signal) (int, error)) (int, error) { return run(signals) }
				sig := syscall.SIGINT
				want = 130
				if phase == "TERM" || phase == "cleanup-signal" {
					sig, want = syscall.SIGTERM, 143
				}
				if phase == "cleanup-signal" {
					step := x.deps.step
					x.deps.step = func(name string) error {
						if name == "restore-hooks" {
							signals <- sig
						}
						return step(name)
					}
				} else {
					write(t, filepath.Join(x.fake, "mutate"), "exec sleep 30")
					x.deps.ops.scan = func(int) ([]int, error) { return nil, nil }
					signals <- sig
				}
			}
			x.run(t, want)
			x.cleaned(t)
		})
	}
}

func TestImplementRunUnconfirmedStop(t *testing.T) {
	x := newImplementRun(t)
	x.deps.ops.held = func(int) bool { return false }
	x.deps.ops.scan = func(int) ([]int, error) { return nil, errors.New("cannot scan") }
	status := x.run(t, 1)
	for _, want := range []string{"manual recovery needed", "stop-confirmed=false", "session-left=unknown", x.f.tree + ".tmp-", ".gemini/config/agents/argon-implementer.md", ".gemini/config/hooks.json", ".gemini/antigravity-cli/settings.json", "/brain"} {
		if !strings.Contains(status, want) {
			t.Errorf("missing recovery hint %q: %s", want, status)
		}
	}
	if x.checks != 2 || strings.Contains(strings.Join(x.order, ","), "restore-") {
		t.Fatalf("normal completion continued: %v", x.order)
	}
	if _, err := os.Stat(filepath.Join(x.f.home, ".gemini/config/agents/argon-implementer.md")); err != nil {
		t.Fatal("agent removed before stop confirmed")
	}
	if tmp, _ := filepath.Glob(x.f.tree + ".tmp-*"); len(tmp) != 1 {
		t.Fatalf("TMPDIR touched: %v", tmp)
	}
}

func TestImplementRunStopsCatchingBeforeStatus(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "late-signal"}[late], func(t *testing.T) {
			x := newImplementRun(t)
			stopped := false
			x.deps.withSignals = func(run func(<-chan os.Signal) (int, error)) (int, error) {
				signals := make(chan os.Signal, 1)
				code, err := run(signals)
				stopped = true
				if late {
					signals <- syscall.SIGINT
				}
				return code, err
			}
			out := stoppedFirst{stopped: &stopped}
			code := implementRunCommand(x.args, &out, &bytes.Buffer{}, x.deps)
			want := 0
			if late {
				want = 130
			}
			if code != want || out.early || !strings.Contains(out.String(), "counts="+map[bool]string{false: "true", true: "false"}[late]) {
				t.Fatalf("status while catching=%t code=%d status=%s", out.early, code, out.String())
			}
			x.cleaned(t)
		})
	}
}

func TestImplementRunUnconfirmedDoesNotJudge(t *testing.T) {
	x := newImplementRun(t)
	x.deps.ops.held = func(int) bool { return false }
	x.deps.ops.scan = func(int) ([]int, error) { return nil, errors.New("cannot scan") }
	write(t, filepath.Join(x.fake, "stdout"), "bad transcript\n")
	status := x.run(t, 1)
	// Seeking a descriptor still shared with a live writer could change its
	// output offset. Unconfirmed output is retained without even judging it.
	if strings.Contains(status, "stream-json") {
		t.Fatal("unconfirmed session continued transcript judgement")
	}
}

func TestImplementRunStatusOutputFailure(t *testing.T) {
	x := newImplementRun(t)
	if code := implementRunCommand(x.args, implementRunFailWriter{}, &bytes.Buffer{}, x.deps); code != 1 {
		t.Fatalf("output failure exit %d", code)
	}
	status, err := os.ReadFile(filepath.Join(x.logs, "status"))
	if err != nil || !strings.Contains(string(status), "counts=false") || !strings.Contains(string(status), "status output failed") {
		t.Fatalf("incorrect status: %s %v", status, err)
	}
	x.cleaned(t)
}

type implementRunFailWriter struct{}

func (implementRunFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("output failed")
}

func TestImplementRunRealSessionStop(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "timeout"}[timeout], func(t *testing.T) {
			x := newImplementRun(t)
			x.deps.ops = systemCleanup()
			want := 0
			if timeout {
				x.set("timeout", "100ms")
				write(t, filepath.Join(x.fake, "mutate"), "exec sleep 30")
				want = 124
			}
			x.run(t, want)
			x.cleaned(t)
		})
	}
}
