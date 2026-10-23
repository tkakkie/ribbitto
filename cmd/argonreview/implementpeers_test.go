package main

import (
	"bytes"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func implementPeerCheckArgs(args []string) []string {
	var check []string
	for i := 0; i < len(args); i += 2 {
		switch args[i] {
		case "--prompt-file", "--log-dir", "--timeout", "--model":
		default:
			check = append(check, args[i:i+2]...)
		}
	}
	return check
}

func TestImplementPeerContainment(t *testing.T) {
	for _, row := range []struct{ name, flag string }{
		{"home", "home"},             // Delete the --home-in-peer check.
		{"worktree", "worktree"},     // Delete the --worktree-in-peer check.
		{"logs", "log-dir"},          // Delete the log-parent-in-peer check.
		{"git", "git-file"},          // Delete the --git-file-in-peer check.
		{"prompt", "prompt-file"},    // Delete the --prompt-file-in-peer check.
		{"agy", "agy"},               // Delete the --agy-in-peer check.
		{"peer-in-tree", "worktree"}, // Delete the peer-in-worktree check.
		{"peer-in-home", "home"},     // Delete the peer-in-home check.
	} {
		for _, equal := range []bool{false, true} {
			if equal && (row.name == "home" || strings.HasPrefix(row.name, "peer-in-")) {
				continue // Home equality is a usage error, covered separately.
			}
			t.Run(row.name+map[bool]string{false: "/inside", true: "/equal"}[equal], func(t *testing.T) {
				x := newImplementRun(t)
				peer := filepath.Join(x.f.root, "peer")
				implementCheckMkdir(t, peer)
				if strings.HasPrefix(row.name, "peer-in-") {
					root := x.f.tree
					if row.flag == "home" {
						root = x.f.home
					}
					peer = filepath.Join(root, "peer")
					implementCheckMkdir(t, peer)
				} else {
					path := peer
					if !equal {
						path = filepath.Join(peer, "child")
					}
					switch row.flag {
					case "home", "worktree":
						implementCheckMkdir(t, path)
					case "log-dir":
						implementCheckMkdir(t, path)
						path = filepath.Join(path, "logs")
					default:
						if !equal {
							write(t, path, "input")
						}
					}
					x.set(row.flag, path)
				}
				x.args = append(x.args, "--peer-home", peer)
				before := implementRunSnapshot(t, x.f.root)
				var out, stderr bytes.Buffer
				if code := implementRunCommand(x.args, &out, &stderr, x.deps); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "peer home") {
					t.Fatalf("run peer refusal: %d %s %s", code, &out, &stderr)
				}
				if row.flag != "log-dir" && row.flag != "prompt-file" {
					out.Reset()
					stderr.Reset()
					if code := implementCheckCommand(implementPeerCheckArgs(x.args), &out, &stderr, x.f.deps); code != 1 || !strings.Contains(out.String(), "peer home") {
						t.Fatalf("check peer refusal: %d %s %s", code, &out, &stderr)
					}
				}
				if !reflect.DeepEqual(before, implementRunSnapshot(t, x.f.root)) {
					t.Fatal("peer refusal wrote files")
				}
			})
		}
	}
}

