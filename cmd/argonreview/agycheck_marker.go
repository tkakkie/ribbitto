package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const agyCheckMarkerName = ".argonreview-agy-check.json"

// Version 1 is the preparation page's contract. Private paths (home, marker,
// isolation) belong to the dedicated user. Protected roots are only probed,
// never statted or opened; their ownership cannot be inspected by this tool.
// Each protected root is itself a canary directory, not a file within it.
type agyCheckMarker struct {
	Version     int      `json:"version"`
	User        string   `json:"user"`
	Home        string   `json:"home"`
	Isolation   string   `json:"isolation"`
	AccountHome string   `json:"account_home"`
	Repository  string   `json:"repository"`
	WorkerHomes []string `json:"worker_homes"`
	ReviewHome  string   `json:"review_home"`
}

func agyCheckCleanPath(path string) bool {
	return utf8.ValidString(path) && filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" &&
		!strings.ContainsFunc(path, unicode.IsControl) && !strings.Contains(path, "'")
}

func agyCheckSettingsPath(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if strings.EqualFold(part, "settings.json") {
			return true
		}
	}
	return false
}

func agyCheckOverlap(a, b string) bool {
	// Conservative folding also refuses case aliases on macOS without reading
	// any protected root to compare filesystem identities.
	a, b = strings.ToLower(a), strings.ToLower(b)
	return implementRunInside(a, b) || implementRunInside(b, a)
}

func agyCheckReadMarker(home string, d agyCheckDeps) (agyCheckMarker, error) {
	var m agyCheckMarker
	data, err := d.read(filepath.Join(home, agyCheckMarkerName), 16<<10)
	if err != nil {
		return m, fmt.Errorf("reading isolation marker: %w", err)
	}
	if !implementGuardUnicode(data) {
		return m, errors.New("decoding isolation marker: invalid Unicode")
	}
	fields, err := verifyObject(data)
	if err != nil {
		return m, fmt.Errorf("decoding isolation marker: %w", err)
	}
	if len(fields) != 8 {
		return m, errors.New("decoding isolation marker: unknown or missing fields")
	}
	// encoding/json otherwise accepts case aliases and null for scalar fields.
	for _, name := range []string{"version", "user", "home", "isolation", "account_home", "repository", "worker_homes", "review_home"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return m, fmt.Errorf("decoding isolation marker: missing or null %s", name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("decoding isolation marker: %w", err)
	}
	if m.Version != 1 {
		return m, errors.New("marker version does not match")
	}
	if m.User == "" {
		return m, errors.New("marker dedicated user is empty")
	}
	if m.Home != home {
		return m, errors.New("marker home does not match")
	}
	return m, nil
}

func (m agyCheckMarker) canaries() []string {
	return append([]string{m.AccountHome, m.Repository, m.ReviewHome}, m.WorkerHomes...)
}

func agyCheckValidate(o agyCheckOptions, d agyCheckDeps) (agyCheckMarker, error) {
	m, err := agyCheckReadMarker(o.home, d)
	if err != nil {
		return m, err
	}
	account, err := d.current()
	if err != nil {
		return m, fmt.Errorf("finding dedicated user: %w", err)
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	real, effective := d.ids()
	if uidErr != nil || uid <= 0 || real != uid || effective != uid || account.Username != m.User {
		return m, errors.New("process is not the marker's dedicated user")
	}
	self, err := d.executable()
	if err != nil {
		return m, fmt.Errorf("finding executable: %w", err)
	}
	if err := m.paths(o, self); err != nil {
		return m, err
	}
	for _, p := range []struct {
		path string
		mode os.FileMode
	}{{m.Home, os.ModeDir | 0o700}, {m.Isolation, os.ModeDir | 0o700}, {filepath.Join(m.Home, agyCheckMarkerName), 0o600}} {
		if err := agyCheckPrivate(p.path, p.mode, uid, d); err != nil {
			return m, err
		}
	}
	for _, path := range []string{self, o.agy} {
		if err := agyCheckExecutable(path, uid, d); err != nil {
			return m, err
		}
	}
	for _, path := range m.canaries() {
		// Check separately: failure of a combined R|W|X test proves only one
		// permission was absent. A missing canary is never evidence of isolation.
		for _, mode := range []uint32{4, 2, 1} { // R_OK, W_OK, X_OK on macOS and Linux.
			if !agyCheckAccessDenied(d.access(path, mode)) {
				return m, fmt.Errorf("canary access not denied: %q mode %d", path, mode)
			}
		}
	}
	return m, nil
}

func (m agyCheckMarker) paths(o agyCheckOptions, self string) error {
	for _, path := range append(m.canaries(), m.Home, m.Isolation, o.agy, self) {
		if !agyCheckCleanPath(path) {
			return errors.New("marker and executable paths must be absolute, clean and non-root")
		}
		if agyCheckSettingsPath(path) {
			return errors.New("marker and executable paths must not name settings files")
		}
	}
	home, isolation := strings.ToLower(m.Home), strings.ToLower(m.Isolation)
	if isolation == home || !implementRunInside(home, isolation) {
		return errors.New("isolation directory must be strictly inside the marked home")
	}
	for _, path := range m.canaries() {
		if agyCheckOverlap(m.Home, path) || agyCheckOverlap(o.agy, path) || agyCheckOverlap(self, path) {
			return errors.New("home or executable overlaps a protected root")
		}
	}
	return nil
}

func agyCheckRealPath(path string, d agyCheckDeps, check func(string, os.FileInfo) error) (os.FileInfo, error) {
	info, err := d.lstat(path)
	if err != nil {
		return nil, fmt.Errorf("examining private path: %w", err)
	}
	for current := path; ; current = filepath.Dir(current) {
		part, err := d.lstat(current)
		if err != nil {
			return nil, fmt.Errorf("examining path component: %w", err)
		}
		if part.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("private and executable paths must not contain symlinks")
		}
		if check != nil {
			if err := check(current, part); err != nil {
				return nil, err
			}
		}
		if current == filepath.Dir(current) {
			return info, nil
		}
	}
}

func agyCheckPrivate(path string, mode os.FileMode, uid int, d agyCheckDeps) error {
	info, err := agyCheckRealPath(path, d, nil)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || info.Mode() != mode {
		return fmt.Errorf("private path must have dedicated ownership and mode %v: %q", mode, path)
	}
	return nil
}

func agyCheckAccessDenied(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func agyCheckNotWritable(err error) bool {
	// macOS's sealed system volume reports EROFS even for access("/", W_OK).
	return agyCheckAccessDenied(err) || errors.Is(err, syscall.EROFS)
}

func agyCheckExecutable(path string, uid int, d agyCheckDeps) error {
	info, err := agyCheckRealPath(path, d, func(target string, info os.FileInfo) error {
		stat, ok := info.Sys().(*syscall.Stat_t)
		// An owner can chmod a read-only file or directory and replace the binary.
		if !ok || int(stat.Uid) == uid {
			return errors.New("trusted executable or its ancestor has dedicated ownership or unknown owner")
		}
		if !agyCheckNotWritable(d.access(target, 2)) {
			return errors.New("trusted executable or its directory is writable or uncheckable")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || d.access(path, 1) != nil {
		return errors.New("trusted executable must be a regular executable file")
	}
	return nil
}
