package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const guardBinary = "/opt/argonreview/argonreview"

// fixture returns a fake account home whose .gemini holds shared settings,
// and a function that fails the test if any of them changed.
func fixture(t *testing.T) (string, func()) {
	t.Helper()
	account := resolved(t, t.TempDir())
	shared := map[string]string{
		".gemini/antigravity-cli/settings.json": `{"permissions":{"allow":["command(git diff)"]}}`,
		".gemini/config/hooks.json":             `{"tencle":{}}`,
	}
	for path, content := range shared {
		write(t, filepath.Join(account, path), content)
	}
	return account, func() {
		t.Helper()
		for path, want := range shared {
			if got, err := os.ReadFile(filepath.Join(account, path)); err != nil || string(got) != want {
				t.Fatalf("shared %s changed: %q, %v", path, got, err)
			}
		}
	}
}

// snapshot records every file under dir with its content, so a refusal can
// be shown to leave the dedicated HOME untouched too.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[path] = "<dir>"
		} else if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			files[path] = "-> " + target
		} else {
			data, _ := os.ReadFile(path)
			files[path] = string(data)
		}
		return nil
	})
	return files
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	mkdir(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func ownedFiles(t *testing.T, guard string) []ownedFile {
	t.Helper()
	files, err := owned(guard)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestPrepare(t *testing.T) {
	account, unchanged := fixture(t)
	// A dedicated directory under the account's home is allowed.
	home := filepath.Join(account, "dev", "agy-home")
	kept := map[string]string{
		".gemini/antigravity-cli/antigravity-oauth-token":  "token",
		".gemini/antigravity-cli/conversations/1.db":       "conversation",
		".gemini/config/projects/default-cli-project.json": "{}",
	}
	for path, content := range kept {
		write(t, filepath.Join(home, path), content)
	}
	// Antigravity's own rewrite, adding a trusted workspace, is replaced.
	write(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), `{"trustedWorkspaces":["/"]}`)
	got, err := prepare(home, account, guardBinary)
	if err != nil || got != home {
		t.Fatalf("prepare = %q, %v", got, err)
	}
	for _, f := range ownedFiles(t, guardBinary) {
		data, err := os.ReadFile(filepath.Join(home, ".gemini", f.dir, f.name))
		if err != nil || !bytes.Equal(data, f.content) {
			t.Fatalf("%s/%s = %q, %v", f.dir, f.name, data, err)
		}
	}
	for path, want := range kept {
		if data, err := os.ReadFile(filepath.Join(home, path)); err != nil || string(data) != want {
			t.Fatalf("kept %s changed: %q, %v", path, data, err)
		}
	}
	// A fresh HOME gets its directories created.
	fresh := filepath.Join(account, "dev", "fresh")
	mkdir(t, fresh)
	if _, err := prepare(fresh, account, guardBinary); err != nil {
		t.Fatal(err)
	}
	unchanged()
}

func TestPrepareSerialises(t *testing.T) {
	for _, phase := range []string{"before writes", "after writes"} {
		t.Run(phase, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := resolved(t, t.TempDir())
			if _, err := prepare(home, account, guardBinary); err != nil {
				t.Fatal(err)
			}
			paused, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			resume := func() { releaseOnce.Do(func() { close(release) }) }
			pause := func() { close(paused); <-release }
			firstHooks := hooks{beforeWrite: pause}
			if phase == "after writes" {
				firstHooks = hooks{afterWrite: pause}
			}
			first, second := make(chan error, 1), make(chan error, 1)
			firstExited := make(chan struct{})
			var secondExited chan struct{}
			go func() {
				defer close(firstExited)
				_, err := prepareWith(home, account, guardBinary, firstHooks)
				first <- err
			}()
			// Release and join even on a failed assertion, before TempDir cleanup.
			defer func() {
				resume()
				<-firstExited
				if secondExited != nil {
					<-secondExited
				}
			}()
			select {
			case <-paused:
			case err := <-first:
				t.Fatalf("first prepare did not pause: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("first prepare did not pause")
			}
			// Check the kernel lock too, so removing it fails deterministically
			// even if the second goroutine is not scheduled during the wait.
			fd, err := unix.Open(filepath.Join(home, prepareLockName), unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer closeFD(fd)
			if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != unix.EWOULDBLOCK {
				t.Fatalf("first prepare does not hold the lock: %v", err)
			}
			// Model the temporary file window without relying on write timing.
			tmp := filepath.Join(home, ".gemini/config/agents/.argonreview-61829")
			write(t, tmp, "in progress")
			started := make(chan struct{})
			secondExited = make(chan struct{})
			go func() {
				defer close(secondExited)
				_, err := prepareWith(home, account, guardBinary, hooks{beforeLock: func() { close(started) }})
				second <- err
			}()
			select {
			case <-started:
			case err := <-second:
				t.Fatalf("second prepare did not reach the lock: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("second prepare did not reach the lock")
			}
			select {
			case err := <-second:
				t.Fatalf("second prepare finished while the first held the lock: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if err := os.Remove(tmp); err != nil {
				t.Fatal(err)
			}
			resume()
			for i, done := range []chan error{first, second} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("prepare %d: %v", i+1, err)
					}
				case <-time.After(10 * time.Second):
					t.Fatalf("prepare %d did not finish", i+1)
				}
			}
			unchanged()
		})
	}
}

func TestPrepareLockTimeout(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing config=%t", existing), func(t *testing.T) {
			account, unchanged := fixture(t)
			home := resolved(t, t.TempDir())
			if existing {
				for _, f := range ownedFiles(t, guardBinary) {
					write(t, filepath.Join(home, ".gemini", f.dir, f.name), "must not be rewritten")
				}
			}
			path := filepath.Join(home, prepareLockName)
			fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer closeFD(fd)
			if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, home)
			writing := false
			const wait = 200 * time.Millisecond
			start := time.Now()
			got, err := prepareWith(home, account, guardBinary, hooks{
				lockWait: wait, lockPoll: 10 * time.Millisecond,
				beforeWrite: func() { writing = true },
			})
			elapsed := time.Since(start)
			if got != "" || err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "another argonreview prepare holds") || !strings.Contains(err.Error(), "wait for it or kill it") {
				t.Fatalf("prepare = %q, %v; want lock holder refusal naming %s", got, err, path)
			}
			if elapsed < wait || elapsed > 2*time.Second {
				t.Fatalf("prepare took %s; want %s to 2s", elapsed, wait)
			}
			if writing || !equal(before, snapshot(t, home)) {
				t.Fatal("timed-out prepare reached writes or changed HOME, including config/agents")
			}
			unchanged()
		})
	}
}

