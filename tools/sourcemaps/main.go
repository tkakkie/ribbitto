// Command sourcemaps uploads production source maps to the private release store,
// enforces distinct-SHA retention and verifies token expiry.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	canonicalRe = regexp.MustCompile(`^map-([0-9]{8}T[0-9]{6}Z)-([0-9a-f]{40})-([1-9][0-9]*)\.([1-9][0-9]*)$`)
	shaRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	numIDRe     = regexp.MustCompile(`^[1-9][0-9]*$`)
)

const (
	maxResponseBytes = 64 << 20
	maxReleasePages  = 100
	prodAPIBase      = "https://api.github.com"
	prodUploadBase   = "https://uploads.github.com"
	prodStoreRepo    = "tkakkie/ribbitto-sourcemaps"
	prodSourceRepo   = "tkakkie/ribbitto"
)

type config struct {
	apiBase, uploadBase, storeRepo, sourceRepo, token string
	now                                               time.Time
	stdout, summary                                   io.Writer
	transport                                         http.RoundTripper
	allowTestEndpoint                                 bool
}

func prodConfig(token string, stdout, summary io.Writer) config {
	return config{apiBase: prodAPIBase, uploadBase: prodUploadBase, storeRepo: prodStoreRepo, sourceRepo: prodSourceRepo, token: token, stdout: stdout, summary: summary}
}

type bundle struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Map    string `json:"map"`
}

type manifest struct {
	Commit  string            `json:"commit"`
	RunID   string            `json:"run_id"`
	Entries map[string]bundle `json:"entries"`
}

type ghAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	ID         int64     `json:"id"`
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type releaseName struct {
	Raw            string
	Start          time.Time
	SHA            string
	RunID, Attempt uint64
}

type parsedRel struct {
	rel    ghRelease
	parsed releaseName
}

func parseReleaseName(s string) (releaseName, bool) {
	m := canonicalRe.FindStringSubmatch(s)
	if m == nil {
		return releaseName{}, false
	}
	t, err := time.Parse("20060102T150405Z", m[1])
	runID, err1 := strconv.ParseUint(m[3], 10, 64)
	attempt, err2 := strconv.ParseUint(m[4], 10, 64)
	if err != nil || err1 != nil || err2 != nil || t.UTC().Format("20060102T150405Z") != m[1] {
		return releaseName{}, false
	}
	return releaseName{s, t.UTC(), m[2], runID, attempt}, true
}

func compareOrder(a, b releaseName) int {
	return cmp.Or(a.Start.Compare(b.Start), cmp.Compare(a.RunID, b.RunID), cmp.Compare(a.Attempt, b.Attempt))
}

type apiClient struct {
	cfg    config
	expiry string
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 || req.Method != http.MethodGet || via[0].Method != http.MethodGet || req.URL.Scheme != "https" {
		return fmt.Errorf("redirect refused")
	}
	req.Header.Del("Authorization")
	return nil
}

