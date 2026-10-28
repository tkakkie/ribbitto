package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func noSweep(io.Writer) error { return nil }

type fakeProcessTable struct {
	procs                       []procInfo
	readFn                      func(int) (procInfo, bool, error)
	signals                     []syscall.Signal
	signalled                   []int
	listErr, readErr, signalErr error
	survive                     bool
	termSurvive                 bool
}

func (p *fakeProcessTable) list() ([]procInfo, error) { return p.procs, p.listErr }
func (p *fakeProcessTable) read(pid int) (procInfo, bool, error) {
	if p.readFn != nil {
		return p.readFn(pid)
	}
	for _, proc := range p.procs {
		if proc.pid == pid {
			return proc, true, p.readErr
		}
	}
	return procInfo{}, false, p.readErr
}
func (p *fakeProcessTable) signal(pid int, sig syscall.Signal) error {
	p.signals = append(p.signals, sig)
	p.signalled = append(p.signalled, pid)
	if !p.survive && (!p.termSurvive || sig == syscall.SIGKILL) {
		p.procs = nil
	}
	return p.signalErr
}

const sweepTestID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const sweepTestName = "argonreview-check-0123456789abcdef0123456789abcdef"

func sweepProc() procInfo {
	return procInfo{41, 1, os.Getuid(), "Tue Oct 20 12:34:56 2026", "/usr/local/bin/docker run --pull=never --name " + sweepTestName + " --network none --read-only"}
}

func sweepFixture(t *testing.T) (*containerSweep, *bytes.Buffer, string) {
	t.Helper()
	dir := resolved(t, t.TempDir())
	// Every Docker operation goes through this script. Unexpected verbs fail;
	// fixtures can replace an inspection after its first read to model races.
	script := `#!/bin/sh
set -eu
dir=${0%/*}
printf '%s\n' "$*" >> "$dir/argv"
[ "$PATH" = '` + containerPath + `' ] && [ "$HOME" = /nonexistent ] && [ "$DOCKER_CONFIG" = /nonexistent ] && [ "$DOCKER_HOST" = unix:///var/run/docker.sock ] || exit 90
case "$1" in
ps)
 [ ! -f "$dir/fail-ps" ] || { echo PRIVATE-DIAGNOSTIC >&2; exit 1; }
 list=all
 case "$*" in *--filter*) list=candidates;; esac
 while IFS= read -r id; do [ -f "$dir/gone-$id" ] || printf '%s\n' "$id"; done < "$dir/$list"
 ;;
inspect)
 id=$4
 [ ! -f "$dir/vanish-inspect" ] || { : > "$dir/gone-$id"; exit 1; }
 [ ! -f "$dir/fail-inspect" ] || exit 1
 if [ -f "$dir/seen-$id" ] && [ -f "$dir/$id.next" ]; then /bin/cp "$dir/$id.next" "$dir/$id.json"; fi
 if [ -f "$dir/seen-$id" ] && [ -f "$dir/vanish" ]; then : > "$dir/gone-$id"; exit 1; fi
 : > "$dir/seen-$id"
 /bin/cat "$dir/$id.json"
 ;;
rm)
 [ "$#" = 3 ] && [ "$2" = -f ] || exit 91
 [ ! -f "$dir/vanish-rm" ] || { : > "$dir/gone-$3"; exit 1; }
 [ ! -f "$dir/fail-rm" ] || { echo PRIVATE-DIAGNOSTIC >&2; exit 1; }
 [ -f "$dir/keep" ] || : > "$dir/gone-$3"
 ;;
*) exit 92;;
esac
`
	write(t, filepath.Join(dir, "docker"), script)
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "all"), "")
	write(t, filepath.Join(dir, "candidates"), "")
	var out bytes.Buffer
	s := newContainerSweep(&out, false)
	s.runner.path = dir
	s.processes = &fakeProcessTable{}
	s.parent = resolved(t, t.TempDir())
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.sleep = func(d time.Duration) { now = now.Add(d) }
	return s, &out, dir
}
func sweepContainerFixture(t *testing.T, s *containerSweep, dir string) sweepContainer {
	t.Helper()
	c := sweepContainer{ID: sweepTestID, Name: "/" + sweepTestName, Created: s.now().Add(-time.Hour)}
	c.Config.Image = containerVerifyImage
	c.Config.Labels = map[string]string{containerOwnerLabel: containerOwnerValue}
	write(t, filepath.Join(dir, "candidates"), c.ID+"\n")
	write(t, filepath.Join(dir, "all"), c.ID+"\n")
	saveSweepContainer(t, dir, c, ".json")
	return c
}
func saveSweepContainer(t *testing.T, dir string, c sweepContainer, suffix string) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["State"] = json.RawMessage(`{"Running":true}`)
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, c.ID+suffix), string(data))
}
func sweepCopyFixture(t *testing.T, s *containerSweep) string {
	t.Helper()
	dir, err := os.MkdirTemp(s.parent, "argonreview-container-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "tree", "file"), "copy bytes")
	ageSweepCopy(t, s, dir)
	return dir
}
func ageSweepCopy(t *testing.T, s *containerSweep, dir string) {
	t.Helper()
	old := s.now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
}
func sweepCalls(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func sweepExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if (err == nil) != want || err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s: %v, want exists=%v", path, err, want)
	}
}
func assertNoSweepRM(t *testing.T, dir string) {
	t.Helper()
	if strings.Contains(sweepCalls(t, dir), "rm -f ") {
		t.Fatal("container removed")
	}
}