func TestPrepareConcurrentFreshHome(t *testing.T) {
	account, unchanged := fixture(t)
	home := resolved(t, t.TempDir())
	const n = 8
	done := make(chan error, n)
	for range n {
		go func() {
			_, err := prepare(home, account, guardBinary)
			done <- err
		}()
	}
	for range n {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	unchanged()
}

func TestPrepareLock(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "directory", "FIFO"} {
		t.Run(kind, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := resolved(t, t.TempDir())
			path := filepath.Join(home, prepareLockName)
			switch kind {
			case "regular":
				write(t, path, "kept")
			case "symlink":
				symlink(t, filepath.Join(account, ".gemini/config/hooks.json"), path)
			case "directory":
				mkdir(t, path)
			case "FIFO":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				_, err := prepare(home, account, guardBinary)
				if kind == "regular" {
					if err != nil {
						t.Fatal(err)
					}
					if data, _ := os.ReadFile(path); string(data) != "kept" {
						t.Fatal("lock content changed")
					}
				} else if err == nil || !strings.Contains(err.Error(), "prepare lock") {
					t.Fatalf("invalid lock accepted: %v", err)
				}
			}
			unchanged()
		})
	}
}

func TestPrepareRefusesExtraAgentFiles(t *testing.T) {
	// Use an exited process's PID so the leftover has no live writer.
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	leftover := fmt.Sprintf(".argonreview-%d", dead.Process.Pid)
	for _, name := range []string{"unknown.md", leftover, prepareLockName} {
		t.Run(name, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := resolved(t, t.TempDir())
			write(t, filepath.Join(home, prepareLockName), "")
			write(t, filepath.Join(home, ".gemini/config/agents", name), "leftover")
			before := snapshot(t, home)
			for range 2 {
				if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "customisation") || !strings.Contains(err.Error(), "remove it by hand") {
					t.Fatalf("extra agent file accepted: %v", err)
				}
			}
			if !equal(before, snapshot(t, home)) {
				t.Fatal("refusal changed HOME")
			}
			unchanged()
		})
	}
}

