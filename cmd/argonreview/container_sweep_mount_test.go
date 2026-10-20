package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

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
		{"many-prefixes", "/run/desktop/mnt/host/private/tmp", true},
		{"run-host-private", "/run/host_mnt/private/tmp", true},
		{"run-host-tmp", "/run/host_mnt/tmp", true},
		{"desktop-tmp", "/run/desktop/mnt/host/tmp", true},
		{"private-ancestor", "/private", true},
		{"private-tmp-ancestor", "/private/tmp", true},
		{"tmp", "/tmp", true},
		{"root", "/", true},
		{"var-tmp", "/var/tmp", false},
		{"home-tmp", "/home/me/tmp", false},
		{"project-tmp", "/home/me/proj/tmp", false},
		{"unrelated-private", "/something/private", false},
		{"unrelated-private-tmp", "/something/private/tmp", false},
		{"unknown-prefix", "/desktop/host/mnt/private/tmp", false},
		{"prefix-lookalike", "/host_mnt_extra/private/tmp", false},
		{"unknown-copy-name", "/unknown/prefix/tmp/argonreview-container-123/tree", true},
		{"cleaned-prefix", "/host_mnt//private//tmp/.", true},
		{"middle-name", "/elsewhere/argonreview-container-123/different", true},
		{"unrelated", "/elsewhere/other", false},
		{"volume", "/var/lib/docker/volumes/id/_data", false},
		{"sibling", copyDir + "4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, dir := range []string{copyDir, strings.TrimPrefix(copyDir, "/private")} {
				if got := sweepPathsOverlap(dir, tc.source); got != tc.want {
					t.Fatalf("copy %q source %q protects=%v, want %v", dir, tc.source, got, tc.want)
				}
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

func TestSweepMountResolutionPermission(t *testing.T) {
	for _, kind := range []string{"bind", "volume", "tmpfs", "other"} {
		for _, overlap := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{false: "elsewhere", true: "copy-name"}[overlap], func(t *testing.T) {
				s, out, fixture := sweepFixture(t)
				dir := sweepCopyFixture(t, s)
				c := sweepContainerFixture(t, s, fixture)
				c.Name = "/foreign"
				source := "/var/lib/docker/volumes/id/_data"
				if overlap {
					source = "/vm/prefix/" + filepath.Base(dir) + "/tree"
				}
				c.Mounts = []sweepMount{{kind, source, "/work"}}
				saveSweepContainer(t, fixture, c, ".json")
				calls := 0
				s.resolveMount = func(path string) (string, error) {
					calls++
					if path != source {
						t.Fatalf("unexpected resolution %q", path)
					}
					return "", &os.PathError{Op: "lstat", Path: path, Err: unix.EACCES}
				}
				err := s.run()
				if calls != 1 || (err != nil) != (kind == "bind") {
					t.Fatalf("calls=%d err=%v report=%s", calls, err, out)
				}
				sweepExists(t, dir, kind == "bind" || overlap)
				if kind == "bind" && !strings.Contains(out.String(), "unreclaimed copy "+dir) {
					t.Fatalf("report %q", out)
				}
				if kind != "bind" && overlap && !strings.Contains(out.String(), "skipped (in use) copy "+dir) {
					t.Fatalf("report %q", out)
				}
				assertNoSweepRM(t, fixture)
			})
		}
	}
}
