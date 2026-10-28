package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const agyCheckHookTestDir = "/fictional-check/middle/parent/hook"

type agyCheckHookFile struct {
	bytes.Buffer
	closed             bool
	writeErr, closeErr error
	short              bool
}

func (f *agyCheckHookFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.short {
		return 0, nil
	}
	return f.Buffer.Write(p)
}
func (f *agyCheckHookFile) Close() error { f.closed = true; return f.closeErr }

type agyCheckHookHarness struct {
	d     agyCheckHookDeps
	info  map[string]agyCheckInfo
	files map[string]*agyCheckHookFile
	wait  time.Duration
}

func agyCheckHookHarnessNew(t *testing.T) *agyCheckHookHarness {
	t.Helper()
	h := &agyCheckHookHarness{info: map[string]agyCheckInfo{
		"/":                              {os.ModeDir | 0o755, 0},
		"/fictional-check":               {os.ModeDir | 0o755, 0},
		"/fictional-check/middle":        {os.ModeDir | 0o755, 0},
		"/fictional-check/middle/parent": {os.ModeDir | 0o755, 0},
		agyCheckHookTestDir:              {os.ModeDir | 0o700, 501},
		agyCheckHookTestDir + "/" + agyCheckHookSentinel: {0o600, 501},
		agyCheckHookTestDir + "/calls":                   {os.ModeDir | 0o700, 501},
	}, files: make(map[string]*agyCheckHookFile)}
	guard := func(path string) {
		t.Helper()
		// Fictional protected roots are intercepted before any filesystem access.
		ancestor := path == "/" || strings.HasPrefix(agyCheckHookTestDir, path+"/")
		if (!ancestor && !implementRunInside(agyCheckHookTestDir, path)) || agyCheckSettingsPath(path) {
			t.Fatalf("forbidden filesystem access: %s", path)
		}
	}
	h.d = agyCheckHookDeps{
		current: func() (*user.User, error) { return &user.User{Uid: "501"}, nil },
		uid:     func() (int, int) { return 501, 501 }, stdin: strings.NewReader("ignored"),
		lstat: func(path string) (os.FileInfo, error) {
			guard(path)
			if info, ok := h.info[path]; ok {
				return info, nil
			}
			return nil, os.ErrNotExist
		},
		mkdir: func(path string, mode os.FileMode) error {
			guard(path)
			if path != agyCheckHookTestDir+"/calls" || mode != 0o700 {
				t.Fatalf("unsafe mkdir: %s %v", path, mode)
			}
			return os.ErrExist
		},
		create: func(path string, flags int, mode os.FileMode) (io.WriteCloser, error) {
			guard(path)
			if flags != os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW || mode != 0o600 {
				t.Fatalf("unsafe claim flags/mode: %d %v", flags, mode)
			}
			if _, exists := h.files[path]; exists {
				return nil, os.ErrExist
			}
			file := &agyCheckHookFile{}
			h.files[path] = file
			return file, nil
		},
		sleep: func(wait time.Duration) { h.wait += wait },
	}
	return h
}

func agyCheckHookRun(t *testing.T, h *agyCheckHookHarness, args []string, wantCode int, wantOut, wantErr string) {
	t.Helper()
	var out, stderr strings.Builder
	code := agyCheckHook(args, &out, &stderr, h.d)
	if code != wantCode || out.String() != wantOut || stderr.String() != wantErr {
		t.Fatalf("hook result: code=%d stdout=%q stderr=%q; want %d %q %q", code, out.String(), stderr.String(), wantCode, wantOut, wantErr)
	}
}