// The hook command decodes to exactly the single-quoted path and "guard",
// whatever JSON-special characters the path holds.
func TestHookCommand(t *testing.T) {
	for _, path := range []string{guardBinary, `/opt/a "b"/argonreview`, `/opt/a\b/argonreview`} {
		var hooks map[string]map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		files := ownedFiles(t, path)
		if err := json.Unmarshal(files[1].content, &hooks); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		entries := hooks["ribbitto-guard"]["PreToolUse"]
		if len(hooks) != 1 || len(entries) != 1 || entries[0].Matcher != "*" || len(entries[0].Hooks) != 1 || entries[0].Hooks[0].Command != "'"+path+"' guard" {
			t.Fatalf("%s: hooks = %+v", path, hooks)
		}
	}
	for _, path := range []string{"/opt/it's/argonreview", "/opt/a\nb", "argonreview"} {
		if _, err := owned(path); err == nil {
			t.Fatalf("%q accepted", path)
		}
	}
}

func TestPrepareRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, account, home string) string // returns RIBBITTO_ARGON_HOME
		want  string
	}{
		{"account home", func(_ *testing.T, account, _ string) string { return account }, "account's home"},
		{"ancestor of the account home", func(_ *testing.T, account, _ string) string { return filepath.Dir(account) }, "account's home"},
		{"the shared .gemini", func(_ *testing.T, account, _ string) string { return filepath.Join(account, ".gemini") }, "overlaps"},
		{"inside the shared .gemini", func(_ *testing.T, account, _ string) string { return filepath.Join(account, ".gemini/config") }, "overlaps"},
		{"a HOME that is a symlink", func(t *testing.T, _, home string) string {
			link := filepath.Join(filepath.Dir(home), "alias")
			symlink(t, home, link)
			return link
		}, "without symlinks"},
		{"a symlinked ancestor", func(t *testing.T, _, home string) string {
			link := filepath.Join(filepath.Dir(home), "parent-alias")
			symlink(t, filepath.Dir(home), link)
			return filepath.Join(link, filepath.Base(home))
		}, "without symlinks"},
		{"relative", func(_ *testing.T, _, _ string) string { return "agy-home" }, "absolute path"},
		{"unclean", func(_ *testing.T, _, home string) string { return home + "/../agy-home" }, "absolute path"},
		{"unset", func(_ *testing.T, _, _ string) string { return "" }, "absolute path"},
		{"symlinked .gemini", func(t *testing.T, account, home string) string {
			symlink(t, filepath.Join(account, ".gemini"), filepath.Join(home, ".gemini"))
			return home
		}, "not a plain directory"},
		{"symlinked config", func(t *testing.T, account, home string) string {
			symlink(t, filepath.Join(account, ".gemini/config"), filepath.Join(home, ".gemini/config"))
			return home
		}, "not a plain directory"},
		{"symlinked settings", func(t *testing.T, account, home string) string {
			symlink(t, filepath.Join(account, ".gemini/antigravity-cli/settings.json"), filepath.Join(home, ".gemini/antigravity-cli/settings.json"))
			return home
		}, "not a regular file"},
		// The symlink at hooks.json is found before settings.json, which
		// exists and differs, is rewritten.
		{"symlinked hooks with existing settings", func(t *testing.T, account, home string) string {
			write(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), "mine")
			symlink(t, filepath.Join(account, ".gemini/config/hooks.json"), filepath.Join(home, ".gemini/config/hooks.json"))
			return home
		}, "not a regular file"},
		{".agents", func(t *testing.T, _, home string) string { mkdir(t, filepath.Join(home, ".agents")); return home }, "customisation"},
		{"hooks in antigravity-cli", func(t *testing.T, _, home string) string {
			write(t, filepath.Join(home, ".gemini/antigravity-cli/hooks.json"), "{}")
			return home
		}, "customisation"},
		{"MCP in antigravity-cli", func(t *testing.T, _, home string) string {
			write(t, filepath.Join(home, ".gemini/antigravity-cli/mcp_config.json"), "{}")
			return home
		}, "customisation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := filepath.Join(account, "dev", "agy-home")
			mkdir(t, home)
			target := tc.setup(t, account, home)
			// Keep the persistent lock in the fixture so the snapshot still
			// proves that a refusal changes nothing.
			write(t, filepath.Join(home, prepareLockName), "")
			before := snapshot(t, home)
			if _, err := prepare(target, account, guardBinary); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if after := snapshot(t, home); !equal(before, after) {
				t.Fatalf("the dedicated HOME changed:\n%v\n%v", before, after)
			}
			unchanged()
		})
	}
	for _, dir := range ownedDirs {
		for _, name := range refusedCustomisation {
			t.Run(dir+"/"+name, func(t *testing.T) {
				account, unchanged := fixture(t)
				home := filepath.Join(account, "dev", "agy-home")
				write(t, filepath.Join(home, ".gemini", dir, name), "{}")
				write(t, filepath.Join(home, prepareLockName), "")
				before := snapshot(t, home)
				if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "customisation") {
					t.Fatalf("err = %v", err)
				}
				if !equal(before, snapshot(t, home)) {
					t.Fatal("the dedicated HOME changed")
				}
				unchanged()
			})
		}
	}
}

