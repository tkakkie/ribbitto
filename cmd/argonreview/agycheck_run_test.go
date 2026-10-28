package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type agyCheckInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i agyCheckInfo) Name() string       { return "fixture" }
func (i agyCheckInfo) Size() int64        { return 0 }
func (i agyCheckInfo) Mode() os.FileMode  { return i.mode }
func (i agyCheckInfo) ModTime() time.Time { return time.Time{} }
func (i agyCheckInfo) IsDir() bool        { return i.mode.IsDir() }
func (i agyCheckInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

type agyCheckHarness struct {
	m       agyCheckMarker
	o       agyCheckOptions
	d       agyCheckDeps
	info    map[string]agyCheckInfo
	statErr map[string]error
	probe   map[string]map[uint32]error
	data    string
	readErr error
	user    user.User
	real    int
	euid    int
}

func agyCheckFixtureCanaries(m agyCheckMarker) []string {
	return append([]string{m.AccountHome, m.Repository, m.ReviewHome}, m.WorkerHomes...)
}

func agyCheckNewHarness(t *testing.T) *agyCheckHarness {
	t.Helper()
	h := &agyCheckHarness{
		m:    agyCheckMarker{1, "dedicated", "/check/home", "/check/home/isolation", "/protected/account", "/protected/repository", []string{"/protected/worker", "/protected/worker-two"}, "/protected/review"},
		o:    agyCheckOptions{"/check/home", "/trusted-agy/agy", agyCheckModel, 30},
		info: map[string]agyCheckInfo{}, statErr: map[string]error{}, probe: map[string]map[uint32]error{},
		user: user.User{Uid: "501", Username: "dedicated"}, real: 501, euid: 501,
	}
	for _, path := range []string{h.m.Home, h.m.Isolation} {
		h.info[path] = agyCheckInfo{os.ModeDir | 0o700, 501}
	}
	h.info[filepath.Join(h.m.Home, agyCheckMarkerName)] = agyCheckInfo{0o600, 501}
	for _, path := range []string{"/trusted-agy/agy", "/trusted-tool/argonreview"} {
		h.info[path] = agyCheckInfo{0o555, 0}
	}
	guard := func(path string) {
		t.Helper()
		// No filesystem metadata or content operation may reach a protected
		// root, including any descendant. Only the access probe is exempt.
		for _, root := range agyCheckFixtureCanaries(h.m) {
			if implementRunInside(root, path) {
				t.Fatalf("forbidden filesystem access: %s", path)
			}
		}
	}
	h.d = agyCheckDeps{
		current:    func() (*user.User, error) { return &h.user, nil },
		ids:        func() (int, int) { return h.real, h.euid },
		executable: func() (string, error) { return "/trusted-tool/argonreview", nil },
		lstat: func(path string) (os.FileInfo, error) {
			guard(path)
			if err := h.statErr[path]; err != nil {
				return nil, err
			}
			if info, ok := h.info[path]; ok {
				return info, nil
			}
			return agyCheckInfo{os.ModeDir | 0o755, 0}, nil
		},
		read: func(path string, _ int64) ([]byte, error) {
			guard(path)
			if h.readErr != nil || h.data != "" {
				return []byte(h.data), h.readErr
			}
			if path != filepath.Join(h.o.home, agyCheckMarkerName) {
				t.Fatalf("unexpected content access: %s", path)
			}
			data, err := json.Marshal(h.m)
			return data, err
		},
		access: func(path string, mode uint32) error {
			if modes, ok := h.probe[path]; ok {
				if err, exists := modes[mode]; exists {
					return err
				}
			}
			for _, root := range agyCheckFixtureCanaries(h.m) {
				if path == root {
					return syscall.EACCES
				}
			}
			if mode == 1 {
				return nil
			}
			return syscall.EACCES
		},
	}
	return h
}

func TestAgyCheckArguments(t *testing.T) {
	base := []string{"--home", "/check/home", "--agy", "/trusted-agy/agy"}
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"default", base, 30}, {"override", append(append([]string{}, base...), "--calls", "41"), 41},
		{"missing", nil, 0}, {"unknown", append(append([]string{}, base...), "--unknown", "x"), 0},
		{"missing home", []string{"--agy", "/agy"}, 0}, {"missing agy", []string{"--home", "/home"}, 0},
		{"no value", append(append([]string{}, base...), "--calls"), 0},
		{"relative", []string{"--home", "home", "--agy", "/agy"}, 0},
		{"unclean", []string{"--home", "/a/../home", "--agy", "/agy"}, 0},
		{"relative agy", []string{"--home", "/home", "--agy", "agy"}, 0},
		{"unclean agy", []string{"--home", "/home", "--agy", "/a/../agy"}, 0},
		{"settings as home", []string{"--home", "/settings.json", "--agy", "/agy"}, 0},
		{"settings as agy", []string{"--home", "/home", "--agy", "/settings.json"}, 0},
		{"settings ancestor", []string{"--home", "/settings.json/child", "--agy", "/agy"}, 0},
		{"empty model", append(append([]string{}, base...), "--model", ""), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := agyCheckArguments(tc.args)
			if (err == nil) != (tc.want > 0) || (err == nil && (o.calls != tc.want || o.model != argonModel)) {
				t.Fatalf("calls/default model: %+v, %v; want %d", o, err, tc.want)
			}
			if tc.want == 0 {
				var out, usage strings.Builder
				if code := agyCheckCommand(tc.args, &out, &usage, agyCheckDeps{}); code != 2 || !strings.HasPrefix(out.String(), "USAGE:") || !strings.Contains(usage.String(), agyCheckRunUsage) {
					t.Fatalf("usage did not go through core: code=%d %q %q", code, out.String(), usage.String())
				}
			}
		})
	}
	for _, tc := range []struct {
		flag, valid string
		invalid     []string
	}{
		{"home", "/check/home", []string{"", "home", "/a/../home", "/", "/settings.json"}},
		{"agy", "/trusted-agy/agy", []string{"", "agy", "/a/../agy", "/", "/settings.json"}},
		{"calls", "30", []string{"", "0", "-1", "nope", "99999999999999999999999999999"}},
		{"model", agyCheckModel, []string{"", " ", "\t"}},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			var prefix []string
			for i := 0; i < len(base); i += 2 {
				if base[i] != "--"+tc.flag {
					prefix = append(prefix, base[i:i+2]...)
				}
			}
			args := append(prefix, "--"+tc.flag, tc.valid)
			if _, err := agyCheckArguments(args); err != nil {
				t.Fatalf("valid %s refused: %v", tc.flag, err)
			}
			for _, value := range tc.invalid {
				args[len(args)-1] = value
				if _, err := agyCheckArguments(args); err == nil {
					t.Fatalf("accepted invalid %s %q", tc.flag, value)
				}
			}
			if _, err := agyCheckArguments(args[:len(args)-1]); err == nil {
				t.Fatalf("accepted missing value for %s", tc.flag)
			}
			args[len(args)-1] = tc.valid
			args = append(args, "--"+tc.flag, tc.valid)
			if _, err := agyCheckArguments(args); err == nil {
				t.Fatalf("accepted repeated %s", tc.flag)
			}
		})
	}
}

