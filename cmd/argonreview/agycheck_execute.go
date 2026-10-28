package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const agyCheckRunPath = "/usr/bin:/bin:/usr/sbin:/sbin"

type agyCheckRunnerDeps struct {
	check    agyCheckDeps
	files    agyCheckFiles
	validate func(agyCheckOptions, agyCheckDeps) (agyCheckMarker, error)
	signals  func(func(<-chan os.Signal) (int, error)) (int, error)
}

func agyCheckRunnerDefaults() agyCheckRunnerDeps {
	return agyCheckRunnerDeps{agyCheckDefaults(), agyCheckFilesDefaults(), agyCheckValidate, withSignals}
}

type agyCheckRunResult struct {
	Before, After agyCheckPin
	Transcript    []byte
	Code          int
	Stragglers    []string
}

// One invocation owns fresh directories until collect has read its evidence.
// The caller must stop on ANY returned error, including cleanup/restoration.
func agyCheckRunAgy(ctx context.Context, o agyCheckOptions, d agyCheckRunnerDeps, m agyCheckMarker, r agyCheckRowDirs, prompt string, config agyCheckConfigFiles, fixtures map[string][]byte, limit time.Duration, ops cleanupOps, collect func(agyCheckRowDirs) error) (result agyCheckRunResult, err error) {
	result.Code = 1
	// Validate the entire layout before assuming ownership for cleanup.
	if err := r.validate(m.Isolation, d.files); err != nil {
		return result, errors.Join(err, d.files.cleanup(m.Isolation, r.Run))
	}
	// An exclusive lease prevents accidental reuse of an existing row, including
	// a second call from collect while the first invocation still owns the row.
	if err := d.files.write(m.Isolation, filepath.Join(r.Row, "started"), nil, true, 0o600); err != nil {
		return result, err
	}
	defer func() {
		if recover() != nil {
			err = errors.Join(err, errors.New("agy-check runner panic"))
		}
		err = errors.Join(err, d.files.cleanup(m.Isolation, r.Run))
	}()
	if limit <= 0 {
		return result, errors.New("run time limit must be positive")
	}
	_, err = d.signals(func(signals <-chan os.Signal) (int, error) {
		validate := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case <-signals:
				return errors.New("agy-check interrupted")
			default:
			}
			fresh, err := d.validate(o, d.check)
			if err != nil {
				return err
			}
			a, _ := json.Marshal(fresh)
			b, _ := json.Marshal(m)
			if !bytes.Equal(a, b) {
				return errors.New("isolation marker changed during run")
			}
			return r.validate(m.Isolation, d.files)
		}
		pin := func() (agyCheckPin, error) {
			// RecordPin starts --version: this validation belongs to that start,
			// never to the next -p or the next pin. Its TMPDIR is isolation-local.
			if err := validate(); err != nil {
				return agyCheckPin{}, err
			}
			return agyCheckRecordPin(ctx, o, m.Isolation, d.check, ops)
		}
		before, err := pin()
		result.Before = before
		if err != nil {
			return 1, err
		}
		if reason := agyCheckPinReason(before, before); reason != "" {
			return 1, errors.New(reason)
		}
		// No validation from --version is reused for the actual agent start.
		if err := validate(); err != nil {
			return 1, err
		}
		runErr := agyCheckPlace(m, r, config, fixtures, d.files, func() (err error) {
			logs := make([]*os.File, 0, 2)
			defer func() {
				for _, log := range logs {
					err = errors.Join(err, log.Close())
				}
			}()
			for _, name := range []string{"out.jsonl", "err.txt"} {
				path := filepath.Join(r.Row, name)
				if err := d.files.path(m.Isolation, path); err != nil {
					return err
				}
				log, err := d.files.open(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return err
				}
				logs = append(logs, log)
			}
			printLimit := limit - 2*time.Minute
			if printLimit <= 0 {
				printLimit = limit / 2
			}
			args := []string{"-p", prompt, "--model", o.model, "--sandbox", "--print-timeout", printLimit.String(), "--output-format", "stream-json"}
			sup := supervisor{limit, 5 * time.Second, signals, ops}
			run := sup.runResult(o.agy, args, agyEnvironment([]string{"PATH=" + agyCheckRunPath}, m.Home, r.Tmp), r.Workspace, logs[0], logs[1])
			result.Code, result.Stragglers = run.code, run.stragglers
			result.Transcript, err = agyCheckReadLog(logs[0])
			return errors.Join(run.err, err)
		})
		// Restoration precedes the final validation/pin. Even failed runs cannot
		// provide usable observations unless their before/after pins agree.
		after, pinErr := pin()
		result.After = after
		if reason := agyCheckPinReason(before, after); reason != "" {
			pinErr = errors.Join(pinErr, errors.New(reason))
		}
		if collect != nil {
			runErr = errors.Join(runErr, collect(r))
		}
		return result.Code, errors.Join(runErr, pinErr)
	})
	return result, err
}
