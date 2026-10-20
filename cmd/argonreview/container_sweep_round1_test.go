package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSweepCopyLarge(t *testing.T) {
	for _, shuffled := range []bool{false, true} {
		t.Run(fmt.Sprint(shuffled), func(t *testing.T) {
			s, _, _ := sweepFixture(t)
			dir := sweepCopyFixture(t, s)
			for _, count := range []int{350, 140, 135} {
				for i := 0; i < count; i++ {
					write(t, filepath.Join(dir, "tree", fmt.Sprint(count), fmt.Sprint(i)), "copy")
				}
			}
			outside := resolved(t, t.TempDir())
			write(t, filepath.Join(outside, "safe"), "outside")
			symlink(t, outside, filepath.Join(dir, "tree", "dir-link"))
			symlink(t, filepath.Join(outside, "safe"), filepath.Join(dir, "tree", "file-link"))
			if shuffled {
				// Model a directory cursor losing its remaining entries after deletion.
				// A complete read before unlinking, using a fresh cursor per pass, is safe.
				seen := map[*os.File]bool{}
				s.readDir = func(f *os.File, n int) ([]os.DirEntry, error) {
					if n > 0 && seen[f] {
						return nil, io.EOF
					}
					seen[f] = true
					return f.ReadDir(n)
				}
			}
			ageSweepCopy(t, s, dir)
			if err := s.run(); err != nil {
				t.Fatal(err)
			}
			sweepExists(t, dir, false)
			sweepExists(t, filepath.Join(outside, "safe"), true)
		})
	}
}

func TestSweepCopyPasses(t *testing.T) {
	for _, endless := range []bool{false, true} {
		t.Run(fmt.Sprint(endless), func(t *testing.T) {
			s, out, _ := sweepFixture(t)
			dir := sweepCopyFixture(t, s)
			reads := 0
			s.readDir = func(f *os.File, n int) ([]os.DirEntry, error) {
				entries, err := f.ReadDir(n)
				if filepath.Base(f.Name()) == filepath.Base(dir) && (endless || reads == 0) {
					// A new entry after the full read requires another full pass.
					write(t, filepath.Join(dir, fmt.Sprintf("late-%d", reads)), "late")
					reads++
				}
				return entries, err
			}
			err := s.run()
			if (err != nil) != endless {
				t.Fatalf("endless=%v: %v; %s", endless, err, out)
			}
			sweepExists(t, dir, endless)
			if endless && (reads != 8 || !strings.Contains(out.String(), "unreclaimed copy "+dir)) {
				t.Fatalf("unbounded removal: reads=%d report=%q", reads, out)
			}
		})
	}
}

func TestSweepMountForms(t *testing.T) {
	const copyDir = "/private/tmp/argonreview-container-123"
	for _, tc := range []struct {
		name, source string
		want         bool
	}{
		{"desktop-root", "/host_mnt/private/tmp/argonreview-container-123", true},
		{"desktop-tree", "/host_mnt/private/tmp/argonreview-container-123/tree", true},
		{"real", copyDir, true},
		{"tmp-sub", "/tmp/argonreview-container-123/sub", true},
		{"desktop-ancestor", "/host_mnt/private/tmp", true},
		{"tmp-ancestor", "/host_mnt/tmp", true},
		{"many-prefixes", "/desktop/host/mnt/private/tmp", true},
		{"cleaned-prefix", "/host_mnt//private//tmp/.", true},
		{"middle-name", "/elsewhere/argonreview-container-123/different", true},
		{"unrelated", "/elsewhere/other", false},
		{"volume", "/var/lib/docker/volumes/id/_data", false},
		{"sibling", copyDir + "4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sweepPathsOverlap(copyDir, tc.source); got != tc.want {
				t.Fatalf("source %q protects=%v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

func TestSweepMountInvalid(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"relative", "host_mnt/tmp/copy"}, {"parent", "/absent/../tmp"},
		{"nul", "/tmp/a\x00b"}, {"empty-bind", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, out, fixture := sweepFixture(t)
			dir := sweepCopyFixture(t, s)
			c := sweepContainerFixture(t, s, fixture)
			c.Name = "/foreign"
			c.Mounts = []sweepMount{{"bind", tc.source, "/work"}}
			saveSweepContainer(t, fixture, c, ".json")
			if err := s.run(); err == nil {
				t.Fatal("uninterpretable mount accepted")
			}
			sweepExists(t, filepath.Join(dir, "tree", "file"), true)
			if !strings.Contains(out.String(), "unreclaimed copy "+dir) {
				t.Fatalf("report %q", out)
			}
			assertNoSweepRM(t, fixture)
		})
	}
}

func TestSweepMountPrefixProtection(t *testing.T) {
	s, out, fixture := sweepFixture(t)
	dir := sweepCopyFixture(t, s)
	c := sweepContainerFixture(t, s, fixture)
	c.Name = "/foreign"
	c.Mounts = []sweepMount{{"bind", "/host_mnt" + filepath.Join(dir, "tree"), "/work"}}
	saveSweepContainer(t, fixture, c, ".json")
	if err := s.run(); err != nil {
		t.Fatal(err)
	}
	sweepExists(t, dir, true)
	if !strings.Contains(out.String(), "skipped (in use) copy "+dir) {
		t.Fatalf("report %q", out)
	}
}

func TestSweepConcurrentContainerRemoval(t *testing.T) {
	s, out, fixture := sweepFixture(t)
	c := sweepContainerFixture(t, s, fixture)
	write(t, filepath.Join(fixture, "vanish-rm"), "")
	if err := s.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reclaimed container "+c.ID) {
		t.Fatalf("report %q", out)
	}
}

