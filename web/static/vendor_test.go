package static_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestVendoredJavaScript(t *testing.T) {
	hashes := map[string]string{
		"htmx-2.0.7.min.js":          "60231ae6ba9db3825eb15a261122d5f55921c4d53b66bf637dc18b4ee27c79f9",
		"htmx-ext-sse-2.2.4.min.js":  "98a46496de0c3605fbffdce9167ba427bdd9553184f83f149c261891a92c0136",
		"idiomorph-ext-0.7.4.min.js": "a6437e55b1b6a07bc421f0d230266a39399b6826c6ed19e0ed9c63b707444a5f",
	}
	files := os.DirFS(".")
	for _, finding := range checkVendorJavaScript(files, hashes) {
		t.Error(finding)
	}
	for _, finding := range checkVendorREADME(files, hashes) {
		t.Error(finding)
	}
}

func checkVendorJavaScript(files fs.FS, hashes map[string]string) []string {
	names, err := fs.Glob(files, "vendor/*.js")
	if err != nil {
		return []string{fmt.Sprintf("listing vendored JavaScript: %v", err)}
	}
	var findings []string
	for _, name := range names {
		if _, ok := hashes[strings.TrimPrefix(name, "vendor/")]; !ok {
			findings = append(findings, fmt.Sprintf("%s: no pinned SHA-256", name))
		}
	}
	// Reading every pin also catches a removed file, which a glob alone cannot.
	for name, want := range hashes {
		data, err := fs.ReadFile(files, "vendor/"+name)
		if err != nil {
			findings = append(findings, fmt.Sprintf("reading vendor/%s: %v", name, err))
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			findings = append(findings, fmt.Sprintf("vendor/%s: SHA-256 mismatch: got %s, want %s", name, got, want))
		}
	}
	return findings
}

func checkVendorREADME(files fs.FS, hashes map[string]string) []string {
	data, err := fs.ReadFile(files, "vendor/README.md")
	if err != nil {
		return []string{fmt.Sprintf("reading vendor/README.md: %v", err)}
	}
	var section string
	documented := map[string][]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if name, ok := strings.CutPrefix(line, "## "); ok {
			section = strings.Trim(strings.TrimSpace(name), "`")
		}
		if hash, ok := strings.CutPrefix(line, "- SHA-256: "); ok {
			documented[section] = append(documented[section], strings.Trim(strings.TrimSpace(hash), "`"))
		}
	}
	var findings []string
	for name, want := range hashes {
		// Require one hash in its own section so missing or duplicate lines fail.
		if got := documented[name]; len(got) != 1 || got[0] != want {
			findings = append(findings, fmt.Sprintf("vendor/README.md: %s SHA-256 mismatch: got %v, want %s", name, got, want))
		}
	}
	return findings
}

func TestVendorChecks(t *testing.T) {
	const hash = "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"
	const readme = "## `example.js`\n\n- SHA-256: `" + hash + "`\n\n## `sprite.svg`\n\n- SHA-256: `source tarball hash`\n"
	for _, tt := range []struct {
		name   string
		mutate func(fstest.MapFS)
		wantJS string
		wantMD string
	}{
		{
			name: "unchanged",
		},
		{
			name: "one changed byte",
			mutate: func(files fstest.MapFS) {
				files["vendor/example.js"].Data[0] = 'b'
			},
			wantJS: "vendor/example.js: SHA-256 mismatch",
		},
		{
			name: "unpinned extra file",
			mutate: func(files fstest.MapFS) {
				files["vendor/extra.js"] = &fstest.MapFile{Data: []byte("extra")}
			},
			wantJS: "vendor/extra.js: no pinned SHA-256",
		},
		{
			name: "missing pinned file",
			mutate: func(files fstest.MapFS) {
				delete(files, "vendor/example.js")
			},
			wantJS: "reading vendor/example.js:",
		},
		{
			name: "README hash differs",
			mutate: func(files fstest.MapFS) {
				files["vendor/README.md"].Data = []byte(strings.ReplaceAll(readme, hash, strings.Repeat("0", 64)))
			},
			wantMD: "vendor/README.md: example.js SHA-256 mismatch",
		},
		{
			name: "README hash in wrong section",
			mutate: func(files fstest.MapFS) {
				files["vendor/README.md"].Data = []byte(strings.ReplaceAll(readme, "example.js", "other.js"))
			},
			wantMD: "vendor/README.md: example.js SHA-256 mismatch",
		},
		{
			name: "missing README hash",
			mutate: func(files fstest.MapFS) {
				files["vendor/README.md"].Data = []byte("## `example.js`\n")
			},
			wantMD: "vendor/README.md: example.js SHA-256 mismatch",
		},
		{
			name: "duplicate README hash",
			mutate: func(files fstest.MapFS) {
				files["vendor/README.md"].Data = []byte(readme + "\n## `example.js`\n\n- SHA-256: `" + hash + "`\n")
			},
			wantMD: "vendor/README.md: example.js SHA-256 mismatch",
		},
		{
			name: "missing README",
			mutate: func(files fstest.MapFS) {
				delete(files, "vendor/README.md")
			},
			wantMD: "reading vendor/README.md:",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := fstest.MapFS{
				"vendor/example.js": &fstest.MapFile{Data: []byte("a")},
				"vendor/README.md":  &fstest.MapFile{Data: []byte(readme)},
				"vendor/sprite.svg": &fstest.MapFile{Data: []byte("sprite")},
				"application.js":    &fstest.MapFile{Data: []byte("application")},
			}
			if tt.mutate != nil {
				tt.mutate(files)
			}
			hashes := map[string]string{"example.js": hash}
			for _, check := range []struct {
				name     string
				findings []string
				want     string
			}{
				{"JavaScript", checkVendorJavaScript(files, hashes), tt.wantJS},
				{"README", checkVendorREADME(files, hashes), tt.wantMD},
			} {
				if check.want == "" {
					if len(check.findings) != 0 {
						t.Errorf("%s: unexpected findings: %v", check.name, check.findings)
					}
				} else if len(check.findings) != 1 || !strings.Contains(check.findings[0], check.want) {
					t.Errorf("%s: got %v, want one finding containing %q", check.name, check.findings, check.want)
				}
			}
		})
	}
}
