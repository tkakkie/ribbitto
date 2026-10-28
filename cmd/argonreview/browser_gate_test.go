package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// browserGateFixture supplies a fresh entry without writing it, for missing rows.
func browserGateFixture(t *testing.T) (browserSecurity, string, browserSecurityEntry) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "browser-mode")
	browserWrite(t, path, "on", 0o400)
	b := newBrowserSecurity()
	b.path, b.now = filepath.Join(dir, "browser-security-check"), func() time.Time { return now }
	return b, path, browserSecurityEntry{path, now.Add(-time.Hour), "https://github.com/tkakkie/ribbitto/issues/1908#issuecomment-123"}
}

func TestBrowserRecordURL(t *testing.T) {
	const base = "https://github.com/tkakkie/ribbitto/issues/"
	for _, value := range []string{"1", "0001", strings.Repeat("9", 20), "1#issuecomment-0", "1#issuecomment-" + strings.Repeat("9", 20), "empty", "", "1\n", "1\n2", "1 ", "1\r", "1\x00", "1x", "1#issuecomment-", "1#issuecomment-1#issuecomment-2", "1/", "1?a=b", "/1", "١", "１", "1#issuecomment-١", strings.Repeat("1", 21), "1#issuecomment-" + strings.Repeat("1", 21), "xhttps://github.com/tkakkie/ribbitto/issues/1", "http://github.com/tkakkie/ribbitto/issues/1", "https://GITHUB.COM/tkakkie/ribbitto/issues/1", "https://github.com/tkakkie/ribbitto2/issues/1", "https://github.com/tkakkie/Ribbitto/issues/1", "https://github.com/tkakkie/ribbitto/pull/1", "https://x@github.com/tkakkie/ribbitto/issues/1"} {
		url := base + value
		if strings.Contains(value, "://") {
			url = value
		}
		if value == "empty" {
			url = ""
		}
		want := value == "1" || value == "0001" || value == strings.Repeat("9", 20) || value == "1#issuecomment-0" || value == "1#issuecomment-"+strings.Repeat("9", 20)
		t.Run(value, func(t *testing.T) {
			if got := browserRecordURL(url); got != want {
				t.Fatalf("URL accepted=%v want=%v for %q", got, want, url)
			}
			b, _, entry := browserGateFixture(t)
			entry.record = url
			browserWrite(t, b.path, string(entry.bytes()), 0o600)
			parent, err := b.parent()
			if err != nil {
				t.Fatal(err)
			}
			defer closeFD(parent)
			if _, err := b.readAt(parent); (err == nil) != want {
				t.Fatalf("gate URL accepted=%v want=%v", err == nil, want)
			}
			code := 2
			if want {
				code = 0
			}
			log := browserRecordCall(t, b, browserRecordArgs(entry, false), code)
			if !want && log != "browser-check-record: invalid completion, reference or switch path\n" {
				t.Fatalf("URL refusal=%q", log)
			}
		})
	}
}

