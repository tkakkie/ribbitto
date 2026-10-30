package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const agyCheckKeepMarker = "synthetic transcript KEEP_MARKER_1983"

func TestAgyCheckKeepFlag(t *testing.T) {
	base := []string{"--home", "/check/home", "--agy", "/trusted/agy"}
	for _, tc := range []struct {
		args        []string
		keep, valid bool
	}{{nil, false, true}, {[]string{"--keep-transcripts"}, true, true}, {[]string{"--keep-transcripts=true"}, true, true}, {[]string{"--keep-transcripts=false"}, false, false}, {[]string{"--keep-transcripts", "--keep-transcripts"}, false, false}} {
		o, err := agyCheckArguments(append(append([]string{}, base...), tc.args...))
		if (err == nil) != tc.valid || err == nil && o.keepTranscripts != tc.keep {
			t.Fatalf("%v: %+v %v", tc.args, o, err)
		}
	}
}

func TestAgyCheckKeepFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{{"short", 80}, {"caps", agyCheckTranscriptMaxBytes + 17}} {
		t.Run(tc.name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			data := bytes.Repeat([]byte("x"), tc.size)
			copy(data, agyCheckKeepMarker)
			for _, name := range []string{"out.jsonl", "err.txt"} {
				agyCheckTestWrite(t, filepath.Join(r.Row, name), data, 0o600)
			}
			read, open := f.read, f.open
			f.read = func(p string, cap int64) ([]byte, error) {
				t.Fatal("retention used evidence reader")
				return read(p, cap)
			}
			f.open = func(p string, flags int, mode os.FileMode) (*os.File, error) {
				if strings.Contains(p, "/kept/") && flags&os.O_WRONLY == 0 {
					t.Fatal("read back retained file")
				}
				return open(p, flags, mode)
			}
			if err := agyCheckKeepRow(m.Isolation, r.Row, "hook-silent", f); err != nil {
				t.Fatal(err)
			}
			for rel, cap := range map[string]int{"": 0, "hook-silent": 0, "hook-silent/out.jsonl": agyCheckTranscriptMaxBytes, "hook-silent/err.txt": 64 << 10} {
				p := filepath.Join(m.Isolation, "kept", rel)
				info, err := os.Lstat(p)
				mode := os.FileMode(0o600)
				if cap == 0 {
					mode = os.ModeDir | 0o700
				}
				if err != nil || info.Mode() != mode || f.private(p, cap == 0) != nil {
					t.Fatalf("private retained path %s: %v %v", rel, info, err)
				}
				if cap > 0 {
					got, err := os.ReadFile(p)
					if err != nil || !bytes.Equal(got, data[:min(cap, len(data))]) {
						t.Fatalf("prefix/cap %s: %d %v", rel, len(got), err)
					}
				}
			}
		})
	}
}

func TestAgyCheckKeepRefusals(t *testing.T) {
	for _, name := range []string{"outside", "kept-symlink", "row-symlink", "owner", "source-owner", "file-owner", "read", "mkdir", "write"} {
		t.Run(name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			for _, name := range []string{"out.jsonl", "err.txt"} {
				agyCheckTestWrite(t, filepath.Join(r.Row, name), []byte(agyCheckKeepMarker), 0o600)
			}
			kept := filepath.Join(m.Isolation, "kept")
			switch name {
			case "outside":
				r.Row = agyCheckRealTemp(t)
				for _, file := range []string{"out.jsonl", "err.txt"} {
					agyCheckTestWrite(t, filepath.Join(r.Row, file), []byte(agyCheckKeepMarker), 0o600)
				}
			case "kept-symlink":
				if err := os.Symlink(r.Row, kept); err != nil {
					t.Fatal(err)
				}
			case "row-symlink":
				if err := os.Mkdir(kept, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(agyCheckRealTemp(t), filepath.Join(kept, "hook-silent")); err != nil {
					t.Fatal(err)
				}
			case "owner", "source-owner", "file-owner":
				stat := f.lstat
				f.lstat = func(p string) (os.FileInfo, error) {
					info, err := stat(p)
					if err == nil && (p == kept && name == "owner" || p == filepath.Join(r.Row, "out.jsonl") && name == "source-owner" || p == filepath.Join(kept, "hook-silent", "out.jsonl") && name == "file-owner") {
						return agyCheckOwnedInfo{info, info.Mode(), f.uid + 1}, nil
					}
					return info, err
				}
			case "mkdir":
				f.mkdir = func(string, os.FileMode) error { return os.ErrPermission }
			case "read", "write":
				open := f.open
				f.open = func(p string, flags int, mode os.FileMode) (*os.File, error) {
					if name == "write" && strings.HasPrefix(p, kept+"/") || name == "read" && flags&os.O_WRONLY == 0 {
						return nil, os.ErrPermission
					}
					return open(p, flags, mode)
				}
			}
			if err := agyCheckKeepRow(m.Isolation, r.Row, "hook-silent", f); err == nil {
				t.Fatal("unsafe retention accepted")
			}
		})
	}
}

