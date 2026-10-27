package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// This primitive is intentionally not called by part A's command. Part B must
// call it twice, around the rows, after validation; never reuse the first pin.
// Before each agy start, part B must call agyCheckValidate to re-read and
// re-validate the marker, canaries and executables; never reuse it across rows.
func agyCheckRecordPin(ctx context.Context, o agyCheckOptions, isolation string, d agyCheckDeps, ops cleanupOps) (pin agyCheckPin, err error) {
	if err := ctx.Err(); err != nil {
		return pin, fmt.Errorf("recording pin: %w", err)
	}
	tmp, err := os.MkdirTemp(isolation, "pin-*")
	if err != nil {
		return pin, fmt.Errorf("creating pin directory: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(tmp)) }()
	out, err := os.CreateTemp(tmp, "version-*")
	if err != nil {
		return pin, fmt.Errorf("creating version log: %w", err)
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	code, err := withSignals(func(signals <-chan os.Signal) (int, error) {
		sup := supervisor{30 * time.Second, time.Second, signals, ops}
		r := sup.runResult(o.agy, []string{"--version"}, agyEnvironment(os.Environ(), o.home, tmp), tmp, out, out)
		return r.code, r.err
	})
	if err != nil {
		return pin, fmt.Errorf("running version command: %w", err)
	}
	if code != 0 {
		return pin, fmt.Errorf("version command exited %d", code)
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return pin, fmt.Errorf("seeking version log: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(out, 64<<10))
	if err != nil {
		return pin, fmt.Errorf("reading version log: %w", err)
	}
	for _, token := range strings.Fields(string(data)) {
		if agyCheckVersionPattern.MatchString(token) {
			pin.Version = token
			break
		}
	}
	executable, err := d.open(o.agy)
	if err != nil {
		return pin, fmt.Errorf("opening executable for pin: %w", err)
	}
	defer func() { err = errors.Join(err, executable.Close()) }()
	hash := sha256.New()
	if _, err := io.Copy(hash, executable); err != nil {
		return pin, fmt.Errorf("reading executable for pin: %w", err)
	}
	pin.ExecutableSHA256 = fmt.Sprintf("%x", hash.Sum(nil))
	return pin, nil
}