func TestAgyCheckRefusals(t *testing.T) {
	withExtra := func(h *agyCheckHarness, extra string) {
		data, err := json.Marshal(h.m)
		if err != nil {
			t.Fatal(err)
		}
		h.data = strings.TrimSuffix(string(data), "}") + "," + extra + "}"
	}
	for _, tc := range []struct {
		name, reason string
		change       func(*agyCheckHarness)
	}{
		{"missing", "reading isolation marker", func(h *agyCheckHarness) { h.readErr = os.ErrNotExist }},
		{"unreadable", "reading isolation marker", func(h *agyCheckHarness) { h.readErr = os.ErrPermission }},
		{"malformed", "decoding isolation marker", func(h *agyCheckHarness) { h.data = "{" }},
		{"unknown field", "decoding isolation marker", func(h *agyCheckHarness) { withExtra(h, `"unexpected":1`) }},
		{"duplicate", "decoding isolation marker", func(h *agyCheckHarness) { withExtra(h, `"version":1`) }},
		{"trailing", "decoding isolation marker", func(h *agyCheckHarness) { data, _ := json.Marshal(h.m); h.data = string(data) + `{}` }},
		{"version", "marker version does not match", func(h *agyCheckHarness) { h.m.Version = 2 }},
		{"home", "marker home does not match", func(h *agyCheckHarness) {
			h.m.Home, h.m.Isolation = "/other", "/other/isolation"
			h.info[h.m.Home], h.info[h.m.Isolation] = agyCheckInfo{os.ModeDir | 0o700, 501}, agyCheckInfo{os.ModeDir | 0o700, 501}
			h.info[filepath.Join(h.m.Home, agyCheckMarkerName)] = agyCheckInfo{0o600, 501}
		}},
		{"empty user", "marker dedicated user is empty", func(h *agyCheckHarness) { h.m.User, h.user.Username = "", "" }},
		{"user mismatch", "dedicated user", func(h *agyCheckHarness) { h.user.Username = "other" }},
		{"real uid", "dedicated user", func(h *agyCheckHarness) { h.real = 502 }},
		{"effective uid", "dedicated user", func(h *agyCheckHarness) { h.euid = 502 }},
		{"uid malformed", "dedicated user", func(h *agyCheckHarness) { h.user.Uid = "bad" }},
		{"root", "dedicated user", func(h *agyCheckHarness) {
			h.user.Uid, h.real, h.euid = "0", 0, 0
			for path, info := range h.info {
				info.uid = 0
				h.info[path] = info
			}
		}},
		{"user lookup", "finding dedicated user", func(h *agyCheckHarness) { h.d.current = func() (*user.User, error) { return nil, os.ErrPermission } }},
		{"exe lookup", "finding executable", func(h *agyCheckHarness) { h.d.executable = func() (string, error) { return "", os.ErrPermission } }},
		{"isolation outside", "strictly inside", func(h *agyCheckHarness) {
			h.m.Isolation = "/elsewhere"
			h.info[h.m.Isolation] = agyCheckInfo{os.ModeDir | 0o700, 501}
		}},
		{"isolation equals home", "strictly inside", func(h *agyCheckHarness) { h.m.Isolation = h.m.Home }},
		{"missing account", "paths must", func(h *agyCheckHarness) { h.m.AccountHome = "" }},
		{"missing repository", "paths must", func(h *agyCheckHarness) { h.m.Repository = "" }},
		{"missing review", "paths must", func(h *agyCheckHarness) { h.m.ReviewHome = "" }},
		{"missing workers", "null worker_homes", func(h *agyCheckHarness) { h.m.WorkerHomes = nil }},
		{"unclean marker", "paths must", func(h *agyCheckHarness) { h.m.Isolation += "/.." }},
		{"settings filename", "settings files", func(h *agyCheckHarness) { h.m.WorkerHomes = []string{"/protected/settings.json"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := agyCheckNewHarness(t)
			tc.change(h)
			agyCheckWantRefusal(t, h, tc.reason)
		})
	}
}

func TestAgyCheckMarkerUnicode(t *testing.T) {
	for _, invalid := range []string{"\"\xff\"", `"\ud800"`} {
		h := agyCheckNewHarness(t)
		data, err := json.Marshal(h.m)
		if err != nil {
			t.Fatal(err)
		}
		h.data = strings.Replace(string(data), `"dedicated"`, invalid, 1)
		if _, err := agyCheckReadMarker(h.o.home, h.d); err == nil || !strings.Contains(err.Error(), "invalid Unicode") {
			t.Fatalf("invalid Unicode marker accepted or repaired: %v", err)
		}
	}
}

func agyCheckWantRefusal(t *testing.T, h *agyCheckHarness, reason string) {
	t.Helper()
	_, err := agyCheckValidate(h.o, h.d)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("missing refusal %q: %v", reason, err)
	}
	var out, stderr strings.Builder
	code := agyCheckCommand([]string{"--home", h.o.home, "--agy", h.o.agy}, &out, &stderr, h.d)
	if code != 1 || !strings.Contains(out.String(), reason) || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("command lost refusal %q: %d %q", reason, code, out.String())
	}
}