func TestSweepContainerReclaim(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(strconv.FormatBool(running), func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			c := sweepContainerFixture(t, s, dir)
			if !running {
				data, err := os.ReadFile(filepath.Join(dir, c.ID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(dir, c.ID+".json"), strings.Replace(string(data), `"Running":true`, `"Running":false`, 1))
			}
			t.Setenv("DOCKER_CONTEXT", "caller-sentinel")
			t.Setenv("DOCKER_HOST", "caller-sentinel")
			if err := s.run(); err != nil {
				t.Fatal(err)
			}
			calls := sweepCalls(t, dir)
			inspect := "inspect --format {{json .}} " + c.ID + "\n"
			if strings.Count(calls, inspect) != 2 || !strings.Contains(calls, inspect+"rm -f "+c.ID+"\nps -a -q --no-trunc\n") || strings.Contains(calls, "prune") || strings.Contains(calls, "system") {
				t.Fatalf("calls: %s", calls)
			}
			want := "leftover container " + c.ID + " " + sweepTestName + "\nreclaimed container " + c.ID + "\n"
			if out.String() != want {
				t.Fatalf("report %q", out)
			}
		})
	}
}

func TestSweepContainerRevalidation(t *testing.T) {
	for _, change := range []string{"vanish", "label", "image", "old image", "old verify image", "name", "live", "id"} {
		t.Run(change, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			c := sweepContainerFixture(t, s, dir)
			switch change {
			case "vanish":
				write(t, filepath.Join(dir, "vanish"), "")
			case "label":
				c.Config.Labels = nil
			case "image":
				c.Config.Image = "other"
			case "old image":
				c.Config.Image = containerBaseImage
			case "old verify image":
				c.Config.Image = "sha256:12c5636332bebbfbc6f734bac9c7bb59b3bdd9ac9e4ed373beb180fca8e79e80"
			case "name":
				c.Name = "/foreign"
			case "live":
				c.Created = s.now()
			case "id":
				c.ID = strings.Repeat("b", 64)
			}
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, sweepTestID+".next"), string(data))
			err = s.run()
			if (err != nil) != (change == "id" || change == "image" || change == "old image" || change == "old verify image") {
				t.Fatalf("result: %v", err)
			}
			assertNoSweepRM(t, dir)
			if change == "old verify image" && out.String() != "leftover container "+sweepTestID+" "+sweepTestName+"\nunreclaimed container "+sweepTestID+"\n" {
				t.Fatalf("old verify image revalidation report: %q", out)
			}
		})
	}
}
func TestSweepContainerScope(t *testing.T) {
	for _, change := range []string{"no label", "wrong label", "image", "old image", "old verify image", "unlabelled image", "xyz", "31 hex", "uppercase", "foreign", "double slash"} {
		t.Run(change, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			c := sweepContainerFixture(t, s, dir)
			switch change {
			case "no label":
				c.Config.Labels = nil
			case "wrong label":
				c.Config.Labels[containerOwnerLabel] = "other"
			case "image":
				c.Config.Image = "other"
			case "old image":
				c.Config.Image = containerBaseImage
			case "old verify image":
				c.Config.Image = "sha256:12c5636332bebbfbc6f734bac9c7bb59b3bdd9ac9e4ed373beb180fca8e79e80"
			case "unlabelled image":
				c.Config.Image = "other"
				c.Config.Labels = nil
			case "xyz":
				c.Name = "/argonreview-check-xyz"
			case "31 hex":
				c.Name = "/argonreview-check-" + strings.Repeat("a", 31)
			case "uppercase":
				c.Name = "/argonreview-check-" + strings.Repeat("A", 32)
			case "foreign":
				c.Name = "/other"
			case "double slash":
				c.Name = "//" + sweepTestName
			}
			saveSweepContainer(t, dir, c, ".json")
			err := s.run()
			if change == "image" || change == "old image" || change == "old verify image" {
				if err == nil || out.String() != "unreclaimed container "+c.ID+"\n" {
					t.Fatalf("image mismatch: %v %q", err, out)
				}
			} else if err != nil || out.Len() != 0 {
				t.Fatalf("scope: %v %q", err, out)
			}
			assertNoSweepRM(t, dir)
			if change == "old verify image" && strings.Count(sweepCalls(t, dir), "inspect --format {{json .}} "+c.ID+"\n") != 1 {
				t.Fatal("old verify image must stop at first inspection")
			}
		})
	}
}
func TestSweepLiveness(t *testing.T) {
	for _, kind := range []string{"lock", "client", "created", "copy grace"} {
		t.Run(kind, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			copyDir := sweepCopyFixture(t, s)
			c := sweepContainerFixture(t, s, dir)
			c.Mounts = []sweepMount{{"bind", filepath.Join(copyDir, "tree"), "/work"}}
			switch kind {
			case "lock":
				f, err := lockContainerCopy(copyDir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = f.Close() }()
			case "client":
				p := sweepProc()
				p.ppid = 200
				s.processes = &fakeProcessTable{procs: []procInfo{p}}
			case "created":
				c.Created = s.now().Add(-sweepGrace + time.Second)
			case "copy grace":
				now := s.now().Add(-sweepGrace + time.Second)
				if err := os.Chtimes(copyDir, now, now); err != nil {
					t.Fatal(err)
				}
			}
			saveSweepContainer(t, dir, c, ".json")
			if err := s.run(); err != nil {
				t.Fatal(err)
			}
			assertNoSweepRM(t, dir)
			sweepExists(t, copyDir, true)
			if !strings.Contains(out.String(), "skipped (in use) container "+c.ID) || !strings.Contains(out.String(), "skipped (in use) copy "+copyDir) || strings.Contains(out.String(), "leftover") {
				t.Fatalf("report %q", out)
			}
		})
	}
}
func TestSweepRunnerLock(t *testing.T) {
	s, _, _ := sweepFixture(t)
	tree := containerFixture(t)
	var copyDir string
	var held *os.File
	runner := fakeContainerRunner{func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
		if args[0] == "run" {
			for _, arg := range args {
				if strings.HasSuffix(arg, ",dst=/work,readonly") {
					copyDir = filepath.Dir(strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly"))
				}
			}
		}
		if held == nil {
			var err error
			held, err = os.Open(copyDir)
			if err != nil {
				t.Fatal(err)
			}
			deferClose := held
			t.Cleanup(func() { _ = deferClose.Close() })
		}
		fd, err := openSweepParent(s.parent)
		if err != nil {
			t.Fatal(err)
		}
		defer closeFD(fd)
		f, _, live, err := s.openCopy(fd, filepath.Base(copyDir))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		// Ignore grace: the probe itself must fail while run and cleanup execute.
		if !live || !errors.Is(unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB), unix.EWOULDBLOCK) {
			t.Fatal("runner did not hold lock")
		}
		return 0, nil
	}}
	if _, err := runContainerCheck(context.Background(), tree, "build", ".", runner, s.parent, grantContainerBudget()); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("runner lock survived return: %v", err)
	}
	sweepExists(t, copyDir, false)
	// Check CLOEXEC directly as well as through the child-process fixture.
	dir := sweepCopyFixture(t, s)
	lock, err := lockContainerCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(lock.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("lock is inheritable: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := lockContainerCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
}

func TestSweepLockChildHelper(t *testing.T) {
	if os.Getenv("ARGONREVIEW_LOCK_CHILD") != "1" {
		return
	}
	fd, err := unix.Open(os.Getenv("ARGONREVIEW_LOCK_DIR"), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		os.Exit(3)
	}
	defer closeFD(fd)
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		os.Exit(4)
	}
	os.Exit(0)
}
func TestSweepLockNotInherited(t *testing.T) {
	s, _, dockerDir := sweepFixture(t)
	dir := sweepCopyFixture(t, s)
	lock, err := lockContainerCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The fake Docker child waits until the runner's only lock has closed.
	// Any inherited lock would then keep the child's own probe from succeeding.
	script := fmt.Sprintf(`#!/bin/sh
 dir=${0%%/*}
 : > "$dir/ready"
 while [ ! -f "$dir/release" ]; do /bin/sleep 0.01; done
 export ARGONREVIEW_LOCK_CHILD=1 ARGONREVIEW_LOCK_DIR='%s'
 exec '%s' -test.run=^TestSweepLockChildHelper$
`, dir, exe)
	write(t, filepath.Join(dockerDir, "docker"), script)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		code, err := s.runner.run(ctx, []string{"run"}, dockerEnvironment(), io.Discard, io.Discard)
		if code != 0 && err == nil {
			err = fmt.Errorf("child status %d", code)
		}
		done <- err
	}()
	waitFor(t, filepath.Join(dockerDir, "ready"))
	_ = lock.Close()
	write(t, filepath.Join(dockerDir, "release"), "")
	if err := <-done; err != nil {
		t.Fatalf("Docker child inherited copy lock: %v", err)
	}
}