func TestImplementPeerArguments(t *testing.T) {
	for _, row := range []struct {
		name string
		want int
	}{
		{"repeat", 0},       // Make --peer-home a once-only flag.
		{"relative", 2},     // Delete the absolute-path rule.
		{"unclean", 2},      // Delete the clean-path rule.
		{"root", 2},         // Delete the root refusal.
		{"file", 2},         // Delete the directory rule.
		{"symlink", 2},      // Follow a symlink leaf.
		{"component", 2},    // Follow a symlink component.
		{"same-home", 2},    // Delete home equality refusal.
		{"four", 2},         // Delete the three-peer cap.
		{"other-repeat", 2}, // Permit other flags to repeat.
	} {
		t.Run(row.name, func(t *testing.T) {
			x := newImplementRun(t)
			peer := filepath.Join(x.f.root, "peer")
			implementCheckMkdir(t, peer)
			peers := []string{peer}
			switch row.name {
			case "repeat", "four":
				count := 3
				if row.name == "four" {
					count = 4
				}
				for len(peers) < count {
					next := peer + strings.Repeat("x", len(peers))
					implementCheckMkdir(t, next)
					peers = append(peers, next)
				}
			case "relative":
				peers[0] = "relative"
			case "unclean":
				peers[0] = peer + "/../peer"
			case "root":
				peers[0] = "/"
			case "file":
				peers[0] = x.f.saved
			case "symlink", "component":
				link := filepath.Join(x.f.root, "alias")
				symlink(t, peer, link)
				peers[0] = link
				if row.name == "component" {
					implementCheckMkdir(t, filepath.Join(peer, "child"))
					peers[0] = filepath.Join(link, "child")
				}
			case "same-home":
				peers[0] = x.f.home
			case "other-repeat":
				x.args = append(x.args, "--home", x.f.home)
			}
			for _, peer := range peers {
				x.args = append(x.args, "--peer-home", peer)
			}
			for _, check := range []bool{true, false} {
				var out, stderr bytes.Buffer
				code := 0
				if check {
					code = implementCheckCommand(implementPeerCheckArgs(x.args), &out, &stderr, x.f.deps)
				} else {
					code = implementRunCommand(x.args, &out, &stderr, x.deps)
				}
				if code != row.want {
					t.Fatalf("check=%t: exit=%d want=%d out=%s err=%s", check, code, row.want, &out, &stderr)
				}
			}
		})
	}
}

func TestImplementPeerForwarding(t *testing.T) {
	x := newImplementRun(t)
	peer := filepath.Join(x.f.root, "peer")
	implementCheckMkdir(t, peer)
	x.args = append(x.args, "--peer-home", peer)
	check := x.deps.check
	x.deps.check = func(args []string, out, stderr io.Writer) int {
		// Delete peer forwarding from checkState: every phase must retain policy.
		if !strings.Contains(strings.Join(args, "\n"), "--peer-home\n"+peer) {
			t.Error("in-process check lost peers")
		}
		return check(args, out, stderr)
	}
	x.run(t, 0)
}

func TestImplementPeerNoPeersUnchanged(t *testing.T) {
	x := newImplementRun(t)
	// Allocating an empty peer set must preserve the prior nil-set behavior.
	var previous string
	for i, peers := range []implementPeerHomes{nil, {}} {
		r, err := implementRunArguments(x.args, io.Discard, x.deps)
		if err != nil {
			t.Fatal(err)
		}
		r.peers = peers
		var out bytes.Buffer
		code := r.checkState("off", &out)
		got := out.String()
		if code != 0 || i > 0 && got != previous {
			t.Fatalf("empty peers changed output/exit: %d %q vs %q", code, got, previous)
		}
		previous = got
	}
}

func TestImplementPeerDisjointUnchanged(t *testing.T) {
	for _, check := range []bool{true, false} {
		t.Run(map[bool]string{true: "check", false: "run"}[check], func(t *testing.T) {
			x := newImplementRun(t)
			peer := filepath.Join(x.f.root, "peer")
			implementCheckMkdir(t, peer)
			var previous string
			for i := range 2 {
				var out, stderr bytes.Buffer
				code := 0
				if check {
					code = implementCheckCommand(implementPeerCheckArgs(x.args), &out, &stderr, x.f.deps)
				} else {
					code = implementRunCommand(x.args, &out, &stderr, x.deps)
				}
				// Add output or a refusal for empty/disjoint peer sets.
				got := out.String() + stderr.String()
				if code != 0 || i > 0 && got != previous {
					t.Fatalf("peer option changed output/exit: %d %q vs %q", code, got, previous)
				}
				previous = got
				implementCheckRemove(t, x.logs)
				implementCheckRemove(t, filepath.Join(x.fake, "args"))
				x.order, x.checks = nil, 0
				x.args = append(x.args, "--peer-home", peer)
			}
		})
	}
}