func TestAgyCheckHookModes(t *testing.T) {
	const allow = "{\"decision\":\"allow\"}\n"
	const deny = "{\"decision\":\"deny\",\"reason\":\"agy-check fixture denial\"}\n"
	cases := []struct {
		mode, out, stderr, word string
		code                    int
	}{
		{"allow", allow, "", "allow", 0}, {"deny", deny, "", "deny", 0},
		{"exit2", deny, "agy-check fixture denial\n", "exit2", 2},
		{"exit1", "", "agy-check fixture denial\n", "exit1", 1},
		{"unparseable", "not JSON\n", "", "unparseable", 0}, {"silent", "", "", "silent", 0},
		{"hang", allow, "", "hang", 0}, {"alternate", allow, "", "allow", 0},
	}
	if len(cases) != len(agyCheckHookModes) {
		t.Fatal("mode test coverage drifted")
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			agyCheckHookRun(t, h, []string{tc.mode, agyCheckHookTestDir, "location-1"}, tc.code, tc.out, tc.stderr)
			file := h.files[agyCheckHookTestDir+"/calls/000001"]
			if len(h.files) != 1 || file == nil || !file.closed || file.String() != tc.word+" location-1\n" {
				t.Fatalf("missing closed call record: %+v", h.files)
			}
			if tc.mode == "hang" {
				if h.wait != 60*time.Second || h.wait <= time.Duration(agyCheckHookTimeout)*time.Second {
					t.Fatalf("hang must outwait timeout: %s", h.wait)
				}
			} else if h.wait != 0 {
				t.Fatal("unexpected sleep")
			}
			if tc.mode == "alternate" {
				agyCheckHookRun(t, h, []string{tc.mode, agyCheckHookTestDir}, 0, deny, "")
				if h.files[agyCheckHookTestDir+"/calls/000002"].String() != "deny -\n" {
					t.Fatal("even invocation must deny with absent tag marker")
				}
			}
		})
	}
}

func TestAgyCheckHookStateRefusals(t *testing.T) {
	for _, dir := range []string{"relative", "/", "/a/../b", "/a'b", "/a\nb", "/protected/account/settings.json", "/protected/worker/settings.json/child"} {
		t.Run(dir, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			var out, stderr strings.Builder
			if code := agyCheckHook([]string{"allow", dir}, &out, &stderr, h.d); code != 1 || out.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 || len(h.files) != 0 {
				t.Fatalf("unsafe directory accepted: %d %q %q", code, out.String(), stderr.String())
			}
		})
	}
	for _, part := range []string{"", "/" + agyCheckHookSentinel, "/calls"} {
		for _, kind := range []string{"missing", "symlink", "owner", "mode", "type", "special bits"} {
			t.Run(part+"/"+kind, func(t *testing.T) {
				h := agyCheckHookHarnessNew(t)
				path := agyCheckHookTestDir + part
				info := h.info[path]
				switch kind {
				case "missing":
					delete(h.info, path)
				case "symlink":
					info.mode = os.ModeSymlink | info.mode.Perm()
					h.info[path] = info
				case "owner":
					info.uid++
					h.info[path] = info
				case "mode":
					info.mode |= 0o040
					h.info[path] = info
				case "type":
					info.mode ^= os.ModeDir
					h.info[path] = info
				case "special bits":
					info.mode |= os.ModeSetgid
					h.info[path] = info
				}
				var out, stderr strings.Builder
				if code := agyCheckHook([]string{"allow", agyCheckHookTestDir}, &out, &stderr, h.d); code != 1 || out.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 || len(h.files) != 0 {
					t.Fatalf("unsafe %s %s accepted: %d %q %q", part, kind, code, out.String(), stderr.String())
				}
			})
		}
	}
}