func TestSweepCopyName(t *testing.T) {
	if containerCopyParent != "/tmp" || sweepGrace != 90*time.Second {
		t.Fatal("trusted policy changed")
	}
	s, _, _ := sweepFixture(t)
	dir := sweepCopyFixture(t, s)
	if !sweepCopyName.MatchString(filepath.Base(dir)) {
		t.Fatalf("MkdirTemp suffix no longer numeric: %s", dir)
	}
	for _, name := range []string{"argonreview-container-0", "argonreview-container-12345678901234567890"} {
		if !sweepCopyName.MatchString(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"argonreview-container-", "argonreview-container-123456789012345678901", "argonreview-container-123x", "xargonreview-container-1", "argonreview-container-1\n"} {
		if sweepCopyName.MatchString(name) {
			t.Fatal(name)
		}
	}
}
func TestSweepUnsafeCopies(t *testing.T) {
	for _, kind := range []string{"symlink", "foreign uid", "mode", "sticky", "file"} {
		t.Run(kind, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			copyDir := sweepCopyFixture(t, s)
			target := resolved(t, t.TempDir())
			write(t, filepath.Join(target, "safe"), "outside")
			if err := os.Chmod(target, 0o700); err != nil {
				t.Fatal(err)
			}
			ageSweepCopy(t, s, target)
			switch kind {
			case "foreign uid":
				s.uid = func() int { return os.Getuid() + 1 }
			case "mode":
				if err := os.Chmod(copyDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "sticky":
				if err := os.Chmod(copyDir, 0o700|os.ModeSticky); err != nil {
					t.Fatal(err)
				}
			case "symlink", "file":
				if err := os.RemoveAll(copyDir); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					symlink(t, target, copyDir)
				} else {
					write(t, copyDir, "file")
				}
			}
			if err := s.run(); err == nil {
				t.Fatal("unsafe copy accepted")
			}
			sweepExists(t, copyDir, true)
			sweepExists(t, filepath.Join(target, "safe"), true)
			assertNoSweepRM(t, dir)
			if out.String() != "unreclaimed copy "+copyDir+"\n" {
				t.Fatalf("report %q", out)
			}
		})
	}
}
func TestSweepCopyReclaim(t *testing.T) {
	s, out, dir := sweepFixture(t)
	copyDir := sweepCopyFixture(t, s)
	target := resolved(t, t.TempDir())
	file := filepath.Join(target, "survives")
	write(t, file, "outside")
	symlink(t, target, filepath.Join(copyDir, "tree", "link"))
	ageSweepCopy(t, s, copyDir)
	if err := s.run(); err != nil {
		t.Fatal(err)
	}
	sweepExists(t, copyDir, false)
	sweepExists(t, file, true)
	assertNoSweepRM(t, dir)
	if out.String() != "leftover copy "+copyDir+"\nreclaimed copy "+copyDir+"\n" {
		t.Fatalf("report %q", out)
	}
}
func TestSweepCopyMounted(t *testing.T) {
	for _, source := range []string{"root", "tree", "file", "alias", "sibling", "stopped", "ancestor", "ancestor_alias", "tmp_alias", "lexical"} {
		t.Run(source, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			if source == "tmp_alias" {
				parent, err := os.MkdirTemp("/tmp", "argonreview-sweep-test-*")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(parent) })
				s.parent = resolved(t, parent)
			}
			copyDir := sweepCopyFixture(t, s)
			c := sweepContainerFixture(t, s, dir)
			c.Name = "/foreign"
			c.Config.Labels = nil
			mount := copyDir
			switch source {
			case "tree":
				mount = filepath.Join(copyDir, "tree")
			case "file":
				mount = filepath.Join(copyDir, "tree", "file")
			case "alias":
				mount = filepath.Join(resolved(t, t.TempDir()), "alias")
				symlink(t, filepath.Join(copyDir, "tree"), mount)
			case "ancestor":
				mount = s.parent
			case "ancestor_alias":
				mount = filepath.Join(resolved(t, t.TempDir()), "alias")
				symlink(t, s.parent, mount)
			case "tmp_alias":
				mount = "/tmp"
			case "lexical":
				mount = filepath.Join(copyDir, "outside")
				symlink(t, resolved(t, t.TempDir()), mount)
				ageSweepCopy(t, s, copyDir)
			case "sibling":
				mount = copyDir + "-sibling"
				mkdir(t, mount)
			}
			c.Mounts = []sweepMount{{"bind", mount, "/anywhere"}}
			saveSweepContainer(t, dir, c, ".json")
			if source == "stopped" {
				data, err := os.ReadFile(filepath.Join(dir, c.ID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(dir, c.ID+".json"), strings.Replace(string(data), `"Running":true`, `"Running":false`, 1))
			}
			if err := s.run(); err != nil {
				t.Fatal(err)
			}
			sweepExists(t, copyDir, source != "sibling")
			assertNoSweepRM(t, dir)
			if source != "sibling" && !strings.Contains(out.String(), "skipped (in use) copy "+copyDir) {
				t.Fatalf("report %q", out)
			}
		})
	}
}
func TestSweepCopyMissingMountSource(t *testing.T) {
	for _, source := range []string{"volume", "not_directory", "lexical"} {
		t.Run(source, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			copyDir := sweepCopyFixture(t, s)
			c := sweepContainerFixture(t, s, dir)
			c.Name = "/foreign"
			mount := filepath.Join(s.parent, "absent-volume", "_data")
			if source == "not_directory" {
				mount = filepath.Join(copyDir, "tree", "file", "_data")
			}
			if source == "lexical" {
				// Keep the missing component: filepath.Join would clean it away.
				mount = copyDir + "/absent/.."
			}
			if _, err := filepath.EvalSymlinks(mount); !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOTDIR) {
				t.Fatalf("expected missing source, got %v", err)
			}
			c.Mounts = []sweepMount{{"volume", mount, "/anywhere"}}
			saveSweepContainer(t, dir, c, ".json")
			if err := s.run(); (err != nil) != (source == "lexical") {
				t.Fatal(err)
			}
			protected := source != "volume"
			sweepExists(t, copyDir, protected)
			want := "reclaimed copy "
			if protected {
				want = "skipped (in use) copy "
			}
			if source == "lexical" {
				want = "unreclaimed copy "
			}
			if !strings.Contains(out.String(), want+copyDir) {
				t.Fatalf("report %q", out)
			}
			assertNoSweepRM(t, dir)
		})
	}
}

