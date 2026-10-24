package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestImplementPeerOverlap(t *testing.T) {
	for _, row := range []struct {
		name string
		want int
	}{
		{"equal", 1},          // Delete both direction predicates; either redundantly catches equality.
		{"inside", 1},         // Delete the second-inside-first predicate alone.
		{"parent", 1},         // Delete the first-inside-second predicate alone.
		{"disjoint", 0},       // Refuse any two peers.
		{"three-disjoint", 0}, // Refuse any two peers, including a three-peer set.
	} {
		t.Run(row.name, func(t *testing.T) {
			x := newImplementRun(t)
			a := filepath.Join(x.f.root, "peer")
			b := filepath.Join(x.f.root, "peer-sibling")
			switch row.name {
			case "equal":
				b = a
			case "inside":
				b = filepath.Join(a, "child")
			case "parent":
				b, a = a, filepath.Join(a, "child")
			}
			peers := implementPeerHomes{a, b}
			if row.name == "three-disjoint" {
				peers = append(peers, filepath.Join(x.f.root, "peer-third"))
			}
			for _, peer := range peers {
				implementCheckMkdir(t, peer)
				x.args = append(x.args, "--peer-home", peer)
			}
			before := implementRunSnapshot(t, x.f.root)
			if row.want == 0 {
				paths := []string{x.f.home, x.f.tree, filepath.Dir(x.logs), x.f.saved, filepath.Join(x.f.root, "prompt"), filepath.Join(x.fake, "agy")}
				if err := peers.separate(x.f.home, x.f.tree, paths, os.Lstat); err != nil {
					t.Fatalf("disjoint peers refused: %v", err)
				}
			} else {
				var out, stderr bytes.Buffer
				if code := implementRunCommand(x.args, &out, &stderr, x.deps); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "peer home") {
					t.Fatalf("run peer refusal: %d %s %s", code, &out, &stderr)
				}
				out.Reset()
				stderr.Reset()
				if code := implementCheckCommand(implementPeerCheckArgs(x.args), &out, &stderr, x.f.deps); code != 1 || !strings.Contains(out.String(), "peer home") {
					t.Fatalf("check peer refusal: %d %s %s", code, &out, &stderr)
				}
			}
			if !reflect.DeepEqual(before, implementRunSnapshot(t, x.f.root)) {
				t.Fatal("peer separation wrote files")
			}
		})
	}
}

func TestImplementPeerOverlapErrors(t *testing.T) {
	for _, row := range []string{
		"second-inside-first", // Treat the first containment error as disjoint.
		"first-inside-second", // Treat the reverse containment error as disjoint.
	} {
		t.Run(row, func(t *testing.T) {
			f := implementCheckFixture(t)
			a, b := filepath.Join(f.root, "peer-a"), filepath.Join(f.root, "peer-b")
			implementCheckMkdir(t, a)
			implementCheckMkdir(t, b)
			calls := 0
			lstat := func(path string) (fs.FileInfo, error) {
				if path == a {
					calls++
				}
				// Fail only the selected walk so later checks cannot mask a
				// swallowed error; a's second stat selects the reverse walk.
				if path == a && (row == "second-inside-first" && calls == 1 || row == "first-inside-second" && calls == 2) {
					return nil, fs.ErrPermission
				}
				return os.Lstat(path)
			}
			if err := (implementPeerHomes{a, b}).separate(f.home, f.tree, nil, lstat); !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "checking peer containment") {
				t.Fatalf("containment error not wrapped: %v", err)
			}
		})
	}
}
