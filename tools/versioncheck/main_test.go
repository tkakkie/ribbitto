package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDigest = "sha256:3a4264520e121176619f54ff867d12774bc81e95e7b84db58d63bccdb6144b81"

func TestVersionCheck(t *testing.T) {
	dep, err := os.ReadFile("../../.github/dependabot.yml")
	wf, wfErr := os.ReadFile("../../.github/workflows/version-coverage.yml")
	if err != nil || wfErr != nil || strings.Contains(string(wf), "pull_request") {
		t.Fatalf("reading configs: %v / %v", err, wfErr)
	}
	for _, want := range []string{"package-ecosystem: gomod", "package-ecosystem: github-actions", "package-ecosystem: npm", "package-ecosystem: docker-compose", "default-days: 7"} {
		if !bytes.Contains(dep, []byte(want)) {
			t.Fatalf("dependabot.yml missing %q", want)
		}
	}
	for _, want := range []string{"schedule:\n    - cron: \"17 5 1 * *\"\n  workflow_dispatch:\n\npermissions:\n  contents: read\n", "if: github.repository == 'tkakkie/ribbitto' && github.ref == 'refs/heads/main'\n", "permissions:\n      contents: read\n      issues: write\n", "with:\n          ref: refs/heads/main\n          persist-credentials: false\n", "      - name: build version coverage tool\n        run: go -C tools build -mod=readonly -o \"$RUNNER_TEMP/versioncheck\" ./versioncheck\n      - name: check version coverage and update tracking issue\n", "run: '\"$RUNNER_TEMP/versioncheck\" -sync-issue .'"} {
		if !bytes.Contains(wf, []byte(want)) {
			t.Fatalf("version-coverage.yml missing %q", want)
		}
	}
	runStep := bytes.Index(wf, []byte("      - name: check version coverage and update tracking issue\n"))
	if runStep < 0 || bytes.Contains(wf[:runStep], []byte("GITHUB_TOKEN")) {
		t.Fatal("GITHUB_TOKEN must only be present in the binary execution step")
	}
	policy, err := os.ReadFile("../../docs/dependency-updates.md")
	if err != nil || !bytes.Contains(policy, []byte("Node major upgrades and end-of-life transitions are a manual-only gap; the Node row checks only the pinned major.")) {
		t.Fatalf("Node major/EOL manual-only gap must be documented: %v", err)
	}
	for _, tc := range []struct {
		name, goVer, nodeVer, pgDigest, twVer, lintVer string
		failNode, driftGo, existingIssue, wantErr      bool
		wantSummary, wantRow                           string
		laterPage, failPage                            bool
	}{
		{"up to date", "go1.27.3", "v26.8.1", testDigest, "v4.3.3", "v2.14.0", false, false, false, false, "Summary: 5 up-to-date, 0 outdated, 0 check-failed.", "| Go runtime | `go.mod`, `tools/go.mod`, `tools/tbls/go.mod` | `1.27.3` | `1.27.3` | `up-to-date` |", false, false},
		{"pin newer than latest", "go1.27.3", "v26.8.1", testDigest, "v3.4.20", "v2.14.0", false, false, false, true, "Summary: 4 up-to-date, 0 outdated, 1 check-failed.", "| Tailwind CSS binary | `tools/download/main.go` | `v4.3.3` | `v3.4.20 (pin newer than upstream latest)` | `check-failed` |", false, false},
		{"redirect limit", "go1.27.3", "v26.8.1", testDigest, "v4.3.3", "v2.14.0", false, false, false, true, "Summary: 4 up-to-date, 0 outdated, 1 check-failed.", "stopped after 10 redirects", false, false},
		{"outdated pins", "go1.27.4", "v26.9.0", strings.Replace(testDigest, "3a", "4b", 1), "v4.4.0", "v2.15.0", false, false, true, false, "Summary: 0 up-to-date, 5 outdated, 0 check-failed.", "| Node runtime | `.node-version` | `26.8.1` | `26.9.0` | `outdated` |", false, false},
		{"newer Node major only", "go1.27.3", "v27.0.0", testDigest, "v4.3.3", "v2.14.0", false, false, false, true, "Summary: 4 up-to-date, 0 outdated, 1 check-failed.", "| Node runtime | `.node-version` | `26.8.1` | `no release found` | `check-failed` |", false, false},
		{"failed check", "go1.27.3", "v26.8.1", testDigest, "v4.3.3", "v2.14.0", true, true, true, true, "Summary: 3 up-to-date, 0 outdated, 2 check-failed.", "| Node runtime | `.node-version` | `26.8.1` | `HTTP 502` | `check-failed` |", false, false},
		{"later page match", "go1.27.3", "v26.8.1", testDigest, "v4.3.3", "v2.14.0", false, false, true, false, "Summary: 5 up-to-date, 0 outdated, 0 check-failed.", "", true, false},
		{"pagination failure", "go1.27.3", "v26.8.1", testDigest, "v4.3.3", "v2.14.0", false, false, false, true, "Summary: 5 up-to-date, 0 outdated, 0 check-failed.", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := "../.."
			if tc.driftGo {
				root = t.TempDir()
				for _, rel := range []string{"go.mod", "tools/go.mod", "tools/tbls/go.mod", ".node-version", "compose.yml", ".github/workflows/ci.yml", "tools/download/main.go"} {
					data, _ := os.ReadFile(filepath.Join("../..", rel))
					if rel == "tools/go.mod" {
						data = []byte("module example/tools\n\ngo 1.27.2\n")
					}
					_ = os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755)
					_ = os.WriteFile(filepath.Join(root, rel), data, 0o644)
				}
			}
			var created, patched string
			var issuePages int
			var nodeRedirects int
			responses := map[string]string{
				"/go": `[{"version":"` + tc.goVer + `","stable":true}]`, "/node": `[{"version":"v27.0.0"},{"version":"` + tc.nodeVer + `"},{"version":"v24.10.0"}]`, "/token": `{"token":"reg-tok","expires_in":300,"issued_at":"2026-11-03T05:17:00Z"}`,
				"/repos/tailwindlabs/tailwindcss/releases/latest": `{"tag_name":"` + tc.twVer + `","id":123,"draft":false,"prerelease":false,"author":{"login":"maintainer"},"assets":[{"id":456,"name":"tailwindcss"}]}`,
				"/repos/golangci/golangci-lint/releases/latest":   `{"tag_name":"` + tc.lintVer + `","id":789,"draft":false,"prerelease":false,"author":{"login":"maintainer"},"assets":[{"id":987,"name":"golangci-lint"}]}`,
				"/repos/example/project/issues":                   map[bool]string{false: `[]`, true: `[{"number":9,"title":"` + issueTitle + `","author_association":"NONE","user":{"login":"stranger"}},{"number":42,"title":"` + issueTitle + `","author_association":"NONE","user":{"login":"github-actions[bot]"}}]`}[tc.existingIssue],
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/node" && tc.name == "redirect limit" && nodeRedirects < 11:
					nodeRedirects++
					http.Redirect(w, r, "/node", http.StatusFound)
				case r.URL.Path == "/node" && tc.failNode:
					w.WriteHeader(http.StatusBadGateway)
				case r.URL.Path == "/manifest":
					w.Header().Set("Docker-Content-Digest", tc.pgDigest)
				case r.Method == http.MethodGet && r.URL.Path == "/repos/example/project/issues":
					issuePages++
					wantPage := "1"
					if issuePages == 2 {
						wantPage = "2"
					}
					if r.URL.Query().Get("state") != "open" || r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != wantPage || issuePages > 2 {
						t.Errorf("unexpected issue-list query: %s", r.URL.RawQuery)
						w.WriteHeader(http.StatusBadRequest)
					} else if tc.laterPage && issuePages == 1 {
						_, _ = io.WriteString(w, "["+strings.Repeat(`{"title":"unrelated"},`, 98)+`{"number":8,"title":"`+issueTitle+`","author_association":"OWNER","pull_request":{},"user":{"login":"github-actions[bot]"}},{"number":9,"title":"`+issueTitle+`","author_association":"NONE","user":{"login":"stranger"}}]`)
					} else if tc.failPage {
						w.WriteHeader(http.StatusBadGateway)
					} else {
						_, _ = io.WriteString(w, responses[r.URL.Path])
					}
				case r.Method == http.MethodGet && responses[r.URL.Path] != "":
					_, _ = io.WriteString(w, responses[r.URL.Path])
				case r.URL.Path == "/repos/example/project/issues":
					raw, _ := io.ReadAll(r.Body)
					created = string(raw)
					w.WriteHeader(http.StatusCreated)
				case r.URL.Path == "/repos/example/project/issues/42":
					if r.Method != http.MethodPatch {
						t.Errorf("issue update method = %s, want PATCH", r.Method)
					}
					raw, _ := io.ReadAll(r.Body)
					patched = string(raw)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			cfg := config{goURL: srv.URL + "/go", nodeURL: srv.URL + "/node", dockerTokenURL: srv.URL + "/token", dockerManifestURL: srv.URL + "/manifest", githubAPIBase: srv.URL, repo: "example/project", token: "gh-tok", now: time.Date(2026, 11, 3, 5, 17, 0, 0, time.UTC)}
			var out bytes.Buffer
			err := run(context.Background(), root, true, cfg, &out)
			if tc.name == "redirect limit" && nodeRedirects != 10 {
				t.Fatalf("followed %d redirect responses, want 10", nodeRedirects)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("run error = %v, wantErr = %v", err, tc.wantErr)
			}
			if !strings.Contains(out.String(), tc.wantSummary) || !strings.Contains(out.String(), tc.wantRow) {
				t.Fatalf("unexpected output:\n%s", out.String())
			}
			if tc.laterPage && issuePages != 2 {
				t.Fatalf("listed %d issue pages, want 2", issuePages)
			}
			if tc.failPage {
				if err == nil || !strings.Contains(err.Error(), "listing issues (check-failed): HTTP 502") || created != "" || patched != "" {
					t.Fatalf("pagination failure: err=%v created=%q patched=%q", err, created, patched)
				}
				return
			}
			if (tc.existingIssue && (!strings.Contains(patched, tc.wantSummary) || created != "")) || (!tc.existingIssue && (!strings.Contains(created, tc.wantSummary) || patched != "")) {
				t.Fatalf("unexpected issue sync: created=%q patched=%q", created, patched)
			}
		})
	}
}
