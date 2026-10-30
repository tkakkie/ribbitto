package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const agyCheckKeeping agyCheckStep = "keeping transcripts failed"

type agyCheckKeepFlag struct{ set bool }

func (f *agyCheckKeepFlag) String() string   { return "false" }
func (f *agyCheckKeepFlag) IsBoolFlag() bool { return true }
func (f *agyCheckKeepFlag) Set(value string) error {
	if f.set || value != "true" {
		return errors.New("keep-transcripts requires one affirmative flag")
	}
	f.set = true
	return nil
}

func agyCheckKeptText(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

func agyCheckKeepReady(root string, f agyCheckFiles) error {
	path := filepath.Join(root, "kept")
	if err := f.path(root, path); err != nil {
		return err
	}
	if _, err := f.lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := f.private(path, true); err != nil {
		return err
	}
	entries, err := f.entries(path)
	if err == nil && len(entries) != 0 {
		err = errors.New("kept directory is not empty")
	}
	return err
}

func agyCheckKeepRow(root, source, id string, f agyCheckFiles) error {
	for _, path := range []string{filepath.Join(root, "kept"), filepath.Join(root, "kept", id)} {
		if err := f.path(root, path); err != nil {
			return err
		}
		if err := f.private(filepath.Dir(path), true); err != nil {
			return err
		}
		if err := f.mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := f.private(path, true); err != nil {
			return err
		}
	}
	for _, file := range []struct {
		name string
		cap  int64
	}{{"out.jsonl", agyCheckTranscriptMaxBytes}, {"err.txt", 64 << 10}} {
		data, err := f.readPrefixInside(root, filepath.Join(source, file.name), file.cap)
		if err != nil {
			return err
		}
		path := filepath.Join(root, "kept", id, file.name)
		if err := f.write(root, path, data, true, 0o600); err != nil {
			return err
		}
		if err := f.private(path, false); err != nil {
			return err
		}
	}
	return nil
}

func agyCheckRetainer(o agyCheckOptions, root string, f agyCheckFiles, row agyCheckRow, observation *agyCheckObservation, results *agyCheckRowResults) func(agyCheckRowDirs, agyCheckRunResult) error {
	if !o.keepTranscripts {
		return nil
	}
	return func(r agyCheckRowDirs, run agyCheckRunResult) error {
		final := agyCheckRunObservation(row, *observation, run)
		if agyCheckKeepObservationOK(final, o.calls) {
			return nil
		}
		if err := agyCheckKeepRow(root, r.Row, row.ID, f); err != nil {
			return err
		}
		results.kept = append(results.kept, row.ID)
		return nil
	}
}

func agyCheckKeepObservationOK(o agyCheckObservation, calls int) bool {
	accountingOK := agyCheckClassify(o, calls).OK
	if o.ID == "reconciliation" {
		return accountingOK && agyCheckClassify(agyCheckParallelObservation(o), calls).OK
	}
	return accountingOK
}

func agyCheckFinishKeep(o agyCheckOptions, root string, f agyCheckFiles, kept []string, result agyCheckResult) agyCheckResult {
	if !o.keepTranscripts {
		return result
	}
	result.KeptRows = []string{}
	for _, id := range kept {
		// Discovery silence can become ok only after another row proves denial.
		// Both reconciliation observations must be ok to remove their shared copy.
		// Remove provisional copies without opening retained files.
		ok, found := true, false
		for _, row := range result.Rows {
			if row.ID == id || id == "reconciliation" && row.ID == "parallel-batches" {
				ok = ok && row.OK
				found = true
			}
		}
		if found && ok {
			if err := f.cleanup(root, filepath.Join(root, "kept", id)); err != nil {
				result.InvalidReason = string(agyCheckKeeping)
			}
		} else {
			result.KeptRows = append(result.KeptRows, id)
		}
	}
	if !agyCheckKeptValid(root, f, result.KeptRows) && result.InvalidReason == "" {
		result.InvalidReason = "kept transcripts directory changed after retention"
	}
	return result
}

// This is a metadata snapshot, not protection against later dedicated-user writes.
// Never reopen retained content, even when the tree has changed.
func agyCheckKeptValid(root string, f agyCheckFiles, ids []string) bool {
	kept := filepath.Join(root, "kept")
	if _, err := f.lstat(kept); err != nil {
		return errors.Is(err, os.ErrNotExist) && len(ids) == 0
	}
	if !agyCheckKeptMetadata(f, kept, true, 0) {
		return false
	}
	entries, err := f.entries(kept)
	if err != nil || len(entries) != len(ids) {
		return false
	}
	for _, entry := range entries {
		found := false
		for _, id := range ids {
			if entry.Name() == id {
				found = true
			}
		}
		if !found {
			return false
		}
		row := filepath.Join(kept, entry.Name())
		if !agyCheckKeptMetadata(f, row, true, 0) {
			return false
		}
		files, err := f.entries(row)
		if err != nil || len(files) != 2 {
			return false
		}
		for _, file := range files {
			cap := int64(64 << 10)
			switch file.Name() {
			case "out.jsonl":
				cap = agyCheckTranscriptMaxBytes
			case "err.txt":
			default:
				return false
			}
			if !agyCheckKeptMetadata(f, filepath.Join(row, file.Name()), false, cap) {
				return false
			}
		}
	}
	return true
}

func agyCheckKeptMetadata(f agyCheckFiles, path string, directory bool, cap int64) bool {
	info, err := f.lstat(path)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || int(stat.Uid) != f.uid {
		return false
	}
	want := os.FileMode(0o600)
	if directory {
		want = os.ModeDir | 0o700
	}
	if info.Mode() != want {
		return false
	}
	return directory || stat.Nlink == 1 && info.Size() <= cap
}