func TestSweepInspectionRace(t *testing.T) {
	for _, step := range []string{"containers", "mounts"} {
		t.Run(step, func(t *testing.T) {
			for _, vanished := range []bool{true, false} {
				t.Run(strconv.FormatBool(vanished), func(t *testing.T) {
					s, out, dir := sweepFixture(t)
					copyDir := sweepCopyFixture(t, s)
					sweepContainerFixture(t, s, dir)
					if step == "mounts" {
						write(t, filepath.Join(dir, "candidates"), "")
					}
					failure := "fail-inspect"
					if vanished {
						failure = "vanish-inspect"
					}
					write(t, filepath.Join(dir, failure), "")
					err := s.run()
					if (err == nil) != vanished {
						t.Fatalf("vanished=%v: %v; report %q", vanished, err, out)
					}
					if !vanished && step == "containers" && !strings.Contains(err.Error(), "inspecting container") {
						t.Fatalf("container inspection error missing: %v", err)
					}
					sweepExists(t, filepath.Join(copyDir, "tree", "file"), !vanished)
					assertNoSweepRM(t, dir)
					want := "unreclaimed copy "
					if vanished {
						want = "reclaimed copy "
					}
					if !strings.Contains(out.String(), want+copyDir) {
						t.Fatalf("report %q", out)
					}
				})
			}
		})
	}
}

