package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// browserPaused also covers an absent flag: provisioning never enables a check.
const browserPaused = "NOT VERIFIED: browser check paused"

// browserModePath validates trusted control input before it reaches configuration.
func browserModePath(path string) error {
	if !utf8.ValidString(path) || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsRune(path, '\'') || strings.ContainsFunc(path, unicode.IsControl) {
		return errors.New("browser-mode requires an absolute, clean, safely quotable non-root path")
	}
	return nil
}

// browserSwitchOn opens before checking identity, so replacement cannot bypass
// the checks. Missing or unreadable switches must never enable browser checks.
func browserSwitchOn(path string, uid int) bool {
	if browserModePath(path) != nil {
		return false
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), "browser-mode")
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(uid) || st.Mode&0o022 != 0 {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, 4))
	return err == nil && (string(data) == "on" || string(data) == "on\n")
}

// browserAdmission guards the internal runner; B3b exposes it. Read both files per call
// so a running MCP server observes a pause immediately, before locks or copies.
func (v containerVerifier) browserAdmission() string {
	if !browserSwitchOn(v.browserMode, v.browserSecurity.uid) {
		return browserPaused
	}
	return v.browserSecurity.admit(v.browserMode)
}

// browserBudgetLine changes only contention; other causes keep M3's noun.
func browserBudgetLine(result budgetResult) string {
	if result == budgetBusy {
		return result.line("browser check")
	}
	return result.line("verification budget")
}
