// Command versioncheck checks runtime, container-digest and downloaded-binary
// pins not surfaced by Dependabot and optionally updates the tracking issue.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const issueTitle = "build: monthly version-coverage review"

var (
	pgDigestRe = regexp.MustCompile(`(?m)^\s*image:\s*postgres:18@(sha256:[0-9a-f]{64})\s*$`)
	semverRe   = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	cellSan    = strings.NewReplacer("|", "/", "`", "'", "\n", " ", "\r", " ")
)

type config struct {
	goURL, nodeURL, dockerTokenURL, dockerManifestURL, githubAPIBase, repo, token string
	now                                                                           time.Time
}

func main() {
	syncIssue := flag.Bool("sync-issue", false, "create or update the tracking issue")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: versioncheck [-sync-issue] <repository root>")
		os.Exit(2)
	}
	var out io.Writer = os.Stdout
	if f, err := os.OpenFile(os.Getenv("GITHUB_STEP_SUMMARY"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		defer func() { _ = f.Close() }()
		out = io.MultiWriter(os.Stdout, f)
	}
	cfg := config{
		goURL: "https://go.dev/dl/?mode=json", nodeURL: "https://nodejs.org/dist/index.json",
		dockerTokenURL:    "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/postgres:pull",
		dockerManifestURL: "https://registry-1.docker.io/v2/library/postgres/manifests/18",
		githubAPIBase:     "https://api.github.com", repo: cmp.Or(os.Getenv("GITHUB_REPOSITORY"), "tkakkie/ribbitto"),
		token: os.Getenv("GITHUB_TOKEN"), now: time.Now().UTC(),
	}
	if err := run(context.Background(), flag.Arg(0), *syncIssue, cfg, out); err != nil {
		fmt.Fprintln(os.Stderr, "versioncheck:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, root string, syncIssue bool, cfg config, out io.Writer) error {
	c := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error { req.Header.Del("Authorization"); return nil }}
	var goPin, pgPin string
	var goErr error
	for _, rel := range []string{"go.mod", "tools/go.mod", "tools/tbls/go.mod"} {
		if v, err := readMatch(root, rel, `(?m)^go (\d+\.\d+\.\d+)\s*$`); err != nil || (goPin != "" && v != goPin) {
			goErr = cmp.Or(err, fmt.Errorf("go.mod mismatch: %s != %s", v, goPin))
			break
		} else {
			goPin = v
		}
	}
	nodePin, nodeErr := readMatch(root, ".node-version", `^\s*(\d+\.\d+\.\d+)\s*$`)
	comp, compErr := os.ReadFile(filepath.Join(root, "compose.yml"))
	ci, ciErr := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	pgMatches := pgDigestRe.FindAllStringSubmatch(string(ci), -1)
	pgErr := cmp.Or(compErr, ciErr)
	if pgErr == nil && (!bytes.Contains(comp, []byte("image: postgres:18\n")) || len(pgMatches) < 3 || slices.ContainsFunc(pgMatches[1:], func(m []string) bool { return m[1] != pgMatches[0][1] })) {
		pgErr = fmt.Errorf("postgres:18 pin missing or mismatched")
	} else if pgErr == nil {
		pgPin = pgMatches[0][1]
	}
	twPin, twErr := readMatch(root, "tools/download/main.go", `(?m)^const tailwindVersion = "(v\d+\.\d+\.\d+)"\s*$`)
	lintPin, lintErr := readMatch(root, "tools/download/main.go", `(?m)^const lintVersion = "(\d+\.\d+\.\d+)"\s*$`)
	ghBase := strings.TrimRight(cfg.githubAPIBase, "/")
	rows := [][5]string{
		evalRow("Go runtime", "`go.mod`, `tools/go.mod`, `tools/tbls/go.mod`", goPin, true, goErr, fetchVer(ctx, c, cfg.goURL, "", true, true)),
		evalRow("Node runtime", "`.node-version`", nodePin, true, nodeErr, fetchVer(ctx, c, cfg.nodeURL, "", true, false)),
		evalRow("PostgreSQL 18 image", "`compose.yml`, `.github/workflows/ci.yml`", pgPin, false, pgErr, func() (string, error) {
			var tok struct {
				Token string `json:"token"`
			}
			if _, err := doJSON(ctx, c, "GET", cfg.dockerTokenURL, "", "", nil, &tok); err != nil || tok.Token == "" {
				return "", cmp.Or(err, fmt.Errorf("empty registry token"))
			}
			h, err := doJSON(ctx, c, "HEAD", cfg.dockerManifestURL, tok.Token, "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json", nil, nil)
			if d := strings.TrimSpace(h.Get("Docker-Content-Digest")); err == nil && regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(d) {
				return d, nil
			}
			return "", cmp.Or(err, fmt.Errorf("invalid digest header"))
		}),
		evalRow("Tailwind CSS binary", "`tools/download/main.go`", twPin, true, twErr, fetchVer(ctx, c, ghBase+"/repos/tailwindlabs/tailwindcss/releases/latest", cfg.token, false, false)),
		evalRow("golangci-lint binary", "`tools/download/main.go`", "v"+lintPin, true, lintErr, fetchVer(ctx, c, ghBase+"/repos/golangci/golangci-lint/releases/latest", cfg.token, false, false)),
	}
	var counts [3]int
	var b strings.Builder
	fmt.Fprintf(&b, "# Monthly version-coverage review\n\nChecked at `%s`. Policy and inventory: [`docs/dependency-updates.md`](docs/dependency-updates.md).\n\n| Class | Source of truth | Pinned | Latest upstream | Status |\n|---|---|---|---|---|\n", cfg.now.UTC().Format(time.RFC3339))
	for _, r := range rows {
		counts[max(0, slices.Index([]string{"up-to-date", "outdated", "check-failed"}, r[4]))]++
		fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` | `%s` |\n", r[0], r[1], r[2], r[3], r[4])
	}
	fmt.Fprintf(&b, "\nSummary: %d up-to-date, %d outdated, %d check-failed.\n\nUnchecked manual-only pins: `web/static/vendor/` (`htmx`, `htmx-ext-sse`, `idiomorph`, `@fluentui/svg-icons`); see [`docs/dependency-updates.md`](docs/dependency-updates.md).\n", counts[0], counts[1], counts[2])
	report := b.String()
	_, _ = io.WriteString(out, report)
	if syncIssue {
		if cfg.token == "" {
			return fmt.Errorf("missing GITHUB_TOKEN for -sync-issue")
		}
		base := ghBase + "/repos/" + cfg.repo + "/issues"
		method, target, payload := "POST", base, map[string]string{"title": issueTitle, "body": report}
		for page := 1; ; page++ {
			var issues []map[string]any
			if _, err := doJSON(ctx, c, "GET", fmt.Sprintf("%s?state=open&per_page=100&page=%d", base, page), cfg.token, "application/vnd.github+json", nil, &issues); err != nil {
				return fmt.Errorf("listing issues (check-failed): %w", err)
			}
			for _, it := range issues {
				user, _ := it["user"].(map[string]any)
				if it["pull_request"] == nil && it["title"] == issueTitle && (user["login"] == "github-actions[bot]" || it["author_association"] == "OWNER") {
					method, target, payload = "PATCH", fmt.Sprintf("%s/%.0f", base, it["number"]), map[string]string{"body": report}
					break
				}
			}
			if method == "PATCH" || len(issues) < 100 {
				break
			}
		}
		raw, _ := json.Marshal(payload)
		if _, err := doJSON(ctx, c, method, target, cfg.token, "application/vnd.github+json", raw, nil); err != nil {
			return err
		}
	}
	if counts[2] > 0 {
		return fmt.Errorf("%d version check(s) failed", counts[2])
	}
	return nil
}

func fetchVer(ctx context.Context, c *http.Client, u, token string, list, stableOnly bool) func() (string, error) {
	return func() (string, error) {
		if !list {
			var rel struct {
				TagName string `json:"tag_name"`
			}
			_, err := doJSON(ctx, c, "GET", u, token, "application/vnd.github+json", nil, &rel)
			return rel.TagName, err
		}
		var rels []map[string]any
		_, err := doJSON(ctx, c, "GET", u, "", "", nil, &rels)
		for _, r := range rels {
			if s, _ := r["version"].(string); err == nil && s != "" && (!stableOnly || r["stable"] == true) {
				return strings.TrimLeft(s, "gov"), nil
			}
		}
		return "", cmp.Or(err, fmt.Errorf("no release found"))
	}
}

func readMatch(root, rel, pattern string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if m := regexp.MustCompile(pattern).FindSubmatch(data); err == nil && m != nil {
		return string(m[1]), nil
	}
	return "", cmp.Or(err, fmt.Errorf("missing pin in %s", rel))
}

func evalRow(class, src, pin string, semver bool, pinErr error, fetch func() (string, error)) [5]string {
	if pinErr != nil {
		return [5]string{class, src, "invalid", cellSan.Replace(strings.TrimSpace(pinErr.Error())), "check-failed"}
	}
	latest, err := fetch()
	if err != nil {
		return [5]string{class, src, pin, cellSan.Replace(strings.TrimSpace(err.Error())), "check-failed"}
	}
	if strings.HasPrefix(pin, "v") && !strings.HasPrefix(latest, "v") {
		latest = "v" + latest
	}
	if semver && (!semverRe.MatchString(pin) || !semverRe.MatchString(latest)) {
		return [5]string{class, src, pin, cellSan.Replace(strings.TrimSpace(latest)), "check-failed"}
	}
	var va, vb [3]int
	_, _ = fmt.Sscanf(strings.TrimPrefix(pin, "v"), "%d.%d.%d", &va[0], &va[1], &va[2])
	_, _ = fmt.Sscanf(strings.TrimPrefix(latest, "v"), "%d.%d.%d", &vb[0], &vb[1], &vb[2])
	if (!semver && pin != latest) || (semver && slices.Compare(va[:], vb[:]) < 0) {
		return [5]string{class, src, pin, latest, "outdated"}
	}
	return [5]string{class, src, pin, latest, "up-to-date"}
}

func doJSON(ctx context.Context, c *http.Client, method, u, token, accept string, body []byte, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"Accept": accept, "Authorization": strings.TrimSpace("Bearer " + token), "Content-Type": map[bool]string{true: "application/json"}[body != nil]} {
		if v != "" && v != "Bearer" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.Header, cmp.Or(err, fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	if out != nil && len(raw) > 0 {
		return resp.Header, json.Unmarshal(raw, out)
	}
	return resp.Header, nil
}