func TestSweepCopyMountFailure(t *testing.T) {
	for _, failure := range []string{"listing", "inspection", "source"} {
		t.Run(failure, func(t *testing.T) {
			s, _, dir := sweepFixture(t)
			copyDir := sweepCopyFixture(t, s)
			c := sweepContainerFixture(t, s, dir)
			c.Name = "/foreign"
			switch failure {
			case "listing":
				write(t, filepath.Join(dir, "fail-ps"), "")
			case "inspection":
				write(t, filepath.Join(dir, "fail-inspect"), "")
			case "source":
				loop := filepath.Join(s.parent, "loop")
				symlink(t, loop, loop)
				c.Mounts = []sweepMount{{"bind", loop, "/anywhere"}}
				saveSweepContainer(t, dir, c, ".json")
			}
			if err := s.run(); err == nil {
				t.Fatal("mount discovery failure accepted")
			}
			sweepExists(t, filepath.Join(copyDir, "tree", "file"), true)
			assertNoSweepRM(t, dir)
		})
	}
}
func TestSweepCopyDevice(t *testing.T) {
	s, out, _ := sweepFixture(t)
	copyDir := sweepCopyFixture(t, s)
	s.sameDevice = func(uint64, uint64) bool { return false }
	if err := s.run(); err == nil {
		t.Fatal("device boundary crossed")
	}
	sweepExists(t, filepath.Join(copyDir, "tree", "file"), true)
	if !strings.Contains(out.String(), "unreclaimed copy "+copyDir) {
		t.Fatalf("report %q", out)
	}
}

