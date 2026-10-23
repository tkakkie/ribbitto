package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestImplementPeerIdentity(t *testing.T) {
	for _, row := range []string{
		"same-home", // Compare home equality only by spelling.
		"path",      // Compare peer/path containment only by spelling.
		"ancestor",  // Stop comparing identities before reaching a parent.
		"nested",    // Compare root/peer containment only by spelling.
	} {
		t.Run(row, func(t *testing.T) {
			f := implementCheckFixture(t)
			peer := filepath.Join(f.root, "peer")
			implementCheckMkdir(t, peer)
			alias := f.home
			identity := peer
			if row == "nested" {
				alias, identity = peer, f.tree
			}
			info, err := os.Lstat(identity)
			if err != nil {
				t.Fatal(err)
			}
			// Synthetic aliases exercise device/inode matching on both
			// case-sensitive CI and case-insensitive host filesystems.
			lstat := func(path string) (fs.FileInfo, error) {
				if path == alias {
					return info, nil
				}
				return os.Lstat(path)
			}
			peers := implementPeerHomes{peer}
			if row == "same-home" {
				if err := peers.validate(f.home, lstat); err == nil {
					t.Fatal("same home identity accepted")
				}
				return
			}
			path := f.home
			if row == "ancestor" {
				path = filepath.Join(f.home, "child")
				write(t, path, "input")
			}
			if row == "nested" {
				path = f.saved
			}
			if err := peers.separate(f.home, f.tree, []string{path}, lstat); err == nil {
				t.Fatal("overlapping identity accepted")
			}
		})
	}
}