// The shared .gemini may live elsewhere through a symlink; a HOME that
// contains its target is refused (Codex on #1075).
func TestPrepareRefusesASymlinkedSharedGemini(t *testing.T) {
	account := resolved(t, t.TempDir())
	elsewhere := resolved(t, t.TempDir())
	write(t, filepath.Join(elsewhere, ".gemini/config/hooks.json"), `{"tencle":{}}`)
	symlink(t, filepath.Join(elsewhere, ".gemini"), filepath.Join(account, ".gemini"))
	if _, err := prepare(elsewhere, account, guardBinary); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(elsewhere, ".gemini/config/hooks.json")); string(data) != `{"tencle":{}}` {
		t.Fatalf("shared hooks changed: %q", data)
	}
}

// On a case-insensitive file system a differently spelled path names the
// same directory; identities, not spellings, decide (Codex on #1075).
func TestPrepareRefusesACaseAlias(t *testing.T) {
	account, unchanged := fixture(t)
	alias := filepath.Join(filepath.Dir(account), strings.ToUpper(filepath.Base(account)))
	if alias == account {
		alias = filepath.Join(filepath.Dir(account), strings.ToLower(filepath.Base(account)))
	}
	a, errA := os.Stat(account)
	b, errB := os.Stat(alias)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("the file system is case-sensitive")
	}
	for _, home := range []string{alias, filepath.Join(alias, ".gemini"), filepath.Join(account, ".GEMINI")} {
		if _, err := prepare(home, account, guardBinary); err == nil {
			t.Fatalf("%s accepted", home)
		}
	}
	unchanged()
}

// A directory swapped for a symlink after the writes is found by walking the
// path again (Codex on #1075).
func TestPrepareRefusesASwapAfterWriting(t *testing.T) {
	for _, tc := range []struct {
		dir     string
		symlink bool
	}{{".gemini", true}, {".gemini/config", true}, {".gemini", false}, {".gemini/antigravity-cli", false}} {
		dir := tc.dir
		t.Run(fmt.Sprintf("%s symlink=%v", dir, tc.symlink), func(t *testing.T) {
			account, unchanged := fixture(t)
			home := filepath.Join(account, "dev", "agy-home")
			mkdir(t, home)
			swap := func(home string) {
				path := filepath.Join(home, dir)
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				// A symlink is refused by the walk; another real directory
				// only by its identity.
				if tc.symlink {
					symlink(t, filepath.Join(account, dir), path)
				} else {
					mkdir(t, filepath.Join(path, "antigravity-cli"))
					mkdir(t, filepath.Join(path, "config"))
				}
			}
			if _, err := prepareWith(home, account, guardBinary, hooks{afterWrite: func() { swap(home) }}); err == nil {
				t.Fatal("a swapped directory was accepted")
			}
			unchanged()
		})
	}
}

// A dangling shared symlink names a place this run could create; it is
// refused before anything is created (Codex on #1075).
func TestPrepareRefusesADanglingSharedSymlink(t *testing.T) {
	for _, rel := range []string{".gemini", ".gemini/config", ".gemini/antigravity-cli"} {
		t.Run(rel, func(t *testing.T) {
			account := resolved(t, t.TempDir())
			home := resolved(t, t.TempDir())
			if rel != ".gemini" {
				mkdir(t, filepath.Join(account, ".gemini"))
			}
			symlink(t, filepath.Join(home, rel), filepath.Join(account, rel))
			write(t, filepath.Join(home, prepareLockName), "")
			before := snapshot(t, home)
			if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
				t.Fatalf("err = %v", err)
			}
			if !equal(before, snapshot(t, home)) {
				t.Fatal("the dedicated HOME changed")
			}
			if _, err := os.Lstat(filepath.Join(home, ".gemini")); err == nil {
				t.Fatal("the dangling target was created")
			}
		})
	}
}