func TestSweepProcessParsing(t *testing.T) {
	text := " 41 1 501 Tue Oct 20 12:34:56 2026 /usr/local/bin/docker run --pull=never --name " + sweepTestName + " --network none --read-only  trailing\n 42 20 0 Mon Jan  2 01:02:03 2006 /bin/other\n"
	got, err := parseSweepProcesses(text)
	if err != nil || len(got) != 2 || got[0].pid != 41 || got[0].ppid != 1 || got[0].uid != 501 || got[0].start != "Tue Oct 20 12:34:56 2026" || !strings.HasSuffix(got[0].command, "  trailing") || got[1].start != "Mon Jan 2 01:02:03 2006" {
		t.Fatalf("parse %+v %v", got, err)
	}
	for _, bad := range []string{"garbage", "0 1 501 Tue Oct 20 12:34:56 2026 /x", "41 x 501 Tue Oct 20 12:34:56 2026 /x", "41 1 x Tue Oct 20 12:34:56 2026 /x", "41 1 501 Bad Oct 20 12:34:56 2026 /x", "41 1 501 Tue Oct 20 12:34:56 2026"} {
		if _, err := parseSweepProcesses(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestSweepClientScope(t *testing.T) {
	for _, change := range []string{"parent", "uid", "relative", "binary", "prefix", "name", "network", "not run"} {
		t.Run(change, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			p := sweepProc()
			switch change {
			case "parent":
				p.ppid = 2
			case "uid":
				p.uid++
			case "relative":
				p.command = strings.TrimPrefix(p.command, "/")
			case "binary":
				p.command = strings.Replace(p.command, "/docker ", "/notdocker ", 1)
			case "prefix":
				p.command = "sh " + p.command
			case "name":
				p.command = strings.Replace(p.command, sweepTestName, "argonreview-check-xyz", 1)
			case "network":
				p.command = strings.Replace(p.command, "--network none ", "--network host ", 1)
			case "not run":
				p.command = strings.Replace(p.command, " run ", " stop ", 1)
			}
			table := &fakeProcessTable{procs: []procInfo{p}}
			s.processes = table
			if err := s.run(); err != nil || len(table.signals) != 0 || out.Len() != 0 {
				t.Fatalf("scope: %v signals %v report %q", err, table.signals, out)
			}
			assertNoSweepRM(t, dir)
		})
	}
}
func TestSweepClientRevalidation(t *testing.T) {
	for _, stage := range []string{"before TERM", "before KILL"} {
		for _, change := range []string{"pid", "start", "command", "uid", "parent", "gone"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				s, _, _ := sweepFixture(t)
				p := sweepProc()
				q := p
				switch change {
				case "pid":
					q.pid++
				case "start":
					q.start = "other"
				case "command":
					q.command += " changed"
				case "uid":
					q.uid++
				case "parent":
					q.ppid = 2
				}
				table := &fakeProcessTable{procs: []procInfo{p}, survive: true}
				s.processes = table
				reads := 0
				table.readFn = func(int) (procInfo, bool, error) {
					reads++
					if stage == "before TERM" || reads >= 52 {
						return q, change != "gone", nil
					}
					return p, true, nil
				}
				if err := s.run(); err != nil {
					t.Fatal(err)
				}
				want := 0
				if stage == "before KILL" {
					want = 1
				}
				if len(table.signals) != want || want == 1 && table.signals[0] != syscall.SIGTERM {
					t.Fatalf("signals %v (reads %d)", table.signals, reads)
				}
			})
		}
	}
}
func TestSweepClientTermination(t *testing.T) {
	for _, kind := range []string{"TERM", "KILL", "survivor", "signal error", "read error"} {
		t.Run(kind, func(t *testing.T) {
			s, out, _ := sweepFixture(t)
			p := sweepProc()
			table := &fakeProcessTable{procs: []procInfo{p}}
			s.processes = table
			table.termSurvive = kind == "KILL"
			table.survive = kind == "survivor"
			if kind == "signal error" {
				table.signalErr = errors.New("PRIVATE-DIAGNOSTIC")
			}
			if kind == "read error" {
				table.readErr = errors.New("PRIVATE-DIAGNOSTIC")
			}
			err := s.run()
			failed := kind == "survivor" || strings.Contains(kind, "error")
			if (err != nil) != failed {
				t.Fatalf("result %v", err)
			}
			want := []syscall.Signal{syscall.SIGTERM}
			if kind == "KILL" || kind == "survivor" {
				want = append(want, syscall.SIGKILL)
			}
			if kind == "read error" {
				want = nil
			}
			if !reflect.DeepEqual(table.signals, want) {
				t.Fatalf("signals %v, want %v", table.signals, want)
			}
			for _, pid := range table.signalled {
				if pid != p.pid {
					t.Fatalf("wrong pid %d", pid)
				}
			}
			if failed && !strings.Contains(out.String(), "unreclaimed client 41") || !failed && !strings.Contains(out.String(), "reclaimed client 41") {
				t.Fatalf("report %q", out)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE-DIAGNOSTIC") {
				t.Fatal("unsafe error")
			}
		})
	}
}
func TestSweepDryRun(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(strconv.FormatBool(dirty), func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			s.dry = true
			table := &fakeProcessTable{}
			s.processes = table
			var copyDir string
			if dirty {
				sweepContainerFixture(t, s, dir)
				copyDir = sweepCopyFixture(t, s)
				table.procs = []procInfo{sweepProc()}
			}
			if err := s.run(); (err != nil) != dirty {
				t.Fatalf("result: %v", err)
			}
			if len(table.signals) != 0 {
				t.Fatal("dry-run signalled")
			}
			assertNoSweepRM(t, dir)
			if dirty {
				sweepExists(t, filepath.Join(copyDir, "tree", "file"), true)
				for _, kind := range []string{"container", "copy", "client"} {
					if !strings.Contains(out.String(), "leftover "+kind+" ") {
						t.Fatalf("report %q", out)
					}
				}
			} else if out.Len() != 0 {
				t.Fatalf("clean report %q", out)
			}
		})
	}
}
func TestSweepFailures(t *testing.T) {
	for _, kind := range []string{"missing docker", "ps", "garbage", "invalid id", "rm", "still listed", "process listing"} {
		t.Run(kind, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			sweepContainerFixture(t, s, dir)
			copyDir := sweepCopyFixture(t, s)
			switch kind {
			case "missing docker":
				s.runner.path = t.TempDir()
			case "ps":
				write(t, filepath.Join(dir, "fail-ps"), "")
			case "garbage":
				write(t, filepath.Join(dir, sweepTestID+".json"), "PRIVATE-DIAGNOSTIC")
			case "invalid id":
				write(t, filepath.Join(dir, "candidates"), "--bad\n")
			case "rm":
				write(t, filepath.Join(dir, "fail-rm"), "")
			case "still listed":
				write(t, filepath.Join(dir, "keep"), "")
			case "process listing":
				s.processes = &fakeProcessTable{listErr: errors.New("PRIVATE-DIAGNOSTIC")}
			}
			err := s.run()
			if err == nil || strings.Contains(err.Error()+out.String(), "PRIVATE-DIAGNOSTIC") {
				t.Fatalf("error/report: %v %q", err, out)
			}
			if kind == "missing docker" || kind == "ps" || kind == "garbage" {
				sweepExists(t, filepath.Join(copyDir, "tree", "file"), true)
			}
		})
	}
}
func TestSweepIndependentSteps(t *testing.T) {
	s, out, dir := sweepFixture(t)
	write(t, filepath.Join(dir, "fail-ps"), "")
	table := &fakeProcessTable{procs: []procInfo{sweepProc()}}
	s.processes = table
	if err := s.run(); err == nil || len(table.signals) != 1 || !strings.Contains(out.String(), "reclaimed client 41") {
		t.Fatalf("independent steps: %v %q", err, out)
	}
}
func TestSweepDispatchTrust(t *testing.T) {
	for _, args := range [][]string{{"container-sweep"}, {"container-sweep", "--dry-run"}, {"container-sweep", "--bad"}, {"container-sweep", "--dry-run", "extra"}} {
		var out, log bytes.Buffer
		want := 1
		if len(args) > 2 || len(args) == 2 && args[1] != "--dry-run" {
			want = 2
		}
		if code := run(args, nil, &out, &log); code != want || out.Len() != 0 || log.Len() == 0 || want == 1 && !strings.Contains(log.String(), "build") {
			t.Fatalf("dispatch %v: %d %q %q", args, code, &out, &log)
		}
	}
}
func TestSweepStartup(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			s, _, _ := sweepFixture(t)
			tree := containerFixture(t)
			sweep := func(out io.Writer) error {
				_, _ = fmt.Fprintln(out, "reclaimed client 41")
				if failed {
					return fmt.Errorf("listing containers: %w", errors.New("sweep failed"))
				}
				return nil
			}
			var out, log bytes.Buffer
			code := startVerifyMCP(context.Background(), []string{"--trusted-source", resolved(t, t.TempDir()), "--worktree", resolved(t, t.TempDir())}, nil, func(string) error { return nil }, sweep, noVerify(t), strings.NewReader(mcpInit), &out, &log)
			want := 0
			if failed {
				want = 1
			}
			if code != want || !strings.Contains(log.String(), "reclaimed client 41") || failed && (out.Len() != 0 || !strings.Contains(log.String(), "listing containers: sweep failed")) || !failed && len(readMCPResponses(t, out.String())) != 1 {
				t.Fatalf("MCP startup %d %q %q", code, &out, &log)
			}
			out.Reset()
			log.Reset()
			calls := 0
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) { calls++; return 0, nil }}
			code = startContainerCheck([]string{tree, "build", "."}, &out, &log, nil, sweep, runner, s.parent, grantContainerBudget())
			if code != want || failed && (out.Len() != 0 || calls != 0) || !failed && (!strings.HasPrefix(out.String(), "verified\n") || calls != 3) || !strings.Contains(log.String(), "reclaimed client 41") {
				t.Fatalf("check startup %d calls %d %q %q", code, calls, &out, &log)
			}
		})
	}
}