func TestAgyCheckPrivatePredicates(t *testing.T) {
	for _, path := range []string{"/check/home", "/check/home/isolation", "/check/home/" + agyCheckMarkerName} {
		for _, kind := range []string{"owner", "mode", "type", "symlink", "missing"} {
			t.Run(filepath.Base(path)+"/"+kind, func(t *testing.T) {
				h := agyCheckNewHarness(t)
				i := h.info[path]
				reason := "dedicated ownership"
				switch kind {
				case "owner":
					i.uid++
				case "mode":
					i.mode |= 0o020
				case "type":
					i.mode ^= os.ModeDir
				case "symlink":
					i.mode |= os.ModeSymlink
					reason = "symlinks"
				case "missing":
					h.statErr[path] = os.ErrNotExist
					reason = "examining private path"
				}
				h.info[path] = i
				agyCheckWantRefusal(t, h, reason)
			})
		}
	}
}

func TestAgyCheckProtectedPredicates(t *testing.T) {
	for index := range 5 {
		for _, relation := range []string{"equals", "inside", "contains", "case alias", "agy", "self"} {
			t.Run(string(rune('0'+index))+"/"+relation, func(t *testing.T) {
				h := agyCheckNewHarness(t)
				path := h.m.Home
				switch relation {
				case "inside":
					path = filepath.Dir(path)
				case "contains":
					path += "/nested"
				case "case alias":
					path = strings.ToUpper(path)
				case "agy":
					path = h.o.agy
				case "self":
					path = "/trusted-tool/argonreview"
				}
				roots := []*string{&h.m.AccountHome, &h.m.Repository, &h.m.ReviewHome, &h.m.WorkerHomes[0], &h.m.WorkerHomes[1]}
				*roots[index] = path
				// Bootstrap reads only the requested home's marker before the
				// declarations are known; separation must precede metadata reads.
				h.d.read = func(string, int64) ([]byte, error) { return json.Marshal(h.m) }
				agyCheckWantRefusal(t, h, "overlaps a protected root")
			})
		}
	}
}