// A shared directory whose parent can be searched but not read hides the
// chain above it; that refuses rather than shortening the chain.
func TestPrepareRefusesAnUnwalkableChain(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	account := resolved(t, t.TempDir())
	elsewhere := resolved(t, t.TempDir())
	parent := filepath.Join(elsewhere, "p")
	mkdir(t, filepath.Join(parent, ".gemini"))
	symlink(t, filepath.Join(parent, ".gemini"), filepath.Join(account, ".gemini"))
	if err := os.Chmod(parent, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	home := filepath.Join(account, "dev", "agy-home")
	mkdir(t, home)
	if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "cannot walk up") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".gemini")); err == nil {
		t.Fatal("prepare wrote before refusing")
	}
}

// An owned directory that can be listed but not searched hides what is in
// it; that refuses before settings.json is rewritten (Codex on #1075).
func TestPrepareRefusesWhatItCannotInspect(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches any directory")
	}
	account, unchanged := fixture(t)
	home := filepath.Join(account, "dev", "agy-home")
	write(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), "mine")
	symlink(t, filepath.Join(account, ".gemini/config/hooks.json"), filepath.Join(home, ".gemini/config/hooks.json"))
	config := filepath.Join(home, ".gemini/config")
	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(config, 0o700) })
	if _, err := prepare(home, account, guardBinary); err == nil {
		t.Fatal("accepted")
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".gemini/antigravity-cli/settings.json")); string(data) != "mine" {
		t.Fatalf("settings.json was rewritten: %q", data)
	}
	unchanged()
}

// Refusals close every descriptor they opened.
func TestPrepareKeepsNoDescriptors(t *testing.T) {
	account, _ := fixture(t)
	home := filepath.Join(account, "dev", "agy-home")
	mkdir(t, filepath.Join(home, ".gemini/antigravity-cli"))
	symlink(t, filepath.Join(account, ".gemini/config"), filepath.Join(home, ".gemini/config"))
	open := func() int {
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Skip("no /dev/fd")
		}
		return len(entries)
	}
	before := open()
	for range 50 {
		if _, err := prepare(home, account, guardBinary); err == nil {
			t.Fatal("accepted")
		}
	}
	if after := open(); after > before {
		t.Fatalf("descriptors grew from %d to %d", before, after)
	}
}

// A checked directory swapped for another real one before the first write is
// refused, and nothing is written into the replacement (Copilot on #1075).
func TestPrepareRefusesASwapBeforeWriting(t *testing.T) {
	for _, planted := range []string{"", "skills.json"} {
		t.Run("planted="+planted, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := filepath.Join(account, "dev", "agy-home")
			mkdir(t, filepath.Join(home, ".gemini/config"))
			mkdir(t, filepath.Join(home, ".gemini/antigravity-cli"))
			config := filepath.Join(home, ".gemini/config")
			swap := func() {
				if err := os.Rename(config, config+".old"); err != nil {
					t.Fatal(err)
				}
				mkdir(t, config)
				if planted != "" {
					write(t, filepath.Join(config, planted), "{}")
				}
			}
			if _, err := prepareWith(home, account, guardBinary, hooks{beforeWrite: swap}); err == nil {
				t.Fatal("a directory swapped before writing was accepted")
			}
			if _, err := os.Lstat(filepath.Join(config, "hooks.json")); err == nil {
				t.Fatal("hooks.json was written into the replacement")
			}
			unchanged()
		})
	}
}

// A directory absent when checked, created with customisation in it before
// the first write, is checked again and refused.
func TestPrepareRefusesADirectoryPlantedBeforeWriting(t *testing.T) {
	account, unchanged := fixture(t)
	home := filepath.Join(account, "dev", "agy-home")
	mkdir(t, filepath.Join(home, ".gemini/antigravity-cli"))
	plant := func() { write(t, filepath.Join(home, ".gemini/config/skills.json"), "{}") }
	if _, err := prepareWith(home, account, guardBinary, hooks{beforeWrite: plant}); err == nil || !strings.Contains(err.Error(), "changed after it was checked") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".gemini/config/hooks.json")); err == nil {
		t.Fatal("hooks.json was written next to the planted customisation")
	}
	unchanged()
}