func TestSweepInspectionIDs(t *testing.T) {
	for _, id := range []string{strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("b", 64)} {
		t.Run(id, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			c := sweepContainerFixture(t, s, dir)
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, sweepTestID+".json"), strings.Replace(string(data), c.ID, id, 1))
			if err := s.run(); err == nil || strings.Contains(out.String(), "leftover container") {
				t.Fatalf("invalid inspection identity accepted: %v %q", err, out)
			}
			assertNoSweepRM(t, dir)
		})
	}
}

func TestSweepStartupChecks(t *testing.T) {
	source, tree := resolved(t, t.TempDir()), resolved(t, t.TempDir())
	for _, kind := range []string{"usage", "environment", "path", "trust"} {
		t.Run(kind, func(t *testing.T) {
			args := []string{"--trusted-source", source, "--worktree", tree}
			var env []string
			switch kind {
			case "usage":
				args = nil
			case "environment":
				env = []string{"FORBIDDEN=x"}
			case "path":
				args[1] += "/absent"
			}
			calls := 0
			sweep := func(io.Writer) error { calls++; return nil }
			trust := func(string) error {
				if kind == "trust" {
					return errors.New("trust refused")
				}
				return nil
			}
			var out, log bytes.Buffer
			code := startVerifyMCP(context.Background(), args, env, trust, sweep, noVerify(t), strings.NewReader(mcpInit), &out, &log)
			if code == 0 || out.Len() != 0 || calls != 0 {
				t.Fatalf("sweep before startup checks: %d calls %d stdout %q", code, calls, &out)
			}
			calls = 0
			code = startContainerCheck([]string{tree, "build", "."}, &out, &log, errors.New("trust refused"), sweep, nil, t.TempDir(), grantContainerBudget())
			if code != 1 || calls != 0 || out.Len() != 0 {
				t.Fatalf("check swept before trust: %d calls %d", code, calls)
			}
		})
	}
}