func TestAgyCheckCanaries(t *testing.T) {
	for _, path := range agyCheckFixtureCanaries(agyCheckNewHarness(t).m) {
		for _, mode := range []uint32{4, 2, 1} {
			for _, outcome := range []error{syscall.EACCES, syscall.EPERM, syscall.EROFS, syscall.ENOENT, syscall.ELOOP, nil, syscall.EIO} {
				t.Run(filepath.Base(path)+"/"+string(rune('0'+mode))+"/"+fmt.Sprint(outcome), func(t *testing.T) {
					h := agyCheckNewHarness(t)
					h.probe[path] = map[uint32]error{mode: outcome}
					_, err := agyCheckValidate(h.o, h.d)
					want := errors.Is(outcome, syscall.EACCES) || errors.Is(outcome, syscall.EPERM)
					if (err == nil) != want {
						t.Fatalf("canary %s mode %d outcome %v accepted=%t: %v", path, mode, outcome, want, err)
					}
				})
			}
		}
	}
}

func TestAgyCheckExecutablePredicates(t *testing.T) {
	for _, path := range []string{"/trusted-agy/agy", "/trusted-tool/argonreview"} {
		for _, kind := range []string{"writable file", "writable directory", "writable ancestor", "nonregular", "no execute bits", "no execute access", "symlink", "parent symlink", "access error", "setuid", "setgid"} {
			t.Run(filepath.Base(path)+"/"+kind, func(t *testing.T) {
				h := agyCheckNewHarness(t)
				switch kind {
				case "writable file":
					h.probe[path] = map[uint32]error{2: nil}
				case "writable directory":
					h.probe[filepath.Dir(path)] = map[uint32]error{2: nil}
				case "writable ancestor":
					h.probe["/"] = map[uint32]error{2: nil}
				case "nonregular":
					h.info[path] = agyCheckInfo{os.ModeDir | 0o555, 0}
				case "no execute bits":
					h.info[path] = agyCheckInfo{0o444, 0}
				case "no execute access":
					h.probe[path] = map[uint32]error{1: syscall.EACCES}
				case "symlink":
					h.info[path] = agyCheckInfo{os.ModeSymlink | 0o555, 0}
				case "parent symlink":
					h.info[filepath.Dir(path)] = agyCheckInfo{os.ModeSymlink | 0o755, 0}
				case "access error":
					h.probe[path] = map[uint32]error{2: syscall.ENOENT}
				case "setuid":
					h.info[path] = agyCheckInfo{os.ModeSetuid | 0o555, 0}
				case "setgid":
					h.info[path] = agyCheckInfo{os.ModeSetgid | 0o555, 0}
				}
				if _, err := agyCheckValidate(h.o, h.d); err == nil {
					t.Fatal("unsafe executable accepted")
				}
			})
		}
	}
}

func TestAgyCheckExecutableWriteAccess(t *testing.T) {
	for _, target := range []string{"/trusted-agy/agy", "/trusted-agy", "/"} {
		for _, outcome := range []error{syscall.EACCES, syscall.EPERM, syscall.EROFS, nil, syscall.ENOENT, syscall.ELOOP} {
			t.Run(target+"/"+fmt.Sprint(outcome), func(t *testing.T) {
				h := agyCheckNewHarness(t)
				h.probe[target] = map[uint32]error{2: outcome}
				_, err := agyCheckValidate(h.o, h.d)
				want := outcome == syscall.EACCES || outcome == syscall.EPERM || outcome == syscall.EROFS
				if (err == nil) != want {
					t.Fatalf("write access %s outcome %v accepted=%t: %v", target, outcome, want, err)
				}
			})
		}
	}
}

