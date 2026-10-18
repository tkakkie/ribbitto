package main

import (
	"os"
	"strings"
	"testing"
)

func TestAgyEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, tmp string
		parent    []string
		want      map[string]string
	}{
		{"empty parent", "", nil, map[string]string{"HOME": "/review-home"}},
		{"check", "", []string{
			"HOME=/caller-home", "TMPDIR=/caller-tmp", "PATH=/bin", "LANG=C",
			"LC_ALL=C", "LC_MESSAGES=C", "LC_CUSTOM=custom", "USER=operator", "LOGNAME=operator",
			"GH_TOKEN=dummy", "RIBBITTO_DATABASE_URL=dummy", "AWS_SECRET_ACCESS_KEY=dummy",
			"HTTPS_PROXY=dummy", "SSL_CERT_FILE=dummy", "LC=excluded", "PATH_EXTRA=excluded", "malformed",
			"PWD=/caller-dir", "SHLVL=3", "_=/caller-executable",
		}, map[string]string{
			"HOME": "/review-home", "PATH": "/bin", "LANG": "C", "LC_ALL": "C",
			"LC_MESSAGES": "C", "LC_CUSTOM": "custom", "USER": "operator", "LOGNAME": "operator",
		}},
		{"review", "/workspace/tmp", []string{"HOME=/caller-home", "TMPDIR=/caller-tmp", "GH_TOKEN=dummy"},
			map[string]string{"HOME": "/review-home", "TMPDIR": "/workspace/tmp"}},
		{"empty values", "", []string{"PATH=", "LANG=", "LC_ALL=", "USER=", "LOGNAME="},
			map[string]string{"HOME": "/review-home", "PATH": "", "LANG": "", "LC_ALL": "", "USER": "", "LOGNAME": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertAgyEnvironment(t, agyEnvironment(tc.parent, "/review-home", tc.tmp), tc.want)
		})
	}
}

// testAgyParentEnvironment seeds both subprocess paths with dummy credentials
// and known allowed values. Existing LC_* variables are also allowed.
func testAgyParentEnvironment(t *testing.T) map[string]string {
	t.Helper()
	for _, name := range []string{"GH_TOKEN", "RIBBITTO_DATABASE_URL", "AWS_SECRET_ACCESS_KEY", "HTTPS_PROXY", "SSL_CERT_FILE", "LC", "PATH_EXTRA"} {
		t.Setenv(name, "dummy-excluded")
	}
	t.Setenv("HOME", "/caller-home")
	want := map[string]string{
		"PATH": os.Getenv("PATH"), "LANG": "C", "LC_ALL": "C", "LC_MESSAGES": "C",
		"USER": "test-operator", "LOGNAME": "test-operator",
	}
	for name, value := range want {
		t.Setenv(name, value)
	}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "LC_") {
			want[name] = value
		}
	}
	return want
}

func assertFakeAgyEnvironment(t *testing.T, path string, want map[string]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, entry := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		name, _, _ := strings.Cut(entry, "=")
		// /bin/sh may introduce these itself. TestAgyEnvironment checks the
		// exact environment before the fake's shell can add anything.
		if name != "PWD" && name != "SHLVL" && name != "_" {
			entries = append(entries, entry)
		}
	}
	assertAgyEnvironment(t, entries, want)
}

func assertAgyEnvironment(t *testing.T, entries []string, want map[string]string) {
	t.Helper()
	seen := make(map[string]bool)
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || seen[name] {
			t.Errorf("malformed or duplicate environment name %q", name)
		}
		seen[name] = true
		expected, allowed := want[name]
		if !allowed {
			// Print names only: a regression could expose real parent secrets.
			t.Errorf("unexpected environment name %q", name)
		} else if value != expected {
			t.Errorf("environment value for %s does not match", name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("missing environment name %q", name)
		}
	}
}