func TestAgyCheckKeepReady(t *testing.T) {
	for _, name := range []string{"missing", "empty", "nonempty", "symlink", "owner", "mode", "entries-error", "off"} {
		t.Run(name, func(t *testing.T) {
			h := agyCheckNewCommandHarness(t, "", "")
			kept := filepath.Join(h.m.Isolation, "kept")
			if name != "missing" && name != "symlink" {
				if err := os.Mkdir(kept, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if name == "nonempty" || name == "off" {
				agyCheckTestWrite(t, filepath.Join(kept, "stale"), nil, 0o600)
			}
			if name == "symlink" {
				if err := os.Symlink(agyCheckRealTemp(t), kept); err != nil {
					t.Fatal(err)
				}
			}
			stat := h.x.runner.files.lstat
			h.x.runner.files.lstat = func(p string) (os.FileInfo, error) {
				if name == "off" && strings.HasPrefix(p, kept) {
					t.Fatal("flag off inspected kept")
				}
				info, err := stat(p)
				if err == nil && p == kept {
					if name == "owner" {
						return agyCheckOwnedInfo{info, info.Mode(), os.Getuid() + 1}, nil
					}
					if name == "mode" {
						return agyCheckOwnedInfo{info, os.ModeDir | 0o755, os.Getuid()}, nil
					}
				}
				return info, err
			}
			if name == "entries-error" {
				h.x.runner.files.entries = func(string) ([]os.DirEntry, error) { return nil, os.ErrPermission }
			}
			allowed := name == "missing" || name == "empty" || name == "off"
			// Allowed cases stop just after the pre-check; refusals have a usable runner.
			if allowed {
				h.d.executable = func() (string, error) { return "", os.ErrPermission }
			}
			h.x.runner.validate = func(agyCheckOptions, agyCheckDeps) (agyCheckMarker, error) { return h.m, nil }
			r := agyCheckExecute(agyCheckOptions{home: h.m.Home, agy: h.agy, model: agyCheckModel, calls: 30, keepTranscripts: name != "off"}, h.d)
			want := "kept transcripts directory must be missing or empty, private and uid-owned"
			if allowed {
				want = "finding check executable"
			}
			if r.InvalidReason != want || len(r.Rows) != 0 || h.starts != 0 {
				t.Fatalf("pre-run refusal: %+v starts=%d", r, h.starts)
			}
			args := []string{"--home", h.m.Home, "--agy", h.agy}
			suffix := ""
			if name != "off" {
				args = append(args, "--keep-transcripts")
				suffix = "kept transcripts: none\n"
			}
			var out, stderr bytes.Buffer
			if code := agyCheckCommand(args, &out, &stderr, h.d); code != 1 || out.String() != "RESULT INVALID: "+want+"\n"+suffix || stderr.Len() != 0 || h.starts != 0 {
				t.Fatalf("pre-run report/start: %d %s %s starts=%d", code, out.String(), stderr.String(), h.starts)
			}
		})
	}
}

func TestAgyCheckKeepRows(t *testing.T) {
	for _, name := range []string{"ok", "differs", "unparsed", "timeout", "straggler", "collection", "write"} {
		t.Run(name, func(t *testing.T) {
			deviation := ""
			if name == "differs" || name == "write" {
				deviation = "flip"
			}
			if name == "unparsed" || name == "timeout" {
				deviation = name
			}
			h := agyCheckNewCommandHarness(t, "hook-silent", deviation)
			h.x.runner.check = h.d
			if name == "timeout" {
				h.x.limit = 200 * time.Millisecond
			}
			if name == "straggler" {
				scans := 0
				h.x.ops.scan = func(int) ([]int, error) {
					scans++
					if scans == 2 {
						return []int{999999}, nil
					}
					return nil, nil
				}
				h.x.ops.killMember = func(int, *child) {}
				h.x.ops.commandLine = func(int) string { return "synthetic straggler" }
			}
			if name == "collection" {
				read := h.x.runner.files.read
				h.x.runner.files.read = func(p string, cap int64) ([]byte, error) {
					if filepath.Base(p) == "out.jsonl" {
						return nil, os.ErrPermission
					}
					return read(p, cap)
				}
			}
			if name == "write" {
				open := h.x.runner.files.open
				h.x.runner.files.open = func(p string, flags int, mode os.FileMode) (*os.File, error) {
					if strings.Contains(p, "/kept/") {
						return nil, os.ErrPermission
					}
					return open(p, flags, mode)
				}
			}
			var results agyCheckRowResults
			err := agyCheckExecuteRow(agyCheckOptions{home: h.m.Home, agy: h.agy, model: agyCheckModel, calls: 30, keepTranscripts: true}, h.x, h.m, h.tool, agyCheckObserveFindRow(t, "hook-silent"), &results)
			wantErr := name == "collection" || name == "write"
			if (err != nil) != wantErr || name == "write" && !strings.Contains(err.Error(), string(agyCheckKeeping)) {
				t.Fatalf("row failure: %v", err)
			}
			_, statErr := os.Lstat(filepath.Join(h.m.Isolation, "kept", "hook-silent", "out.jsonl"))
			if (statErr == nil) != (name != "ok" && name != "write") {
				t.Fatalf("retained wrong row: %v", statErr)
			}
			entries, e := os.ReadDir(h.m.Isolation)
			if e != nil {
				t.Fatal(e)
			}
			for _, entry := range entries {
				if entry.Name() != "kept" {
					t.Fatal("row cleanup changed")
				}
			}
		})
	}
}

func TestAgyCheckKeepOutput(t *testing.T) {
	pin, observations := agyCheckFixtures()
	baseline := agyCheckEvaluate(pin, pin, observations, 30, "")
	var before bytes.Buffer
	if err := agyCheckRender(&before, baseline); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{false, true} {
		m, r, f := agyCheckTestRow(t)
		for _, name := range []string{"out.jsonl", "err.txt"} {
			agyCheckTestWrite(t, filepath.Join(r.Row, name), []byte(agyCheckKeepMarker), 0o600)
		}
		var results agyCheckRowResults
		if !keep {
			f.read = func(string, int64) ([]byte, error) { t.Fatal("flag off read bytes"); return nil, nil }
			f.open = func(string, int, os.FileMode) (*os.File, error) { t.Fatal("flag off opened file"); return nil, nil }
		}
		for _, id := range []string{"discovery-root", "task-off", "outside-read"} {
			observation := agyCheckObservation{ID: id, Outcome: "unparsed transcript"}
			if id == "discovery-root" {
				observation = agyCheckObservation{ID: id, Outcome: agyCheckLoadUnobservable, NotLoaded: true}
			}
			retain := agyCheckRetainer(agyCheckOptions{calls: 30, keepTranscripts: keep}, m.Isolation, f, agyCheckObserveFindRow(t, id), &observation, &results)
			if retain != nil {
				if err := retain(r, agyCheckRunResult{}); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Exercise final discovery promotion without reading provisional copies.
		f.read = func(string, int64) ([]byte, error) { t.Fatal("read retained output"); return nil, nil }
		f.open = func(string, int, os.FileMode) (*os.File, error) { t.Fatal("opened retained output"); return nil, nil }
		final := baseline
		// Two rows differ in this report; discovery's final classification is ok.
		if keep {
			final.Rows = append([]agyCheckResultRow{}, baseline.Rows...)
			final.Rows[27].OK = false
			final.Rows[23].OK = false
		}
		result := agyCheckFinishKeep(agyCheckOptions{keepTranscripts: keep}, m.Isolation, f, results.kept, final)
		var out bytes.Buffer
		if err := agyCheckRender(&out, result); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmt.Sprintf("%+v %+v %+v %s", result, pin, results, out.String()), agyCheckKeepMarker) {
			t.Fatal("transcript leaked into output/pins/result")
		}
		if !keep {
			if !bytes.Equal(out.Bytes(), before.Bytes()) {
				t.Fatal("flag-off complete report changed")
			}
			if _, err := os.Lstat(filepath.Join(m.Isolation, "kept")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("flag off created kept")
			}
		} else {
			if !reflect.DeepEqual(result.KeptRows, []string{"task-off", "outside-read"}) || !strings.HasSuffix(out.String(), "kept transcripts: task-off, outside-read\n") {
				t.Fatalf("kept IDs/report: %+v %s", result.KeptRows, out.String())
			}
			for _, id := range result.KeptRows {
				if _, err := os.Lstat(filepath.Join(m.Isolation, "kept", id, "out.jsonl")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Lstat(filepath.Join(m.Isolation, "kept", "discovery-root")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("kept final ok discovery")
			}
		}
	}
	var out bytes.Buffer
	if err := agyCheckRender(&out, agyCheckResult{KeptRows: []string{}}); err != nil || !strings.HasSuffix(out.String(), "kept transcripts: none\n") {
		t.Fatal("missing none line")
	}
}

// Frozen from the merged, unmodified 89af1b2 sources with retention off.
// The fake has no hook events, so both observations from reconciliation differ.
func TestAgyCheckKeepOffReport(t *testing.T) {
	h := agyCheckNewCommandHarness(t, "", "")
	baselineCode, out := h.run(t, 30)
	if !strings.Contains(out, "Rows: 30; differing: 2\n") {
		t.Fatalf("merged report row count changed: %s", out)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(out))); got != "790ced44fe584e3ca9e3cd4eb0903280925a33d09a91220a9ecadfc3d842bd62" {
		t.Fatalf("complete flag-off report changed: %s", got)
	}
	h.restored(t)
	stale := filepath.Join(h.m.Isolation, "kept", "stale")
	agyCheckTestWrite(t, stale, []byte(agyCheckKeepMarker), 0o600)
	stat := h.x.runner.files.lstat
	h.x.runner.files.lstat = func(p string) (os.FileInfo, error) {
		if strings.HasPrefix(p, filepath.Dir(stale)) {
			t.Fatal("flag off inspected stale kept")
		}
		return stat(p)
	}
	if code, again := h.run(t, 30); code != baselineCode || again != out {
		t.Fatal("stale kept changed complete flag-off output")
	}
	h.x.runner.files.lstat = stat
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(stale)); err != nil {
		t.Fatal(err)
	}
	args := []string{"--home", h.m.Home, "--agy", h.agy, "--keep-transcripts"}
	var kept, stderr bytes.Buffer
	code := agyCheckCommand(args, &kept, &stderr, h.d)
	if code != baselineCode || stderr.Len() != 0 || kept.String() != out+"kept transcripts: reconciliation\n" {
		t.Fatalf("flag-on changed complete report: code=%d %s %s", code, kept.String(), stderr.String())
	}
	entries, err := os.ReadDir(filepath.Join(h.m.Isolation, "kept"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "reconciliation" {
		t.Fatalf("final ok rows retained: %v %v", entries, err)
	}
}

// An otherwise matching observation must still be retained after either override.
func TestAgyCheckKeepOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  agyCheckRunResult
	}{{"timeout", agyCheckRunResult{Code: 124}}, {"stragglers", agyCheckRunResult{Stragglers: []string{"synthetic straggler"}}}} {
		t.Run(tc.name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			for _, name := range []string{"out.jsonl", "err.txt"} {
				agyCheckTestWrite(t, filepath.Join(r.Row, name), []byte(agyCheckKeepMarker), 0o600)
			}
			observation := agyCheckObservation{ID: "hook-silent", Outcome: "allowed"}
			var results agyCheckRowResults
			retain := agyCheckRetainer(agyCheckOptions{calls: 30, keepTranscripts: true}, m.Isolation, f, agyCheckObserveFindRow(t, observation.ID), &observation, &results)
			if err := retain(r, tc.run); err != nil || len(results.kept) != 1 {
				t.Fatalf("override lost: %v %+v", err, results.kept)
			}
		})
	}
}

func TestAgyCheckKeepCleanupFailure(t *testing.T) {
	m, r, f := agyCheckTestRow(t)
	for _, name := range []string{"out.jsonl", "err.txt"} {
		agyCheckTestWrite(t, filepath.Join(r.Row, name), []byte(agyCheckKeepMarker), 0o600)
	}
	if err := agyCheckKeepRow(m.Isolation, r.Row, "discovery-root", f); err != nil {
		t.Fatal(err)
	}
	f.remove = func(string) error { return errors.New(agyCheckKeepMarker) }
	result := agyCheckFinishKeep(agyCheckOptions{keepTranscripts: true}, m.Isolation, f, []string{"discovery-root"}, agyCheckResult{Rows: []agyCheckResultRow{{agyCheckRow: agyCheckRow{ID: "discovery-root"}, OK: true}}})
	if result.InvalidReason != string(agyCheckKeeping) || agyCheckExitCode(result) != 1 || strings.Contains(fmt.Sprint(result), agyCheckKeepMarker) {
		t.Fatalf("provisional cleanup failure lost/leaked: %+v", result)
	}
}