// .gemini replaced by an empty directory after the checks gets nothing
// created in it, whether or not it held owned directories (Codex on #1075).
// An owned directory removed since the checks refuses too.
func TestPrepareRefusesAGeminiChangeBeforeCreating(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, gemini string)
		change func(t *testing.T, gemini string)
	}{
		{"swapped, with owned directories", func(t *testing.T, gemini string) {
			write(t, filepath.Join(gemini, "antigravity-cli/settings.json"), "mine")
		}, swapForEmpty},
		{"swapped, without owned directories", func(t *testing.T, gemini string) { mkdir(t, gemini) }, swapForEmpty},
		{"an owned directory removed", func(t *testing.T, gemini string) {
			write(t, filepath.Join(gemini, "antigravity-cli/settings.json"), "mine")
		}, func(t *testing.T, gemini string) {
			if err := os.RemoveAll(filepath.Join(gemini, "antigravity-cli")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, unchanged := fixture(t)
			home := filepath.Join(account, "dev", "agy-home")
			gemini := filepath.Join(home, ".gemini")
			tc.setup(t, gemini)
			test := hooks{beforeWrite: func() { tc.change(t, gemini) }}
			if _, err := prepareWith(home, account, guardBinary, test); err == nil || !strings.Contains(err.Error(), "changed after it was checked") {
				t.Fatalf("err = %v", err)
			}
			if entries, err := os.ReadDir(gemini); err != nil || len(entries) != 0 {
				t.Fatalf(".gemini now holds %v, %v", entries, err)
			}
			unchanged()
		})
	}
}

func swapForEmpty(t *testing.T, gemini string) {
	if err := os.Rename(gemini, gemini+".old"); err != nil {
		t.Fatal(err)
	}
	mkdir(t, gemini)
}

// Customisation planted in an existing owned directory after the checks is
// found by the checks run again before anything is created (Codex on #1075).
func TestPrepareChecksAgainBeforeCreating(t *testing.T) {
	account, unchanged := fixture(t)
	home := filepath.Join(account, "dev", "agy-home")
	mkdir(t, filepath.Join(home, ".gemini/antigravity-cli"))
	plant := func() { write(t, filepath.Join(home, ".gemini/antigravity-cli/skills.json"), "{}") }
	if _, err := prepareWith(home, account, guardBinary, hooks{beforeWrite: plant}); err == nil || !strings.Contains(err.Error(), "customisation") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".gemini/config")); err == nil {
		t.Fatal("config was created before the refusal")
	}
	unchanged()
}

// A shared directory moved into HOME between prepare creating a directory
// and opening it, with a symlink left at its old place, keeps its identity
// and is refused before anything is created in it or written (Codex on
// #1075).
func TestPrepareRefusesASharedDirectoryMovedInDuringCreation(t *testing.T) {
	for _, tc := range []struct{ created, shared string }{
		{".gemini", ".gemini"},
		{"config", ".gemini/config"},
		{"agents", ".gemini/config/agents"},
	} {
		t.Run(tc.created, func(t *testing.T) {
			account := resolved(t, t.TempDir())
			sharedDir := filepath.Join(account, tc.shared)
			mkdir(t, sharedDir)
			if tc.created == "config" {
				write(t, filepath.Join(sharedDir, "hooks.json"), `{"tencle":{}}`)
			}
			home := filepath.Join(account, "dev", "agy-home")
			mkdir(t, home)
			if tc.created == "config" {
				mkdir(t, filepath.Join(home, ".gemini/antigravity-cli"))
			}
			ours := filepath.Join(home, tc.shared)
			swap := func(name string) {
				if name != tc.created {
					return
				}
				if err := os.Rename(ours, ours+".aside"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(sharedDir, ours); err != nil {
					t.Fatal(err)
				}
				symlink(t, ours, sharedDir)
			}
			if _, err := prepareWith(home, account, guardBinary, hooks{afterMkdir: swap}); err == nil || !strings.Contains(err.Error(), "shared") {
				t.Fatalf("err = %v", err)
			}
			entries, err := os.ReadDir(ours)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.created == "config" {
				want = 1 // the shared hooks.json, untouched below
			}
			if len(entries) != want {
				t.Fatalf("the shared directory now holds %v", entries)
			}
			if tc.created == "config" {
				if data, _ := os.ReadFile(filepath.Join(ours, "hooks.json")); string(data) != `{"tencle":{}}` {
					t.Fatalf("the shared hooks.json was rewritten: %q", data)
				}
			}
		})
	}
}

// A directory that appeared between the re-check and its creation is not
// this run's to use.
func TestMkdirOpenRefusesAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "config"))
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFD(fd)
	if got, err := mkdirOpen(fd, "config", nil); err == nil {
		closeFD(got)
		t.Fatal("an existing directory was accepted")
	}
}