func (c *apiClient) do(ctx context.Context, storeAuth bool, method, path, accept string, body []byte, out any) (int, []byte, error) {
	if !c.cfg.allowTestEndpoint && (c.cfg.apiBase != prodAPIBase || c.cfg.uploadBase != prodUploadBase || c.cfg.storeRepo != prodStoreRepo) {
		return 0, nil, fmt.Errorf("refusing non-production endpoint configuration")
	}
	base := strings.TrimRight(c.cfg.apiBase, "/")
	if method == "POST" && strings.HasSuffix(strings.Split(path, "?")[0], "/assets") {
		base = strings.TrimRight(cmp.Or(c.cfg.uploadBase, strings.Replace(base, "://api.github.com", "://uploads.github.com", 1)), "/")
	}
	if !c.cfg.allowTestEndpoint && !strings.HasPrefix(base, "https://") {
		return 0, nil, fmt.Errorf("refusing non-https base %s", base)
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", cmp.Or(accept, "application/vnd.github+json"))
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if storeAuth {
		req.Header.Set("Authorization", "Bearer "+c.cfg.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 2 * time.Minute, Transport: c.cfg.transport, CheckRedirect: checkRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: request failed", method, path)
	}
	defer func() { _ = resp.Body.Close() }()
	if exp := resp.Header.Get("github-authentication-token-expiration"); storeAuth && exp != "" {
		c.expiry = exp
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err == nil && int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("%s %s: response exceeds %d bytes", method, path, maxResponseBytes)
	}
	if err == nil && out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && len(raw) > 0 {
		err = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, raw, err
}

func (c *apiClient) listReleases(ctx context.Context) ([]ghRelease, error) {
	var all []ghRelease
	for page := 1; page <= maxReleasePages; page++ {
		var batch []ghRelease
		if code, _, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases?per_page=100&page=%d", c.cfg.storeRepo, page), "", nil, &batch); err != nil || code != 200 {
			return nil, fmt.Errorf("listing releases: status %d (%v)", code, err)
		} else if all = append(all, batch...); len(batch) < 100 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("listing releases: exceeded %d pages", maxReleasePages)
}

func (c *apiClient) deleteReleaseByID(ctx context.Context, r ghRelease) error {
	if _, ok := parseReleaseName(r.Name); !ok || r.ID <= 0 || r.Prerelease || r.Name != r.TagName {
		return fmt.Errorf("refusing to delete non-canonical release %d", r.ID)
	}
	p := fmt.Sprintf("/repos/%s/releases/%d", c.cfg.storeRepo, r.ID)
	var got ghRelease
	if code, _, err := c.do(ctx, true, "GET", p, "", nil, &got); err != nil || code == 404 {
		return err
	} else if code != 200 || got.ID != r.ID || got.Name != r.Name || got.TagName != r.TagName || got.Draft != r.Draft || got.Prerelease {
		return fmt.Errorf("release lookup mismatch for id %d (status %d)", r.ID, code)
	}
	if code, _, err := c.do(ctx, true, "DELETE", p, "", nil, nil); err != nil || (code != 204 && code != 404) {
		return fmt.Errorf("deleting release %d: status %d (%v)", r.ID, code, err)
	}
	return nil
}

func (c *apiClient) deleteTagByRef(ctx context.Context, tag string) error {
	if _, ok := parseReleaseName(tag); !ok {
		return fmt.Errorf("refusing to delete non-canonical tag %q", tag)
	}
	if code, _, err := c.do(ctx, true, "DELETE", fmt.Sprintf("/repos/%s/git/refs/tags/%s", c.cfg.storeRepo, tag), "", nil, nil); err != nil || (code != 204 && code != 404) {
		return fmt.Errorf("deleting tag %s: status %d (%v)", tag, code, err)
	}
	return nil
}

func runStore(ctx context.Context, cfg config, mapDir, sha, runID, attempt string) error {
	relName := fmt.Sprintf("map-%s-%s-%s.%s", cfg.now.UTC().Format("20060102T150405Z"), sha, runID, attempt)
	if _, ok := parseReleaseName(relName); !ok || cfg.token == "" || cfg.now.IsZero() {
		return fmt.Errorf("invalid store configuration or arguments")
	}
	idxBytes, err := os.ReadFile(filepath.Join(mapDir, "index.json"))
	var m manifest
	if err != nil || json.Unmarshal(idxBytes, &m) != nil || m.Commit != sha || m.RunID != runID || len(m.Entries) == 0 {
		return fmt.Errorf("invalid map index in %s: %w", mapDir, err)
	}
	assets := map[string][]byte{"index.json": idxBytes}
	for _, b := range m.Entries {
		data, err := os.ReadFile(filepath.Join(mapDir, b.Map))
		if err != nil || b.File == "" || b.SHA256 == "" || b.Map != b.File+".map" || !bytes.Contains(data, []byte(`"sourcesContent"`)) {
			return fmt.Errorf("invalid source map %s: %w", b.Map, err)
		}
		assets[b.Map] = data
	}
	if entries, err := os.ReadDir(mapDir); err != nil || len(entries) != len(assets) {
		return fmt.Errorf("unexpected files in %s", mapDir)
	}
	c := &apiClient{cfg: cfg}
	createBody, _ := json.Marshal(map[string]any{"tag_name": relName, "name": relName, "target_commitish": sha, "draft": true, "prerelease": false})
	var created, draftRel, pubRel ghRelease
	if code, _, err := c.do(ctx, true, "POST", fmt.Sprintf("/repos/%s/releases", cfg.storeRepo), "", createBody, &created); err != nil || code != 201 || created.ID <= 0 || !created.Draft || created.Prerelease {
		return fmt.Errorf("creating draft release: status %d (%v)", code, err)
	}
	for _, name := range slices.Sorted(maps.Keys(assets)) {
		u := fmt.Sprintf("/repos/%s/releases/%d/assets?name=%s", cfg.storeRepo, created.ID, url.QueryEscape(name))
		if code, _, err := c.do(ctx, true, "POST", u, "", assets[name], nil); err != nil || (code != 201 && code != 200) {
			return fmt.Errorf("uploading asset %s: status %d (%v)", name, code, err)
		}
	}
	relPath := fmt.Sprintf("/repos/%s/releases/%d", cfg.storeRepo, created.ID)
	if code, _, err := c.do(ctx, true, "GET", relPath, "", nil, &draftRel); err != nil || code != 200 || draftRel.ID != created.ID || !draftRel.Draft || draftRel.Prerelease || draftRel.Name != relName || draftRel.TagName != relName {
		return fmt.Errorf("verifying draft release: status %d (%v)", code, err)
	}
	var idxID int64
	for _, a := range draftRel.Assets {
		if a.Name == "index.json" {
			idxID = a.ID
		}
	}
	code, upIdxBytes, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/assets/%d", cfg.storeRepo, idxID), "application/octet-stream", nil, nil)
	var upIdx manifest
	if idxID == 0 || err != nil || code != 200 || json.Unmarshal(upIdxBytes, &upIdx) != nil || upIdx.Commit != sha || upIdx.RunID != runID || len(upIdx.Entries) == 0 {
		return fmt.Errorf("verifying uploaded manifest: status %d (%v)", code, err)
	}
	wantAssets := map[string]int64{"index.json": int64(len(idxBytes))}
	for _, e := range upIdx.Entries {
		wantAssets[e.Map] = int64(len(assets[e.Map]))
	}
	for _, a := range draftRel.Assets {
		if sz, ok := wantAssets[a.Name]; !ok || (a.Size > 0 && a.Size != sz) {
			return fmt.Errorf("uploaded asset %s does not match manifest", a.Name)
		}
		delete(wantAssets, a.Name)
	}
	if len(wantAssets) != 0 {
		return fmt.Errorf("uploaded asset set missing manifest entries")
	}
	pubBody, _ := json.Marshal(map[string]any{"draft": false, "prerelease": false})
	if code, _, err = c.do(ctx, true, "PATCH", relPath, "", pubBody, &pubRel); code == 422 {
		// The separate store repository holds only a README; fall back to its default branch if the source commit SHA is absent there.
		fallback, _ := json.Marshal(map[string]any{"draft": false, "prerelease": false, "target_commitish": "main"})
		code, _, err = c.do(ctx, true, "PATCH", relPath, "", fallback, &pubRel)
	}
	if err != nil || code != 200 || pubRel.Draft || pubRel.Prerelease {
		return fmt.Errorf("publishing release: status %d (%v)", code, err)
	}
	first := upIdx.Entries[slices.Sorted(maps.Keys(upIdx.Entries))[0]]
	if gotRel, gotMap, gotBytes, err := lookupWithClient(ctx, c, sha, first.SHA256, runID); err != nil || gotRel != relName || gotMap != first.Map || !bytes.Equal(gotBytes, assets[first.Map]) {
		return fmt.Errorf("fetching back stored map: %v", err)
	}
	_, _ = fmt.Fprintf(cfg.stdout, "stored and fetched %s (%d maps)\n", relName, len(upIdx.Entries))
	return nil
}

func runLookup(ctx context.Context, cfg config, sha, bundleSHA256, runID string) (string, string, []byte, error) {
	return lookupWithClient(ctx, &apiClient{cfg: cfg}, sha, bundleSHA256, runID)
}

func lookupWithClient(ctx context.Context, c *apiClient, sha, bundleSHA256, runID string) (string, string, []byte, error) {
	if !shaRe.MatchString(sha) || (runID != "" && !numIDRe.MatchString(runID)) {
		return "", "", nil, fmt.Errorf("invalid lookup arguments")
	}
	rels, err := c.listReleases(ctx)
	if err != nil {
		return "", "", nil, err
	}
	var cands []parsedRel
	for _, r := range rels {
		p, ok := parseReleaseName(r.Name)
		if ok && !r.Draft && !r.Prerelease && r.Name == r.TagName && p.SHA == sha && (c.cfg.now.IsZero() || !p.Start.After(c.cfg.now.Add(24*time.Hour))) && (runID == "" || strconv.FormatUint(p.RunID, 10) == runID) {
			cands = append(cands, parsedRel{r, p})
		}
	}
	if len(cands) == 0 {
		return "", "", nil, fmt.Errorf("no published release found for %s", sha)
	}
	slices.SortFunc(cands, func(a, b parsedRel) int { return compareOrder(b.parsed, a.parsed) })
	best, assets := cands[0].rel, make(map[string]int64)
	for _, a := range best.Assets {
		assets[a.Name] = a.ID
	}
	code, idxBytes, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/assets/%d", c.cfg.storeRepo, assets["index.json"]), "application/octet-stream", nil, nil)
	var m manifest
	if assets["index.json"] == 0 || err != nil || code != 200 || json.Unmarshal(idxBytes, &m) != nil {
		return "", "", nil, fmt.Errorf("reading index.json from %s: status %d (%v)", best.Name, code, err)
	}
	if bundleSHA256 == "" {
		return best.Name, "index.json", idxBytes, nil
	}
	for _, e := range m.Entries {
		if e.SHA256 == bundleSHA256 && assets[e.Map] != 0 {
			code, mapBytes, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/assets/%d", c.cfg.storeRepo, assets[e.Map]), "application/octet-stream", nil, nil)
			if err != nil || code != 200 {
				return "", "", nil, fmt.Errorf("downloading map %s: status %d (%v)", e.Map, code, err)
			}
			return best.Name, e.Map, mapBytes, nil
		}
	}
	return "", "", nil, fmt.Errorf("bundle hash %s not found in %s", bundleSHA256, best.Name)
}

func main() {
	lookup := flag.Bool("lookup", false, "find and fetch a production source map by commit SHA, bundle SHA-256 and optional run id")
	flag.Parse()
	args := flag.Args()
	var summary io.Writer = os.Stderr
	if f, err := os.OpenFile(os.Getenv("GITHUB_STEP_SUMMARY"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		defer func() { _ = f.Close() }()
		summary = io.MultiWriter(os.Stderr, f)
	}
	cfg := prodConfig(os.Getenv("SOURCEMAPS_TOKEN"), os.Stdout, summary)
	var err error
	switch {
	case *lookup && len(args) >= 1 && len(args) <= 3:
		a := append(args, "", "")
		rel, name, data, lerr := runLookup(context.Background(), cfg, a[0], a[1], a[2])
		if err = lerr; err == nil {
			_, _ = fmt.Fprintf(os.Stdout, "%s %s (%d bytes)\n", rel, name, len(data))
		}
	case !*lookup && len(args) == 5:
		if cfg.now, err = time.Parse("20060102T150405Z", args[1]); err != nil || cfg.now.UTC().Format("20060102T150405Z") != args[1] {
			err = fmt.Errorf("invalid UTC start timestamp %q", args[1])
		} else {
			err = runStore(context.Background(), cfg, args[0], args[2], args[3], args[4])
		}
	default:
		err = fmt.Errorf("usage: sourcemaps <map dir> <YYYYMMDDTHHMMSSZ> <commit SHA> <run id> <attempt> | sourcemaps -lookup <commit SHA> [<bundle sha256> [<run id>]]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
