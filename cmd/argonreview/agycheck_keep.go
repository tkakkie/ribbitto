package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
		if agyCheckClassify(final, o.calls).OK {
			return nil
		}
		if err := agyCheckKeepRow(root, r.Row, row.ID, f); err != nil {
			return err
		}
		results.kept = append(results.kept, row.ID)
		return nil
	}
}

func agyCheckFinishKeep(o agyCheckOptions, root string, f agyCheckFiles, kept []string, result agyCheckResult) agyCheckResult {
	if !o.keepTranscripts {
		return result
	}
	result.KeptRows = []string{}
	for _, id := range kept {
		// Discovery silence can become ok only after another row proves denial.
		// Remove that provisional copy without opening either retained file.
		ok := false
		for _, row := range result.Rows {
			if row.ID == id {
				ok = row.OK
			}
		}
		if ok {
			if err := f.cleanup(root, filepath.Join(root, "kept", id)); err != nil {
				result.InvalidReason = string(agyCheckKeeping)
			}
		} else {
			result.KeptRows = append(result.KeptRows, id)
		}
	}
	return result
}