func TestAgyCheckHookAncestors(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		mode       os.FileMode
		err        error
	}{
		{"root", "/", os.ModeSymlink | 0o755, nil},
		{"first", "/fictional-check", os.ModeSymlink | 0o755, nil},
		{"middle", "/fictional-check/middle", os.ModeSymlink | 0o755, nil},
		{"parent", filepath.Dir(agyCheckHookTestDir), os.ModeSymlink | 0o755, nil},
		{"non-directory", "/fictional-check/middle", 0o755, nil},
		{"lstat error", "/fictional-check/middle", os.ModeDir | 0o755, os.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			h.info[tc.path] = agyCheckInfo{tc.mode, 0}
			lstat := h.d.lstat
			var visited []string
			h.d.lstat = func(path string) (os.FileInfo, error) {
				visited = append(visited, path)
				if path == tc.path && tc.err != nil {
					return nil, tc.err
				}
				return lstat(path)
			}
			var out, stderr strings.Builder
			code := agyCheckHook([]string{"allow", agyCheckHookTestDir}, &out, &stderr, h.d)
			if code != 1 || out.Len() != 0 || stderr.Len() == 0 || len(h.files) != 0 {
				t.Fatalf("unsafe ancestor accepted: %d %q %q", code, out.String(), stderr.String())
			}
			// Metadata checks must stop at the bad component, before traversing it.
			if len(visited) == 0 || visited[len(visited)-1] != tc.path {
				t.Fatalf("walk did not stop at %s: %v", tc.path, visited)
			}
		})
	}
}

func TestAgyCheckHookIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, uid       string
		real, effective int
		err             error
		want            int
	}{
		{"normal", "501", 501, 501, nil, 0},
		{"real root", "501", 0, 501, nil, 1},
		{"effective root", "501", 501, 0, nil, 1},
		{"both root", "0", 0, 0, nil, 1},
		{"real mismatch", "501", 502, 501, nil, 1},
		{"effective mismatch", "501", 501, 502, nil, 1},
		{"lookup mismatch", "502", 501, 501, nil, 1},
		{"lookup failure", "501", 501, 501, os.ErrPermission, 1},
		{"unparsable uid", "bad", 501, 501, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			h.d.uid = func() (int, int) { return tc.real, tc.effective }
			h.d.current = func() (*user.User, error) { return &user.User{Uid: tc.uid}, tc.err }
			// Match filesystem ownership to the real uid so a weakened identity
			// predicate cannot be rescued by an unrelated ownership refusal.
			for path, info := range h.info {
				info.uid = uint32(tc.real)
				h.info[path] = info
			}
			var out, stderr strings.Builder
			code := agyCheckHook([]string{"allow", agyCheckHookTestDir}, &out, &stderr, h.d)
			if code != tc.want {
				t.Fatalf("identity exit: got %d want %d (%q)", code, tc.want, stderr.String())
			}
			if tc.want == 1 {
				if out.Len() != 0 || stderr.Len() == 0 || len(h.files) != 0 {
					t.Fatalf("identity refusal leaked output/claim: %q %q %d", out.String(), stderr.String(), len(h.files))
				}
			} else if out.String() != "{\"decision\":\"allow\"}\n" || stderr.Len() != 0 || len(h.files) != 1 {
				t.Fatalf("normal identity refused: %q %q %d", out.String(), stderr.String(), len(h.files))
			}
		})
	}
}

func TestAgyCheckHookUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"allow"}, {"allow", agyCheckHookTestDir, "tag", "extra"}, {"unknown", agyCheckHookTestDir}, {"allow", agyCheckHookTestDir, ""}, {"allow", agyCheckHookTestDir, "-bad"}, {"allow", agyCheckHookTestDir, "Bad"}, {"allow", agyCheckHookTestDir, "a b"}, {"allow", agyCheckHookTestDir, "a\n"}, {"allow", agyCheckHookTestDir, strings.Repeat("a", 41)}} {
		h := agyCheckHookHarnessNew(t)
		var out, stderr strings.Builder
		if code := agyCheckHook(args, &out, &stderr, h.d); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), agyCheckHookUsage) || len(h.files) != 0 {
			t.Fatalf("invalid usage accepted: %v: %d %q %q", args, code, out.String(), stderr.String())
		}
		for _, mode := range agyCheckHookModes {
			if !strings.Contains(stderr.String(), mode.name) {
				t.Fatalf("usage missing mode %s", mode.name)
			}
		}
	}
	for _, tag := range []string{"a", "0", "a-0", strings.Repeat("z", 40)} {
		h := agyCheckHookHarnessNew(t)
		agyCheckHookRun(t, h, []string{"silent", agyCheckHookTestDir, tag}, 0, "", "")
	}
}