// The shared .gemini is resolved again just before writing and after it.
// Turned meanwhile into a symlink to this HOME's .gemini or to HOME itself, it
// is refused (Copilot on #1075).
func TestPrepareRefusesASharedSwap(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		after, empty bool
	}{
		{"before writing, to HOME/.gemini", ".gemini", false, false},
		{"before writing, to HOME", "", false, false},
		// Dangling until prepare would create HOME/.gemini (Argon on #1075).
		{"before writing, to an empty HOME's .gemini", ".gemini", false, true},
		{"after writing, to HOME", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := resolved(t, t.TempDir())
			write(t, filepath.Join(account, ".gemini/config/hooks.json"), `{"tencle":{}}`)
			home := filepath.Join(account, "dev", "agy-home")
			mkdir(t, home)
			if !tc.empty {
				write(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), "mine")
			}
			write(t, filepath.Join(home, prepareLockName), "")
			before := snapshot(t, home)
			swap := func() {
				shared := filepath.Join(account, ".gemini")
				if err := os.Rename(shared, shared+".old"); err != nil {
					t.Fatal(err)
				}
				symlink(t, filepath.Join(home, tc.target), shared)
			}
			test := hooks{beforeWrite: swap}
			if tc.after {
				test = hooks{afterWrite: swap}
			}
			if _, err := prepareWith(home, account, guardBinary, test); err == nil {
				t.Fatal("accepted")
			}
			// Before writing, nothing is created or rewritten in HOME.
			if !tc.after && !equal(before, snapshot(t, home)) {
				t.Fatalf("HOME changed:\n%v\n%v", before, snapshot(t, home))
			}
		})
	}
}

// HOME itself swapped after the checks: the checked directory, now moved
// aside, gets nothing created or written (Argon on #1075).
func TestPrepareRefusesAHomeSwapBeforeWriting(t *testing.T) {
	account, unchanged := fixture(t)
	home := filepath.Join(account, "dev", "agy-home")
	write(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), "mine")
	write(t, filepath.Join(home, prepareLockName), "")
	before := snapshot(t, home)
	swap := func() {
		if err := os.Rename(home, home+".old"); err != nil {
			t.Fatal(err)
		}
		mkdir(t, home)
	}
	if _, err := prepareWith(home, account, guardBinary, hooks{beforeWrite: swap}); err == nil || !strings.Contains(err.Error(), "changed after it was checked") {
		t.Fatalf("err = %v", err)
	}
	moved := map[string]string{}
	for path, content := range snapshot(t, home+".old") {
		moved[strings.Replace(path, home+".old", home, 1)] = content
	}
	if !equal(before, moved) {
		t.Fatalf("the checked HOME changed:\n%v\n%v", before, moved)
	}
	unchanged()
}

// An owned file swapped for a FIFO after writing makes prepare fail, not hang
// (Copilot on #1075).
func TestPrepareRefusesAFIFO(t *testing.T) {
	account, _ := fixture(t)
	home := filepath.Join(account, "dev", "agy-home")
	mkdir(t, home)
	settings := filepath.Join(home, ".gemini/antigravity-cli/settings.json")
	swap := func() {
		if err := os.Remove(settings); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(settings, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := prepareWith(home, account, guardBinary, hooks{afterWrite: swap})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("prepare hung on a FIFO")
	}
}

// A shared configuration file that is a symlink to an owned file in the
// dedicated HOME would read the launcher's content after the rename; one to a
// not-yet-created owned file likewise. Both are refused before any write,
// with the shared side and the dedicated HOME left as they were.
func TestPrepareRefusesASharedFileAliasingAnOwnedFile(t *testing.T) {
	for _, f := range []struct{ dir, name string }{
		{"antigravity-cli", "settings.json"},
		{"config", "hooks.json"},
		{"config", "mcp_config.json"},
		{"config/agents", "argon-reviewer.md"},
	} {
		for _, dangling := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s dangling=%v", f.dir, f.name, dangling), func(t *testing.T) {
				account := resolved(t, t.TempDir())
				home := filepath.Join(account, "dev", "agy-home")
				mkdir(t, filepath.Join(home, ".gemini/antigravity-cli"))
				mkdir(t, filepath.Join(home, ".gemini/config"))
				mkdir(t, filepath.Join(home, ".gemini", f.dir))
				target := filepath.Join(home, ".gemini", f.dir, f.name)
				if !dangling {
					write(t, target, "mine")
				}
				symlink(t, target, filepath.Join(account, ".gemini", f.dir, f.name))
				write(t, filepath.Join(home, prepareLockName), "")
				before := snapshot(t, home)
				if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "shared") {
					t.Fatalf("err = %v", err)
				}
				if !equal(before, snapshot(t, home)) {
					t.Fatalf("the dedicated HOME changed:\n%v\n%v", before, snapshot(t, home))
				}
				if data, err := os.ReadFile(filepath.Join(account, ".gemini", f.dir, f.name)); dangling && err == nil || !dangling && string(data) != "mine" {
					t.Fatalf("the shared %s now reads %q, %v", f.name, data, err)
				}
			})
		}
	}
}

