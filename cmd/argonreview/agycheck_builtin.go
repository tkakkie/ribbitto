package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func agyCheckBuiltinListing(home string, f agyCheckFiles) ([]byte, error) {
	root := filepath.Join(home, ".gemini/antigravity-cli/builtin")
	if err := f.path(home, root); err != nil {
		return nil, err
	}
	info, err := f.lstat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("builtin root must be a real directory")
	}
	var lines []string
	count := 0
	var visit func(string) error
	visit = func(dir string) error {
		entries, err := f.entries(dir)
		if err != nil {
			return errors.New("listing builtin tree")
		}
		count += len(entries)
		if count > agyCheckHookCallCap {
			return errors.New("builtin entry limit exceeded")
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if err := f.path(home, path); err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || !agyCheckTranscriptPath(rel) || strings.Contains(rel, "\\") {
				return errors.New("invalid builtin entry name")
			}
			info, err := f.lstat(path)
			if err != nil {
				return errors.New("examining builtin entry")
			}
			switch {
			case info.IsDir():
				if err := visit(path); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				data, err := f.readInside(home, path, 1<<20)
				if err != nil || len(data) > 1<<20 {
					return errors.New("builtin file exceeds cap or is unreadable")
				}
				lines = append(lines, fmt.Sprintf("%x  ./%s\n", sha256.Sum256(data), rel))
			default:
				return errors.New("unknown builtin entry type")
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	// Sort by relative path, as implement-check compares its actual map. Hashes
	// must not change order. Reparse to reject duplicate or ambiguous entries.
	sort.Slice(lines, func(i, j int) bool { return lines[i][68:] < lines[j][68:] })
	listing := []byte(strings.Join(lines, ""))
	if len(listing) > agyCheckBuiltinMaxBytes {
		return nil, errors.New("builtin listing exceeds cap")
	}
	if _, err := implementBuiltinList(listing); err != nil {
		return nil, errors.New("invalid builtin listing")
	}
	return listing, nil
}