type agyCheckHookReader struct {
	err   error
	check func()
}

func (r *agyCheckHookReader) Read([]byte) (int, error) {
	if r.check != nil {
		r.check()
	}
	return 0, r.err
}

func TestAgyCheckHookStdin(t *testing.T) {
	for _, input := range []string{"", "invalid JSON", `{"decision":"deny"}`, strings.Repeat("x", maxGuardInput*2)} {
		h := agyCheckHookHarnessNew(t)
		r := strings.NewReader(input)
		h.d.stdin = r
		agyCheckHookRun(t, h, []string{"allow", agyCheckHookTestDir}, 0, "{\"decision\":\"allow\"}\n", "")
		if consumed := len(input) - r.Len(); consumed != min(len(input), maxGuardInput+1) {
			t.Fatalf("stdin not consumed to cap: %d", consumed)
		}
	}
	h := agyCheckHookHarnessNew(t)
	h.d.stdin = &agyCheckHookReader{err: errors.New("input failure"), check: func() {
		f := h.files[agyCheckHookTestDir+"/calls/000001"]
		if f == nil || !f.closed || f.String() != "allow -\n" {
			t.Error("stdin read preceded completed claim")
		}
	}}
	agyCheckHookRun(t, h, []string{"allow", agyCheckHookTestDir}, 1, "", "agy-check hook: reading hook input: input failure\n")
	f := h.files[agyCheckHookTestDir+"/calls/000001"]
	if len(h.files) != 1 || f == nil || !f.closed || f.String() != "allow -\n" {
		t.Fatal("stdin failure must leave exactly one completed claim")
	}
	// Also exercise removal of an already-written record on the real filesystem.
	dir := agyCheckHookPrepared(t)
	var out, stderr strings.Builder
	code := agyCheckHook([]string{"allow", dir}, &out, &stderr, agyCheckHookFileDeps(&agyCheckHookReader{err: errors.New("input failure")}))
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "reading hook input: input failure") {
		t.Fatalf("stdin failure emitted a decision: %d %q %q", code, out.String(), stderr.String())
	}
	entries, err := os.ReadDir(filepath.Join(dir, "calls"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "000001" {
		t.Fatalf("stdin failure must preserve exactly one claim: %v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "calls", "000001"))
	if err != nil || string(data) != "allow -\n" {
		t.Fatalf("stdin failure changed claim: %q %v", data, err)
	}
}

func TestAgyCheckHookLogFailures(t *testing.T) {
	for _, kind := range []string{"mkdir", "create", "write", "short write", "close", "cap"} {
		t.Run(kind, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			file := &agyCheckHookFile{}
			switch kind {
			case "mkdir":
				h.d.mkdir = func(string, os.FileMode) error { return os.ErrPermission }
			case "create":
				h.d.create = func(string, int, os.FileMode) (io.WriteCloser, error) { return nil, os.ErrPermission }
			case "cap":
				attempts := 0
				h.d.create = func(path string, _ int, _ os.FileMode) (io.WriteCloser, error) {
					attempts++
					if path != fmt.Sprintf("%s/calls/%06d", agyCheckHookTestDir, attempts) || attempts > 10000 {
						t.Fatalf("claim cap/sequence: %d %s", attempts, path)
					}
					return nil, os.ErrExist
				}
				defer func() {
					if attempts != 10000 {
						t.Errorf("claim cap: got %d attempts", attempts)
					}
				}()
			default:
				if kind == "write" {
					file.writeErr = os.ErrPermission
				}
				if kind == "short write" {
					file.short = true
				}
				if kind == "close" {
					file.closeErr = os.ErrPermission
				}
				h.d.create = func(string, int, os.FileMode) (io.WriteCloser, error) { return file, nil }
			}
			var out, stderr strings.Builder
			if code := agyCheckHook([]string{"allow", agyCheckHookTestDir}, &out, &stderr, h.d); code != 1 || out.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 || h.wait != 0 {
				t.Fatalf("log failure leaked decision: %d %q %q", code, out.String(), stderr.String())
			}
			if (kind == "write" || kind == "short write" || kind == "close") && !file.closed {
				t.Fatal("failed log not closed")
			}
		})
	}
}

type agyCheckHookObserver struct{ check func() }

func (w agyCheckHookObserver) Write(p []byte) (int, error) { w.check(); return len(p), nil }

func TestAgyCheckHookRecordsBeforeOutput(t *testing.T) {
	for _, mode := range agyCheckHookModes {
		t.Run(mode.name, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			outputStarted := false
			check := func() {
				f := h.files[agyCheckHookTestDir+"/calls/000001"]
				if f == nil || !f.closed || f.Len() == 0 {
					t.Error("output/sleep preceded completed call record")
				}
			}
			h.d.sleep = func(time.Duration) {
				check()
				if outputStarted {
					t.Error("hang emitted output before waiting")
				}
			}
			w := agyCheckHookObserver{func() { check(); outputStarted = true }}
			if code := agyCheckHook([]string{mode.name, agyCheckHookTestDir}, w, w, h.d); code != mode.code {
				t.Fatalf("unexpected code: %d", code)
			}
		})
	}
}

func TestAgyCheckHookOutputFailures(t *testing.T) {
	for _, err := range []error{nil, io.ErrClosedPipe} {
		h := agyCheckHookHarnessNew(t)
		var stderr strings.Builder
		if code := agyCheckHook([]string{"allow", agyCheckHookTestDir}, agyCheckBadWriter{err}, &stderr, h.d); code != 1 || !strings.Contains(stderr.String(), "writing hook output") {
			t.Fatalf("output failure lost: %d %q", code, stderr.String())
		}
	}
}

func agyCheckHookPrepared(t *testing.T) string {
	t.Helper()
	// macOS temporary paths can contain /var, which is a symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agyCheckHookSentinel), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func agyCheckHookFileDeps(stdin io.Reader) agyCheckHookDeps {
	d := agyCheckHookDefaults(stdin)
	// Use the real owner's equal uid pair. These tests must fail under root,
	// since the hook's nonzero process-identity condition still applies.
	d.uid = func() (int, int) { return os.Getuid(), os.Getuid() }
	return d
}

func TestAgyCheckHookConcurrent(t *testing.T) {
	dir := agyCheckHookPrepared(t)
	const workers, calls = 8, 7
	var wg, arrived sync.WaitGroup
	arrived.Add(workers)
	release := make(chan struct{})
	var mu sync.Mutex
	reached := 0
	go func() {
		arrived.Wait()
		close(release)
	}()
	for range workers {
		wg.Go(func() {
			var arrival sync.Once
			// A refusal before create must release the other workers without a timer.
			defer arrival.Do(arrived.Done)
			d := agyCheckHookFileDeps(strings.NewReader("{}"))
			create := d.create
			first := true
			d.create = func(path string, flags int, mode os.FileMode) (io.WriteCloser, error) {
				if first {
					first = false
					mu.Lock()
					reached++
					mu.Unlock()
					arrival.Do(arrived.Done)
					<-release
				}
				return create(path, flags, mode)
			}
			for range calls {
				var out, stderr strings.Builder
				if code := agyCheckHook([]string{"alternate", dir}, &out, &stderr, d); code != 0 || stderr.Len() != 0 {
					t.Errorf("concurrent hook: %d %q", code, stderr.String())
				}
			}
		})
	}
	wg.Wait()
	<-release
	if reached != workers {
		t.Fatalf("first-claim barrier reached by %d workers, want %d", reached, workers)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != workers*calls {
		t.Fatalf("claim gaps/duplicates: %d records, want %d", len(entries), workers*calls)
	}
	allows, denies := 0, 0
	for n, entry := range entries {
		n++
		if entry.Name() != fmt.Sprintf("%06d", n) {
			t.Fatalf("claim sequence has gap: %s at %d", entry.Name(), n)
		}
		data, err := os.ReadFile(filepath.Join(dir, "calls", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		word := "deny"
		if n%2 == 1 {
			word = "allow"
			allows++
		} else {
			denies++
		}
		if string(data) != word+" -\n" {
			t.Fatalf("claim %d parity: %q, want %s", n, data, word)
		}
		info, err := entry.Info()
		if err != nil || info.Mode() != 0o600 {
			t.Fatalf("call mode: %v %v", info, err)
		}
	}
	if allows-denies < -1 || allows-denies > 1 {
		t.Fatalf("unbalanced alternation: %d %d", allows, denies)
	}
}

func TestAgyCheckHookNoFollow(t *testing.T) {
	dir := agyCheckHookPrepared(t)
	calls := filepath.Join(dir, "calls")
	if err := os.Mkdir(calls, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "evidence")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(calls, "000001")); err != nil {
		t.Fatal(err)
	}
	var out, stderr strings.Builder
	if code := agyCheckHook([]string{"allow", dir}, &out, &stderr, agyCheckHookFileDeps(strings.NewReader(""))); code != 0 {
		t.Fatalf("existing claim not skipped: %d %q", code, stderr.String())
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("existing claim followed/truncated: %q %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(calls, "000002")); err != nil {
		t.Fatal(err)
	}
}

func TestAgyCheckHookDispatch(t *testing.T) {
	var out, stderr strings.Builder
	code := run([]string{"agy-check", "hook", "allow", agyCheckHookPrepared(t)}, &agyCheckHookReader{err: errors.New("dispatch input failure")}, &out, &stderr)
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "reading hook input: dispatch input failure") {
		t.Fatalf("hook dispatch lost stdin: %d %q %q", code, out.String(), stderr.String())
	}
}

func TestAgyCheckHookSymlinkedAncestor(t *testing.T) {
	dir := agyCheckHookPrepared(t)
	link := filepath.Join(agyCheckHookPrepared(t), "link")
	if err := os.Symlink(filepath.Dir(dir), link); err != nil {
		t.Fatal(err)
	}
	var out, stderr strings.Builder
	code := agyCheckHook([]string{"allow", filepath.Join(link, filepath.Base(dir))}, &out, &stderr, agyCheckHookFileDeps(strings.NewReader("")))
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "real directory") {
		t.Fatalf("symlinked ancestor accepted: %d %q %q", code, out.String(), stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != agyCheckHookSentinel {
		t.Fatalf("refused hook created files in target: %v", entries)
	}
}

func TestAgyCheckHookSharedTags(t *testing.T) {
	h := agyCheckHookHarnessNew(t)
	agyCheckHookRun(t, h, []string{"deny", agyCheckHookTestDir, "home"}, 0, "{\"decision\":\"deny\",\"reason\":\"agy-check fixture denial\"}\n", "")
	agyCheckHookRun(t, h, []string{"allow", agyCheckHookTestDir, "workspace"}, 0, "{\"decision\":\"allow\"}\n", "")
	if len(h.files) != 2 {
		t.Fatalf("shared hooks need two records: got %d", len(h.files))
	}
	for n, want := range []string{"deny home\n", "allow workspace\n"} {
		f := h.files[fmt.Sprintf("%s/calls/%06d", agyCheckHookTestDir, n+1)]
		if f == nil || !f.closed || f.String() != want {
			t.Fatalf("shared hook %d lost tag/decision: %+v; want %q", n+1, f, want)
		}
	}
}
