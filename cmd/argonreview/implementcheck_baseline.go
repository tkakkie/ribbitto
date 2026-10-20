package main

import (
	"bytes"
	"crypto/sha1" // Git's SHA-1 object format requires this hash.
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type implementBaselineEntry struct{ mode, oid string }

func implementBlobOID(data []byte, format string) string {
	blob := append(fmt.Appendf(nil, "blob %d\x00", len(data)), data...)
	if format == "sha256" {
		return fmt.Sprintf("%x", sha256.Sum256(blob))
	}
	return fmt.Sprintf("%x", sha1.Sum(blob))
}

func implementBaselinePath(rel, base string) bool {
	if rel != base && !strings.HasPrefix(rel, base+"/") {
		return false
	}
	for _, component := range strings.Split(rel, "/") {
		if component == "" || component == "." || component == ".." || strings.EqualFold(component, ".git") || strings.Contains(component, "\\") {
			return false
		}
	}
	return true
}

// Parse plumbing output strictly: a failed or truncated listing is never absence.
func implementTreeEntry(out []byte, base string, hexLen int, index bool) (map[string]implementBaselineEntry, bool) {
	entries := make(map[string]implementBaselineEntry)
	for len(out) != 0 {
		record, rest, terminated := bytes.Cut(out, []byte{0})
		fields, rel, tab := strings.Cut(string(record), "\t")
		parts := strings.Split(fields, " ")
		if !terminated || !tab || len(parts) != 3 || !implementBaselinePath(rel, base) {
			return nil, false
		}
		mode, oid := parts[0], parts[2]
		if index {
			oid = parts[1]
		}
		if mode != "100644" && mode != "100755" || !implementMatch(fmt.Sprintf(`^[0-9a-f]{%d}$`, hexLen), oid) {
			return nil, false
		}
		if !index && parts[1] != "blob" || index && parts[2] != "0" {
			return nil, false
		}
		if _, duplicate := entries[rel]; duplicate {
			return nil, false
		}
		entries[rel] = implementBaselineEntry{mode, oid}
		out = rest
	}
	// A file cannot also be the parent of another file.
	for rel := range entries {
		for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
			if _, file := entries[parent]; file {
				return nil, false
			}
		}
	}
	return entries, true
}

func (c *implementChecker) baselineListing(base string, hexLen int, index bool, args ...string) (map[string]implementBaselineEntry, bool) {
	out, status, err := c.deps.git(args...)
	entries, valid := implementTreeEntry(out, base, hexLen, index)
	if err != nil || status != 0 || !valid {
		c.problem("cannot list baseline %q (%s): status %d, error %q, output %q", base, strings.Join(args[:2], " "), status, fmt.Sprint(err), out)
		return nil, false
	}
	return entries, true
}

func implementBaselineEqual(a, b map[string]implementBaselineEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for rel, entry := range a {
		if other, ok := b[rel]; !ok || entry != other {
			return false
		}
	}
	return true
}