func TestAgyCheckRootWriteAccess(t *testing.T) {
	err := syscall.Access("/", 2)
	if !agyCheckNotWritable(err) && (err != nil || os.Geteuid() != 0) {
		t.Fatalf("root write access unclassified: uid=%d error=%v", os.Geteuid(), err)
	}
	if agyCheckAccessDenied(syscall.EROFS) {
		t.Fatal("EROFS must not count as canary access denial")
	}
}

func TestAgyCheckExecutableOwnership(t *testing.T) {
	for _, path := range []string{"/trusted-agy/agy", "/trusted-tool/argonreview"} {
		for _, target := range []string{path, filepath.Dir(path), "/", ""} {
			t.Run(path+"/owner="+target, func(t *testing.T) {
				h := agyCheckNewHarness(t)
				for _, p := range []string{path, filepath.Dir(path), "/"} {
					mode := os.ModeDir | 0o555
					if p == path {
						mode = 0o555
					}
					uid := uint32(502)
					if p == target {
						uid = 501
					}
					h.info[p] = agyCheckInfo{mode, uid}
				}
				_, err := agyCheckValidate(h.o, h.d)
				if target == "" {
					if err != nil {
						t.Fatalf("other owners refused: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "dedicated ownership") {
					t.Fatalf("dedicated owner %s not refused: %v", target, err)
				}
			})
		}
	}
}

func TestAgyCheckIsolationCaseFold(t *testing.T) {
	for _, tc := range []struct {
		isolation string
		accepted  bool
	}{
		{"/check/HOME", false},
		{"/CHECK/home/isolation", true},
		{"/CHECK/home-other/isolation", false},
	} {
		t.Run(tc.isolation, func(t *testing.T) {
			h := agyCheckNewHarness(t)
			h.m.Isolation = tc.isolation
			h.info[tc.isolation] = agyCheckInfo{os.ModeDir | 0o700, 501}
			_, err := agyCheckValidate(h.o, h.d)
			if (err == nil) != tc.accepted || (err != nil && !strings.Contains(err.Error(), "strictly inside")) {
				t.Fatalf("isolation %s accepted=%t: %v", tc.isolation, tc.accepted, err)
			}
		})
	}
}

func TestAgyCheckCommandFailsClosed(t *testing.T) {
	h := agyCheckNewHarness(t)
	var out, stderr strings.Builder
	code := agyCheckCommand([]string{"--home", h.o.home, "--agy", h.o.agy}, &out, &stderr, h.d)
	if code != 1 || out.String() != "RESULT INVALID: row execution is not implemented yet\n" || stderr.Len() != 0 {
		t.Fatalf("part A must refuse without running agy: %d %q %q", code, out.String(), stderr.String())
	}
	for _, args := range [][]string{{"agy-check"}, {"agy-check", "hook", "allow", filepath.Join(t.TempDir(), "unprepared")}} {
		out.Reset()
		stderr.Reset()
		code := run(args, strings.NewReader(""), &out, &stderr)
		if len(args) > 1 {
			if code != 1 || out.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("unprepared hook did not fail closed: %d %q %q", code, out.String(), stderr.String())
			}
		} else if code != 2 || !strings.Contains(out.String(), "USAGE:") {
			t.Fatalf("dispatch/hook form did not fail closed: %d %q", code, out.String())
		}
	}
}

func TestAgyCheckCommandRevalidatesMarker(t *testing.T) {
	h := agyCheckNewHarness(t)
	var out, stderr strings.Builder
	args := []string{"--home", h.o.home, "--agy", h.o.agy}
	if code := agyCheckCommand(args, &out, &stderr, h.d); code != 1 || !strings.Contains(out.String(), "row execution is not implemented yet") {
		t.Fatalf("initial validation failed: %d %q", code, out.String())
	}
	h.m.AccountHome = "/protected/new-account"
	h.probe[h.m.AccountHome] = map[uint32]error{4: nil}
	out.Reset()
	if code := agyCheckCommand(args, &out, &stderr, h.d); code != 1 || !strings.Contains(out.String(), "canary access not denied") || !strings.Contains(out.String(), h.m.AccountHome) {
		t.Fatalf("new marker root not revalidated: %d %q", code, out.String())
	}
}