// With an empty dedicated HOME, a dangling shared link to an owned file is
// refused before any directory is created (Codex on #1075).
func TestPrepareRefusesASharedAliasBeforeCreatingDirectories(t *testing.T) {
	account := resolved(t, t.TempDir())
	home := filepath.Join(account, "dev", "agy-home")
	mkdir(t, home)
	write(t, filepath.Join(home, prepareLockName), "")
	before := snapshot(t, home)
	symlink(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), filepath.Join(account, ".gemini/antigravity-cli/settings.json"))
	if _, err := prepare(home, account, guardBinary); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("err = %v", err)
	}
	if !equal(before, snapshot(t, home)) {
		t.Fatal("the dedicated HOME changed")
	}
}

// A shared file that is a symlink elsewhere, such as into a dotfiles
// repository, is the account's own business: prepare accepts it and leaves it
// alone. One made into an owned file after the writes is refused by the final
// check.
func TestPrepareSharedFileSymlinks(t *testing.T) {
	account := resolved(t, t.TempDir())
	dotfiles := filepath.Join(account, "dotfiles", "settings.json")
	write(t, dotfiles, "dotfiles")
	shared := filepath.Join(account, ".gemini/antigravity-cli/settings.json")
	symlink(t, dotfiles, shared)
	home := filepath.Join(account, "dev", "agy-home")
	mkdir(t, home)
	if _, err := prepare(home, account, guardBinary); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(shared); string(data) != "dotfiles" {
		t.Fatalf("the shared settings read %q", data)
	}
	alias := func() {
		if err := os.Remove(shared); err != nil {
			t.Fatal(err)
		}
		symlink(t, filepath.Join(home, ".gemini/antigravity-cli/settings.json"), shared)
	}
	if _, err := prepareWith(home, account, guardBinary, hooks{afterWrite: alias}); err == nil || !strings.Contains(err.Error(), "into the dedicated HOME") {
		t.Fatalf("err = %v", err)
	}
}

// checkOwned refuses any content but the launcher's own.
func TestCheckOwned(t *testing.T) {
	for _, tc := range []struct{ name, dir, file, content string }{
		{"an allow rule", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, `"deny": [`, `"allow": ["command(git)"], "deny": [`, 1)},
		{"a read_file grant", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, `"deny": [`, `"allow": ["read_file(*)"], "deny": [`, 1)},
		{"an empty allow list", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, `"deny": [`, `"allow": [], "deny": [`, 1)},
		{"a trusted workspace", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, "{\n", "{\n  \"trustedWorkspaces\": [\"/\"],\n", 1)},
		{"a wrong deny list", "antigravity-cli", "settings.json", strings.Replace(settingsJSON, `      "mcp(*)"`, `      "unsandboxed(*)"`, 1)},
		{"another hook", "config", "hooks.json", `{"other":{"PreToolUse":[]}}`},
		{"an MCP server", "config", "mcp_config.json", `{"mcpServers":{"x":{"command":"sh"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer closeFD(fd)
			write(t, filepath.Join(dir, tc.file), tc.content)
			for _, f := range ownedFiles(t, guardBinary) {
				if f.dir == tc.dir && f.name == tc.file {
					if err := checkOwned(fd, tc.file, f.content); err == nil {
						t.Fatal("accepted")
					}
					return
				}
			}
			t.Fatal("not an owned file")
		})
	}
}

// prepare runs only after the trusted-build check: an unstamped test binary
// is refused before anything in HOME is created.
func TestPrepareChecksTheBuildFirst(t *testing.T) {
	// Resolved, so that prepare would succeed and write if it ran first.
	home := resolved(t, t.TempDir())
	t.Setenv("RIBBITTO_ARGON_HOME", home)
	if code := run([]string{"prepare"}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("HOME holds %v, %v", entries, err)
	}
}

func equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