func (c *implementChecker) baseline(worktree string) {
	out, status, err := c.deps.git("rev-parse", "--show-object-format")
	format, hexLen := strings.TrimSuffix(string(out), "\n"), 40
	if format == "sha256" {
		hexLen = 64
	}
	if err != nil || status != 0 || string(out) != format+"\n" || format != "sha1" && format != "sha256" {
		c.problem("object format failed: status %d, error %q, output %q", status, fmt.Sprint(err), out)
		return
	}
	out, status, err = c.deps.git("merge-base", "HEAD", "refs/remotes/origin/main")
	if err != nil || status != 0 || !implementMatch(fmt.Sprintf(`^[0-9a-f]{%d}\n$`, hexLen), string(out)) {
		if err == nil && status == 1 {
			c.problem("no merge base with origin/main")
		} else {
			c.problem("merge-base with origin/main failed: status %d, error %q, output %q", status, fmt.Sprint(err), out)
		}
		return
	}
	mb := strings.TrimSuffix(string(out), "\n")
	for _, base := range []string{".github", "AGENTS.md", "CLAUDE.md", ".gitattributes", ".gitmodules"} {
		reference, ok := c.baselineListing(base, hexLen, false, "ls-tree", "-r", "-z", mb, "--", base)
		if !ok {
			continue
		}
		// Check every reference blob, including unchanged files beneath .github.
		paths := make([]string, 0, len(reference))
		for rel := range reference {
			paths = append(paths, rel)
		}
		sort.Strings(paths)
		for _, rel := range paths {
			out, status, err := c.deps.git("cat-file", "-t", reference[rel].oid)
			if err != nil || status != 0 || string(out) != "blob\n" {
				c.problem("checking baseline object for %q: status %d, error %q, output %q", rel, status, fmt.Sprint(err), out)
			}
		}
		head, ok := c.baselineListing(base, hexLen, false, "ls-tree", "-r", "-z", "HEAD", "--", base)
		if ok && !implementBaselineEqual(reference, head) {
			c.problem("%q differs from origin/main in HEAD", base)
		}
		index, ok := c.baselineListing(base, hexLen, true, "ls-files", "-s", "-z", "--", base)
		if ok && !implementBaselineEqual(reference, index) {
			if len(reference) == 0 {
				c.problem("%q is in the index but origin/main does not track it", base)
			} else {
				c.problem("%q index differs from the merge base", base)
			}
		}
		out, status, err := c.deps.git("ls-files", "-v", "-z", "--", base)
		if err != nil || status != 0 {
			c.problem("listing baseline index %q: status %d, error %q", base, status, fmt.Sprint(err))
		} else {
			for len(out) != 0 {
				record, rest, ok := bytes.Cut(out, []byte{0})
				if !ok || len(record) < 3 || record[1] != ' ' || !implementBaselinePath(string(record[2:]), base) {
					c.problem("malformed baseline index record for %q: %q", base, record)
					break
				}
				if record[0] != 'H' {
					c.problem("baseline index %q has tag %q", base, record[:1])
				}
				out = rest
			}
		}
		c.baselineWorktree(worktree, base, format, reference)
	}
}

func (c *implementChecker) baselineWorktree(root, base, format string, reference map[string]implementBaselineEntry) {
	// Git's clean filters in local config/info/attributes cannot be disabled by
	// a flag. Direct reads also avoid the index stat cache and hidden-edit flags.
	entries, err := c.deps.readDir(root)
	if err != nil {
		c.problem("listing baseline root %q: %q", root, err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), base) && entry.Name() != base {
			c.problem("baseline spelling differs: %q, expected %q", entry.Name(), base)
		}
	}
	full := filepath.Join(root, base)
	if len(reference) == 0 {
		_, err := c.deps.lstat(full)
		if err == nil {
			c.problem("%q exists but origin/main does not track it", base)
		} else if !errors.Is(err, fs.ErrNotExist) {
			c.problem("checking absent baseline %q: %q", base, err)
		}
		return
	}
	directories := make(map[string]bool)
	for rel := range reference {
		for parent := path.Dir(rel); parent != "." && (parent == base || strings.HasPrefix(parent, base+"/")); parent = path.Dir(parent) {
			directories[parent] = true
		}
	}
	seen := make(map[string]bool)
	var visit func(string)
	visit = func(rel string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		info, err := c.deps.lstat(full)
		if err != nil {
			c.problem("checking baseline %q: %q", rel, err)
			return
		}
		want, file := reference[rel]
		if file && info.Mode().IsRegular() {
			data, mode, err := implementReadMode(full, 1<<20, false)
			if err != nil {
				c.problem("reading baseline %q: %q", rel, err)
				return
			}
			seen[rel] = true
			if implementBlobOID(data, format) != want.oid || (mode&0100 != 0) != (want.mode == "100755") {
				c.problem("%q differs from origin/main at the branch point", rel)
			}
			return
		}
		if !directories[rel] || !info.IsDir() || strings.EqualFold(path.Base(rel), ".git") {
			c.problem("unexpected baseline entry %q (type %s)", rel, info.Mode().Type())
			return
		}
		seen[rel] = true
		entries, err := c.deps.readDir(full)
		if err != nil {
			c.problem("listing baseline %q: %q", rel, err)
		}
		for _, entry := range entries {
			visit(rel + "/" + entry.Name())
		}
	}
	visit(base)
	for rel := range reference {
		if !seen[rel] {
			c.problem("missing baseline file %q", rel)
		}
	}
	for rel := range directories {
		if !seen[rel] {
			c.problem("missing baseline directory %q", rel)
		}
	}
}
