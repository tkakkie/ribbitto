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
			if endless && (reads != 9 || !strings.Contains(out.String(), "unreclaimed copy "+dir)) {
				t.Fatalf("unbounded removal: reads=%d report=%q", reads, out)
			}
		})
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

func TestSweepCopyFinalPass(t *testing.T) {
	for _, remain := range []bool{false, true} {
		t.Run(fmt.Sprint(remain), func(t *testing.T) {
			s, out, _ := sweepFixture(t)
			dir := sweepCopyFixture(t, s)
			// One visible entry per pass: the eighth deletion must get a final listing.
			if err := os.RemoveAll(filepath.Join(dir, "tree")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "entry"), "copy")
			reads, deletes := 0, 0
			s.readDir = func(f *os.File, n int) ([]os.DirEntry, error) {
				if filepath.Base(f.Name()) == filepath.Base(dir) {
					reads++
				}
				return f.ReadDir(n)
			}
			s.unlinkAt = func(fd int, name string, flags int) error {
				err := unix.Unlinkat(fd, name, flags)
				if err == nil && name == "entry" {
					deletes++
					if deletes < 8 || remain {
						write(t, filepath.Join(dir, "entry"), "late")
					}
				}
				return err
			}
			ageSweepCopy(t, s, dir)
			err := s.run()
			if (err != nil) != remain || reads != 9 || deletes != 8 {
				t.Fatalf("remain=%v reads=%d deletes=%d err=%v report=%s", remain, reads, deletes, err, out)
			}
			sweepExists(t, dir, remain)
			if remain && !strings.Contains(out.String(), "unreclaimed copy "+dir) {
				t.Fatalf("report %q", out)
			}
		})
	}
}

func TestSweepCopyDepthLimit(t *testing.T) {
	for _, depth := range []int{256, 257} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			s, out, _ := sweepFixture(t)
			dir := sweepCopyFixture(t, s)
			// Descriptor-relative creation avoids host path-length limits.
			fd, err := openSweepParent(dir)
			if err != nil {
				t.Fatal(err)
			}
			for range depth {
				if err := unix.Mkdirat(fd, "d", 0o700); err != nil {
					_ = unix.Close(fd)
					t.Fatal(err)
				}
				next, err := unix.Openat(fd, "d", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				_ = unix.Close(fd)
				if err != nil {
					t.Fatal(err)
				}
				fd = next
			}
			if err := unix.Close(fd); err != nil {
				t.Fatal(err)
			}
			ageSweepCopy(t, s, dir)
			err = s.run()
			limited := depth > 256
			if (err != nil) != limited {
				t.Fatalf("depth=%d err=%v report=%s", depth, err, out)
			}
			sweepExists(t, dir, limited)
			if limited && !strings.Contains(out.String(), "unreclaimed copy "+dir) {
				t.Fatalf("report %q", out)
			}
		})
	}
}
