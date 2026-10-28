package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type agyCheckExecutionDeps struct {
	runner agyCheckRunnerDeps
	ops    cleanupOps
	limit  time.Duration
}

func agyCheckExecute(o agyCheckOptions, d agyCheckDeps) agyCheckResult {
	x := agyCheckExecutionDeps{agyCheckRunnerDefaults(), systemCleanup(), 10 * time.Minute}
	if d.execution != nil {
		x = *d.execution
	}
	x.runner.check = d
	m, err := x.runner.validate(o, d)
	if err != nil {
		return agyCheckResult{InvalidReason: agyCheckPrintable(err.Error())}
	}
	binary, err := d.executable()
	if err != nil {
		return agyCheckResult{InvalidReason: "finding check executable"}
	}
	var results agyCheckRowResults
	for _, row := range agyCheckExpected {
		if err := agyCheckExecuteRow(o, x, m, binary, row, &results); err != nil {
			return agyCheckResult{InvalidReason: err.Error()}
		}
	}
	return agyCheckAssemble(m, x.runner.files, results, o.calls)
}

type agyCheckRowResults struct {
	before, after agyCheckPin
	observations  []agyCheckObservation
	loads         []string
}

// Both full-command and single-row checks use this collection and override path.
func agyCheckExecuteRow(o agyCheckOptions, x agyCheckExecutionDeps, m agyCheckMarker, binary string, row agyCheckRow, results *agyCheckRowResults) error {
	s, err := agyCheckScenarioFor(row.ID)
	if err != nil {
		return errors.New("missing scenario")
	}
	r, err := agyCheckNewRowDirs(m.Isolation, x.runner.files)
	if err != nil {
		return errors.New("preparing row directories failed")
	}
	config, fixtures, err := s.config(binary, row, r)
	if err == nil && row.ID == "outside-read" {
		err = x.runner.files.write(m.Isolation, s.probe(r), []byte("outside fixture\n"), true, 0o600)
	}
	if err != nil {
		_ = x.runner.files.cleanup(m.Isolation, r.Run)
		return errors.New("preparing row configuration failed")
	}
	observation := agyCheckObservation{ID: row.ID, Outcome: "collection failed"}
	run, err := agyCheckRunAgy(context.Background(), o, x.runner, m, r, s.prompt(r, o.calls), config, fixtures, x.limit, x.ops, func(r agyCheckRowDirs) error {
		e, diagnostic, err := agyCheckCollect(m.Isolation, r, s, x.runner.files)
		if err != nil {
			return err
		}
		parsed, parseErr := agyCheckTranscriptParse(e.Input.Transcript)
		if parseErr == nil {
			results.loads = append(results.loads, parsed.LoadedPaths...)
		}
		if diagnostic != "" {
			observation.Outcome = diagnostic
			return nil
		}
		observation = agyCheckEvidenceObservation(row, s, e)
		return nil
	})
	if err != nil {
		return errors.New("row validation, pin, collection, restoration or cleanup failed")
	}
	if len(results.observations) == 0 {
		results.before = run.Before
	}
	results.after = run.After
	if agyCheckPinReason(results.before, run.Before) != "" || agyCheckPinReason(results.before, results.after) != "" {
		return errors.New("pin changed between rows")
	}
	observation = agyCheckRunObservation(row, observation, run)
	// A nonzero exit can contain expected tool denials, but never repairs a
	// missing/malformed result; parsing already discards every partial fact.
	results.observations = append(results.observations, observation)
	return nil
}

func agyCheckRunObservation(row agyCheckRow, observation agyCheckObservation, run agyCheckRunResult) agyCheckObservation {
	if run.Code == 124 {
		observation = agyCheckObservation{ID: row.ID, Outcome: "run timeout"}
	}
	if len(run.Stragglers) > 0 {
		observation = agyCheckObservation{ID: row.ID, Outcome: "run left processes"}
	}
	return observation
}

func agyCheckAssemble(m agyCheckMarker, f agyCheckFiles, results agyCheckRowResults, calls int) agyCheckResult {
	result := agyCheckEvaluate(results.before, results.after, results.observations, calls, "", results.loads...)
	listing, err := agyCheckBuiltinListing(m.Home, f)
	if err != nil {
		listing = nil
	}
	result.BuiltinInfo = agyCheckBuiltinInfo(listing)
	return result
}

func agyCheckCollect(isolation string, r agyCheckRowDirs, s agyCheckScenario, f agyCheckFiles) (agyCheckEvidence, string, error) {
	var e agyCheckEvidence
	data, err := f.readInside(isolation, filepath.Join(r.Row, "out.jsonl"), agyCheckTranscriptMaxBytes)
	if err != nil {
		return e, "", err
	}
	e.Input.Transcript = data
	claims, claimErr := agyCheckReadClaims(isolation, r.State, f)
	records, stubErr := agyCheckReadStubRecords(isolation, r.Stub, f)
	e.Claims, e.Stub = claims, records
	diagnostic := ""
	if claimErr != nil || stubErr != nil {
		diagnostic = "invalid call records"
	}
	if diagnostic != "" {
		return e, diagnostic, nil
	}
	path := s.probe(r)
	if err := f.path(isolation, path); err != nil {
		return e, "", err
	}
	info, err := f.lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return e, "", err
	case !info.Mode().IsRegular():
		return e, "invalid probe file", nil
	default:
		e.Probe = true
	}
	if s.ID == "reconciliation" {
		entries, err := f.entries(r.Workspace)
		if err != nil {
			return e, "", err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				return e, "unexpected created file", nil
			}
			e.Input.CreatedFiles = append(e.Input.CreatedFiles, entry.Name())
		}
	}
	return e, "", nil
}