// TestBrowserSecurityGate checks every malformed field and unsafe identity alone.
func TestBrowserSecurityGate(t *testing.T) {
	for _, kind := range []string{"valid", "missing", "switch-relative", "switch-unclean", "switch-root", "switch-control", "switch-UTF8", "switch-quote", "completed-invalid", "completed-offset", "completed-lowercase", "completed-noncanonical", "completed-hour", "record-http", "record-host", "record-repo", "record-empty-number", "record-comment-empty", "record-query", "record-suffix", "wrong-order", "extra-line", "no-newline", "NUL", "future", "14d-minus-1ns", "14d", "path-differs", "no-runner-flag", "symlink", "mode", "two-links", "overlong", "unsafe-parent", "parent-symlink", "parent-owner", "owner", "directory", "switch-key", "completed-key", "record-key"} {
		t.Run(kind, func(t *testing.T) {
			b, path, entry := browserGateFixture(t)
			want := browserOverdue
			switch kind {
			case "valid", "14d-minus-1ns":
				want = ""
			case "switch-relative":
				entry.switchPath = "relative"
			case "switch-unclean":
				entry.switchPath = "/a/../b"
			case "switch-root":
				entry.switchPath = "/"
			case "switch-control":
				entry.switchPath = "/a\tb"
			case "switch-UTF8":
				entry.switchPath = "/a\xffb"
			case "switch-quote":
				entry.switchPath = "/a'b"
			case "future":
				entry.completed = b.now().Add(time.Nanosecond)
			case "14d":
				entry.completed = b.now().Add(-browserSecurityAge)
			case "path-differs":
				entry.switchPath = path + "-other"
			case "no-runner-flag":
				path = ""
			}
			if strings.HasPrefix(kind, "switch-") && kind != "switch-key" {
				// Match the raw runner value here to isolate malformed-file parsing from
				// the separate equality check; host flag parsing rejects these values too.
				path = entry.switchPath
			}
			if kind == "14d-minus-1ns" {
				entry.completed = b.now().Add(-browserSecurityAge + time.Nanosecond)
			}
			data := string(entry.bytes())
			switch kind {
			case "completed-invalid":
				data = strings.Replace(data, entry.completed.Format(time.RFC3339Nano), "garbage", 1)
			case "completed-offset":
				data = strings.Replace(data, "Z\nrecord=", "+00:00\nrecord=", 1)
			case "completed-lowercase":
				data = strings.Replace(data, "Z\nrecord=", "z\nrecord=", 1)
			case "completed-noncanonical":
				data = strings.Replace(data, "Z\nrecord=", ".000Z\nrecord=", 1)
			case "completed-hour":
				data = strings.Replace(data, "T11:", "T1:", 1)
			case "record-http":
				data = strings.Replace(data, "https:", "http:", 1)
			case "record-host":
				data = strings.Replace(data, "github.com", "github.com.evil.invalid", 1)
			case "record-repo":
				data = strings.Replace(data, "ribbitto/issues", "other/issues", 1)
			case "record-empty-number":
				data = strings.Replace(data, "1908#", "#", 1)
			case "record-comment-empty":
				data = strings.Replace(data, "comment-123", "comment-", 1)
			case "record-query":
				data = strings.Replace(data, "1908#issuecomment-123", "1908?x=1", 1)
			case "record-suffix":
				data = strings.Replace(data, "comment-123", "comment-123/", 1)
			case "wrong-order":
				lines := strings.Split(data, "\n")
				lines[0], lines[1] = lines[1], lines[0]
				data = strings.Join(lines, "\n")
			case "extra-line":
				data += "extra\n"
			case "no-newline":
				data = strings.TrimSuffix(data, "\n")
			case "NUL":
				data += "\x00"
			case "overlong":
				data = strings.Replace(data, "1908#issuecomment-123", strings.Repeat("1", 2048), 1)
			case "switch-key":
				data = strings.Replace(data, "switch=", "Switch=", 1)
			case "completed-key":
				data = strings.Replace(data, "completed=", "Completed=", 1)
			case "record-key":
				data = strings.Replace(data, "record=", "Record=", 1)
			}
			browserWrite(t, b.path, data, 0o600)
			switch kind {
			case "missing":
				browserRemove(t, b.path)
			case "symlink":
				target := b.path + "-real"
				if err := os.Rename(b.path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, b.path); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(b.path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "two-links":
				if err := os.Link(b.path, b.path+"-link"); err != nil {
					t.Fatal(err)
				}
			case "unsafe-parent":
				if err := os.Chmod(filepath.Dir(b.path), 0o720); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				link := filepath.Join(t.TempDir(), "parent")
				if err := os.Symlink(filepath.Dir(b.path), link); err != nil {
					t.Fatal(err)
				}
				b.path = filepath.Join(link, filepath.Base(b.path))
			case "parent-owner":
				b.uid++
			case "owner":
				// Isolate the file owner check from the directory check with the fd seam.
				parent, err := b.parent()
				if err != nil {
					t.Fatal(err)
				}
				defer closeFD(parent)
				b.uid++
				if _, err := b.readAt(parent); err == nil {
					t.Fatal("wrong gate file owner accepted")
				}
				return
			case "directory":
				browserRemove(t, b.path)
				if err := os.Mkdir(b.path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if got := b.admit(path); got != want {
				t.Fatalf("gate line=%q, want %q", got, want)
			}
		})
	}
	t.Run("switch-paused-beats-overdue", func(t *testing.T) {
		b, path, _ := browserGateFixture(t)
		browserWrite(t, path, "off", 0o400)
		v := newContainerVerifier("unused", path)
		v.browserSecurity = b
		if got := v.browserAdmission(); got != browserPaused {
			t.Fatalf("paused switch lost precedence: %q", got)
		}
		browserWrite(t, path, "on", 0o400)
		if got := v.browserAdmission(); got != browserOverdue {
			t.Fatalf("gate bypassed after switch enabled: %q", got)
		}
	})
	t.Run("gate-reread", func(t *testing.T) {
		b, path, entry := browserGateFixture(t)
		v := newContainerVerifier("unused", path)
		v.browserSecurity = b
		browserWrite(t, b.path, string(entry.bytes()), 0o600)
		if got := v.browserAdmission(); got != "" {
			t.Fatalf("fresh gate refused: %q", got)
		}
		entry.completed = b.now().Add(-browserSecurityAge)
		browserWrite(t, b.path, string(entry.bytes()), 0o600)
		if got := v.browserAdmission(); got != browserOverdue {
			t.Fatalf("expired gate cached: %q", got)
		}
	})
}