func TestSweepConcurrentCopyRemoval(t *testing.T) {
	for _, stage := range []string{"open-copy", "entry-stat", "entry-open", "entry-recheck", "entry-unlink", "root-recheck", "root-unlink", "false-enoent", "false-stat-enoent", "false-root-enoent", "false-root-stat"} {
		t.Run(stage, func(t *testing.T) {
			s, out, _ := sweepFixture(t)
			dir := sweepCopyFixture(t, s)
			calls := map[string]int{}
			fired := false
			s.statAt = func(fd int, name string, st *unix.Stat_t, flags int) error {
				err := unix.Fstatat(fd, name, st, flags)
				calls[name]++
				remove := stage == "open-copy" && name == filepath.Base(dir) && calls[name] == 1 ||
					stage == "entry-stat" && name == "file" && calls[name] == 1 ||
					stage == "entry-open" && name == "tree" && calls[name] == 1 ||
					stage == "entry-recheck" && name == "file" && calls[name] == 2 ||
					stage == "root-recheck" && name == filepath.Base(dir) && calls[name] == 2
				if !fired && remove {
					path := filepath.Join(dir, "tree", "file")
					if name == "tree" {
						path = filepath.Join(dir, "tree")
					}
					if name == filepath.Base(dir) {
						path = dir
					}
					if err := os.RemoveAll(path); err != nil {
						t.Fatal(err)
					}
					fired = true
					if stage == "entry-open" {
						return err
					}
					return unix.ENOENT
				}
				if stage == "false-stat-enoent" && name == "file" && calls[name] == 1 || stage == "false-root-stat" && name == filepath.Base(dir) && calls[name] == 2 {
					fired = true
					return unix.ENOENT
				}
				return err
			}
			s.unlinkAt = func(fd int, name string, flags int) error {
				if !fired && (stage == "entry-unlink" && name == "file" || stage == "root-unlink" && name == filepath.Base(dir) || stage == "false-enoent" && name == "file" || stage == "false-root-enoent" && name == filepath.Base(dir)) {
					fired = true
					if stage != "false-enoent" && stage != "false-root-enoent" {
						if err := unix.Unlinkat(fd, name, flags); err != nil {
							t.Fatal(err)
						}
					}
					return unix.ENOENT
				}
				return unix.Unlinkat(fd, name, flags)
			}
			err := s.run()
			unsafe := strings.HasPrefix(stage, "false-")
			if !fired || (err != nil) != unsafe {
				t.Fatalf("fired=%v unsafe=%v: %v; %s", fired, unsafe, err, out)
			}
			sweepExists(t, dir, unsafe)
			if unsafe && !strings.Contains(out.String(), "unreclaimed copy "+dir) {
				t.Fatalf("report %q", out)
			}
		})
	}
}

func TestSweepCopyRootDevice(t *testing.T) {
	s, out, _ := sweepFixture(t)
	dir := sweepCopyFixture(t, s)
	calls := 0
	s.sameDevice = func(uint64, uint64) bool { calls++; return calls != 1 }
	if err := s.run(); err == nil {
		t.Fatal("mounted copy root accepted")
	}
	sweepExists(t, filepath.Join(dir, "tree", "file"), true)
	if out.String() != "unreclaimed copy "+dir+"\n" {
		t.Fatalf("report %q", out)
	}
}

func TestSweepCopySubtreeDevice(t *testing.T) {
	s, _, _ := sweepFixture(t)
	dir := sweepCopyFixture(t, s)
	calls := 0
	s.sameDevice = func(uint64, uint64) bool { calls++; return calls == 1 }
	if err := s.run(); err == nil {
		t.Fatal("subtree device boundary crossed")
	}
	sweepExists(t, filepath.Join(dir, "tree", "file"), true)
}

func TestSweepCopyAbsentVerificationError(t *testing.T) {
	s, _, _ := sweepFixture(t)
	dir := sweepCopyFixture(t, s)
	unlinked := false
	s.unlinkAt = func(fd int, name string, flags int) error {
		if err := unix.Unlinkat(fd, name, flags); err != nil {
			t.Fatal(err)
		}
		unlinked = true
		return unix.ENOENT
	}
	s.statAt = func(fd int, name string, st *unix.Stat_t, flags int) error {
		if unlinked && name == "file" {
			return errors.New("cannot establish absence")
		}
		return unix.Fstatat(fd, name, st, flags)
	}
	if err := s.run(); err == nil {
		t.Fatal("unknown absence accepted")
	}
	sweepExists(t, dir, true)
}
