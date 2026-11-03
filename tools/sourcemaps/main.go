// Command sourcemaps uploads production source maps to the private release store,
// verifies the uploaded manifest and assets, applies distinct-SHA retention,
// draft/orphan-tag cleanup and token-expiry checks, and looks up stored maps.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
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
	sha256Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	numIDRe     = regexp.MustCompile(`^[1-9][0-9]*$`)
)

func isPlainFilename(s string) bool {
	return s != "" && s != "." && s != ".." && s != "index.json" && filepath.Base(s) == s && !filepath.IsAbs(s) && !strings.ContainsAny(s, `/\`)
}

func validBundleEntry(b bundle) bool {
	return isPlainFilename(b.File) && isPlainFilename(b.Map) && b.Map == b.File+".map" && sha256Re.MatchString(b.SHA256)
}

const (
	maxResponseBytes = 64 << 20
	maxReleasePages  = 100
	maxWorkflowRuns  = 1000
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
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	State string `json:"state"`
}

func validAsset(a ghAsset) bool {
	return a.ID > 0 && a.Size > 0 && (a.State == "" || a.State == "uploaded")
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
	cfg              config
	expiry           string
	delRels, delTags int
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 || req.Method != http.MethodGet || via[0].Method != http.MethodGet || req.URL.Scheme != "https" {
		return fmt.Errorf("redirect refused")
	}
	req.Header.Del("Authorization")
	return nil
}

func (c *apiClient) do(ctx context.Context, storeAuth bool, method, path, accept string, body []byte, out any) (int, []byte, error) {
	if !c.cfg.allowTestEndpoint && (c.cfg.apiBase != prodAPIBase || c.cfg.uploadBase != prodUploadBase || c.cfg.storeRepo != prodStoreRepo || c.cfg.sourceRepo != prodSourceRepo) {
		return 0, nil, fmt.Errorf("refusing non-production endpoint configuration")
	}
	base := strings.TrimRight(c.cfg.apiBase, "/")
	isUpload := method == "POST" && strings.HasSuffix(strings.Split(path, "?")[0], "/assets")
	if isUpload {
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
	if isUpload {
		req.Header.Set("Content-Type", "application/octet-stream")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 2 * time.Minute, Transport: c.cfg.transport, CheckRedirect: checkRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: request failed (%v)", method, path, errors.Unwrap(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if storeAuth {
		c.expiry = cmp.Or(resp.Header.Get("github-authentication-token-expiration"), c.expiry)
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
	p, ok := parseReleaseName(r.Name)
	if !ok || r.ID <= 0 || r.Prerelease || r.Name != r.TagName {
		return fmt.Errorf("refusing to delete non-canonical release %d", r.ID)
	}
	u := fmt.Sprintf("/repos/%s/releases/%d", c.cfg.storeRepo, r.ID)
	var got ghRelease
	if code, _, err := c.do(ctx, true, "GET", u, "", nil, &got); err != nil || code == 404 {
		return err
	} else if code != 200 || got.ID != r.ID || got.Name != r.Name || got.TagName != r.TagName || got.Draft != r.Draft || got.Prerelease {
		return fmt.Errorf("release lookup mismatch for id %d (status %d)", r.ID, code)
	}
	if code, _, err := c.do(ctx, true, "DELETE", u, "", nil, nil); err != nil || (code != 204 && code != 404) {
		return fmt.Errorf("deleting release %d: status %d (%v)", r.ID, code, err)
	} else if code == 204 {
		c.delRels++
		_, _ = fmt.Fprintf(cmp.Or(c.cfg.stdout, io.Discard), "deleted release %q (id %d, sha %s, age %dd)\n", r.Name, r.ID, p.SHA, int(c.cfg.now.Sub(p.Start)/(24*time.Hour)))
	}
	return nil
}

func (c *apiClient) deleteTagByRef(ctx context.Context, tag string) error {
	p, ok := parseReleaseName(tag)
	if !ok {
		return fmt.Errorf("refusing to delete non-canonical tag %q", tag)
	}
	code, _, err := c.do(ctx, true, "DELETE", fmt.Sprintf("/repos/%s/git/refs/tags/%s", c.cfg.storeRepo, tag), "", nil, nil)
	if err == nil && code == 422 {
		code, _, err = c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/git/ref/tags/%s", c.cfg.storeRepo, tag), "", nil, nil)
	}
	if err != nil || (code != 204 && code != 404) {
		return fmt.Errorf("deleting tag %q: status %d (%v)", tag, code, err)
	}
	if code == 204 {
		c.delTags++
		_, _ = fmt.Fprintf(cmp.Or(c.cfg.stdout, io.Discard), "deleted tag %q (sha %s, age %dd)\n", tag, p.SHA, int(c.cfg.now.Sub(p.Start)/(24*time.Hour)))
	}
	return nil
}

func (c *apiClient) deleteUnreferencedTag(ctx context.Context, tag string, rels []ghRelease, deletedIDs map[int64]bool) error {
	isRef := func(r ghRelease) bool { return !deletedIDs[r.ID] && (r.TagName == tag || r.Name == tag) }
	if slices.ContainsFunc(rels, isRef) {
		return nil
	}
	if tCode, _, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/tags/%s", c.cfg.storeRepo, tag), "", nil, nil); err != nil || (tCode != 404 && tCode != 200) {
		return fmt.Errorf("rechecking tag %q: status %d (%v)", tag, tCode, err)
	} else if tCode == 200 {
		return nil
	}
	if fresh, err := c.listReleases(ctx); err != nil {
		return err
	} else if slices.ContainsFunc(fresh, isRef) {
		return nil
	}
	var refObj struct{ Ref string }
	if rCode, _, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/git/ref/tags/%s", c.cfg.storeRepo, tag), "", nil, &refObj); err != nil || (rCode != 200 && rCode != 404) || (rCode == 200 && refObj.Ref != "refs/tags/"+tag) {
		return fmt.Errorf("verifying tag ref %q: status %d (%v)", tag, rCode, err)
	}
	return c.deleteTagByRef(ctx, tag)
}

// hasActiveRun paginates workflow runs and fails closed on API errors, incomplete
// or 1,000-run-capped listings, or page-cap overflow so retention aborts before
// any deletion when active-run status is unknown.
func (c *apiClient) hasActiveRun(ctx context.Context, sha string) (bool, error) {
	seen, total := 0, 0
	for page := 1; page <= maxReleasePages; page++ {
		var res struct {
			TotalCount int                             `json:"total_count"`
			Runs       []struct{ Status, Path string } `json:"workflow_runs"`
		}
		if code, _, err := c.do(ctx, false, "GET", fmt.Sprintf("/repos/%s/actions/runs?head_sha=%s&per_page=100&page=%d", c.cfg.sourceRepo, sha, page), "", nil, &res); err != nil || code != 200 {
			return false, fmt.Errorf("checking concurrency group for %s: status %d (%v)", sha, code, err)
		}
		for _, r := range res.Runs {
			if r.Status != "completed" && strings.TrimPrefix(strings.Split(r.Path, "@")[0], ".github/workflows/") == "sourcemaps-store.yml" {
				return true, nil
			}
		}
		seen += len(res.Runs)
		total = max(total, res.TotalCount)
		if len(res.Runs) < 100 {
			if total >= maxWorkflowRuns || seen >= maxWorkflowRuns || seen < total {
				return false, fmt.Errorf("checking concurrency group for %s: capped or incomplete workflow runs (%d/%d)", sha, seen, total)
			}
			return false, nil
		}
	}
	return false, fmt.Errorf("checking concurrency group for %s: exceeded %d pages", sha, maxReleasePages)
}

func runStore(ctx context.Context, cfg config, mapDir, sha, runID, attempt string) error {
	relName := fmt.Sprintf("map-%s-%s-%s.%s", cfg.now.UTC().Format("20060102T150405Z"), sha, runID, attempt)
	if _, ok := parseReleaseName(relName); !ok || cfg.token == "" || cfg.now.IsZero() {
		return fmt.Errorf("invalid store configuration or arguments")
	}
	idxBytes, err := os.ReadFile(filepath.Join(mapDir, "index.json"))
	var m manifest
	if err = cmp.Or(err, json.Unmarshal(idxBytes, &m)); err != nil {
		return fmt.Errorf("reading map index in %s: %w", mapDir, err)
	}
	if m.Commit != sha || m.RunID != runID || len(m.Entries) == 0 {
		return fmt.Errorf("invalid map index in %s", mapDir)
	}
	assets, seenFiles := map[string][]byte{"index.json": idxBytes}, make(map[string]bool, len(m.Entries))
	for _, b := range m.Entries {
		if !validBundleEntry(b) || seenFiles[b.File] || assets[b.Map] != nil {
			return fmt.Errorf("invalid source map entry %q", b.Map)
		}
		seenFiles[b.File] = true
		data, err := os.ReadFile(filepath.Join(mapDir, b.Map))
		if err != nil {
			return fmt.Errorf("reading source map %q: %w", b.Map, err)
		}
		if !bytes.Contains(data, []byte(`"sourcesContent"`)) {
			return fmt.Errorf("invalid source map %q: missing sourcesContent", b.Map)
		}
		assets[b.Map] = data
	}
	if entries, err := os.ReadDir(mapDir); err != nil || len(entries) != len(assets) {
		return fmt.Errorf("unexpected files in %s", mapDir)
	}
	c := &apiClient{cfg: cfg}
	createBody, _ := json.Marshal(map[string]any{"tag_name": relName, "name": relName, "target_commitish": "main", "draft": true, "prerelease": false})
	var created, draftRel, pubRel ghRelease
	if code, _, err := c.do(ctx, true, "POST", fmt.Sprintf("/repos/%s/releases", cfg.storeRepo), "", createBody, &created); err != nil || code != 201 || created.ID <= 0 || !created.Draft || created.Prerelease {
		return fmt.Errorf("creating draft release: status %d (%v)", code, err)
	}
	for _, name := range slices.Sorted(maps.Keys(assets)) {
		u := fmt.Sprintf("/repos/%s/releases/%d/assets?name=%s", cfg.storeRepo, created.ID, url.QueryEscape(name))
		if code, _, err := c.do(ctx, true, "POST", u, "", assets[name], nil); err != nil || (code != 201 && code != 200) {
			return fmt.Errorf("uploading asset %q: status %d (%v)", name, code, err)
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
	if idxID == 0 || err != nil || code != 200 || !bytes.Equal(upIdxBytes, idxBytes) || json.Unmarshal(upIdxBytes, &upIdx) != nil || upIdx.Commit != sha || upIdx.RunID != runID || !maps.Equal(upIdx.Entries, m.Entries) {
		return fmt.Errorf("verifying uploaded manifest: status %d (%v)", code, err)
	}
	wantAssets := map[string]int64{"index.json": int64(len(idxBytes))}
	for _, e := range m.Entries {
		wantAssets[e.Map] = int64(len(assets[e.Map]))
	}
	assetIDs := make(map[string]int64, len(draftRel.Assets))
	for _, a := range draftRel.Assets {
		if sz, ok := wantAssets[a.Name]; !ok || !validAsset(a) || a.Size != sz {
			return fmt.Errorf("uploaded asset %q does not match manifest", a.Name)
		}
		assetIDs[a.Name] = a.ID
		delete(wantAssets, a.Name)
	}
	if len(wantAssets) != 0 {
		return fmt.Errorf("uploaded asset set missing manifest entries")
	}
	pubBody, _ := json.Marshal(map[string]any{"draft": false, "prerelease": false})
	if code, _, err = c.do(ctx, true, "PATCH", relPath, "", pubBody, &pubRel); err != nil || code != 200 || pubRel.ID != created.ID || pubRel.Draft || pubRel.Prerelease || pubRel.Name != relName || pubRel.TagName != relName {
		return fmt.Errorf("publishing release: status %d (%v)", code, err)
	}
	if gotRel, _, gotIdx, err := lookupWithClient(ctx, c, sha, "", runID); err != nil || gotRel != relName || !bytes.Equal(gotIdx, idxBytes) {
		return fmt.Errorf("verifying published release: %v", err)
	}
	for _, k := range slices.Sorted(maps.Keys(m.Entries)) {
		e := m.Entries[k]
		if code, gotBytes, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/assets/%d", cfg.storeRepo, assetIDs[e.Map]), "application/octet-stream", nil, nil); err != nil || code != 200 || !bytes.Equal(gotBytes, assets[e.Map]) {
			return fmt.Errorf("fetching back stored map %q: status %d (%v)", e.Map, code, err)
		}
	}
	_, _ = fmt.Fprintf(cfg.stdout, "stored and fetched %q (%d maps)\n", relName, len(m.Entries))
	if err := runRetention(ctx, c, sha); err != nil {
		return err
	}
	if c.expiry == "" {
		_, _ = fmt.Fprintf(cmp.Or(cfg.summary, io.Discard), "warning: missing github-authentication-token-expiration header\n")
		return nil
	}
	exp, err := time.Parse("2006-01-02 15:04:05 UTC", c.expiry)
	if err != nil {
		exp, err = time.Parse("2006-01-02 15:04:05 -0700", c.expiry)
	}
	if err != nil {
		_, _ = fmt.Fprintf(cmp.Or(cfg.summary, io.Discard), "warning: unparsable github-authentication-token-expiration header\n")
		return nil
	}
	if exp.Sub(cfg.now) < 14*24*time.Hour {
		return fmt.Errorf("SOURCEMAPS_TOKEN expires within 14 days (%s)", exp.UTC().Format(time.RFC3339))
	}
	return nil
}

func runRetention(ctx context.Context, c *apiClient, currentSHA string) error {
	rels, err := c.listReleases(ctx)
	if err != nil {
		return err
	}
	now, sum, deletedIDs, bySHA := c.cfg.now, cmp.Or(c.cfg.summary, io.Discard), make(map[int64]bool), make(map[string][]parsedRel)
	var delDrafts []ghRelease
	for _, r := range rels {
		p, ok := parseReleaseName(r.Name)
		switch {
		case r.Prerelease:
			_, _ = fmt.Fprintf(sum, "warning: prerelease %q (id %d)\n", r.Name, r.ID)
		case r.Name != r.TagName:
			_, _ = fmt.Fprintf(sum, "warning: name/tag mismatch %q != %q (id %d)\n", r.Name, r.TagName, r.ID)
		case !ok:
			_, _ = fmt.Fprintf(sum, "warning: unparsable release %q (id %d)\n", r.Name, r.ID)
		case p.Start.After(now.Add(24 * time.Hour)):
			_, _ = fmt.Fprintf(sum, "warning: future-dated release %q (id %d)\n", r.Name, r.ID)
		case r.Draft && now.Sub(p.Start) > 7*24*time.Hour:
			_, _ = fmt.Fprintf(sum, "warning: abandoned draft %q (id %d)\n", r.Name, r.ID)
			active := p.SHA == currentSHA
			if !active {
				if active, err = c.hasActiveRun(ctx, p.SHA); err != nil {
					return err
				}
			}
			if !active {
				delDrafts = append(delDrafts, r)
			}
		case r.Draft:
			_, _ = fmt.Fprintf(sum, "warning: younger draft %q (id %d)\n", r.Name, r.ID)
		default:
			bySHA[p.SHA] = append(bySHA[p.SHA], parsedRel{r, p})
		}
	}
	for _, r := range delDrafts {
		if err := c.deleteReleaseByID(ctx, r); err != nil {
			return err
		}
		deletedIDs[r.ID] = true
	}
	var shas []string
	for s, list := range bySHA {
		slices.SortFunc(list, func(a, b parsedRel) int {
			return cmp.Or(compareOrder(b.parsed, a.parsed), cmp.Compare(b.rel.ID, a.rel.ID))
		})
		bySHA[s], shas = list, append(shas, s)
	}
	slices.SortFunc(shas, func(a, b string) int {
		return cmp.Or(compareOrder(bySHA[b][0].parsed, bySHA[a][0].parsed), cmp.Compare(bySHA[b][0].rel.ID, bySHA[a][0].rel.ID))
	})
	for idx := len(shas) - 1; idx >= 10; idx-- {
		if s := shas[idx]; now.Sub(bySHA[s][0].parsed.Start) > 90*24*time.Hour {
			for i := len(bySHA[s]) - 1; i >= 0; i-- {
				item := bySHA[s][i]
				if err := c.deleteReleaseByID(ctx, item.rel); err != nil {
					return err
				}
				deletedIDs[item.rel.ID] = true
				if err := c.deleteUnreferencedTag(ctx, item.rel.TagName, rels, deletedIDs); err != nil {
					return err
				}
			}
		}
	}
	var orphanCands []string
	for page := 1; ; page++ {
		if page > maxReleasePages {
			return fmt.Errorf("listing tags: exceeded %d pages", maxReleasePages)
		}
		var batch []struct{ Name string }
		if code, _, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/tags?per_page=100&page=%d", c.cfg.storeRepo, page), "", nil, &batch); err != nil || code != 200 {
			return fmt.Errorf("listing tags: status %d (%v)", code, err)
		}
		for _, t := range batch {
			if p, ok := parseReleaseName(t.Name); ok && now.Sub(p.Start) > 7*24*time.Hour {
				orphanCands = append(orphanCands, t.Name)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	for _, tag := range orphanCands {
		if err := c.deleteUnreferencedTag(ctx, tag, rels, deletedIDs); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(cmp.Or(c.cfg.stdout, io.Discard), "retention summary: deleted %d releases, %d tags\n", c.delRels, c.delTags)
	return nil
}

func runLookup(ctx context.Context, cfg config, sha, bundleSHA256, runID string) (string, string, []byte, error) {
	return lookupWithClient(ctx, &apiClient{cfg: cfg}, sha, bundleSHA256, runID)
}

func lookupWithClient(ctx context.Context, c *apiClient, sha, bundleSHA256, runID string) (string, string, []byte, error) {
	if !shaRe.MatchString(sha) || (runID != "" && !numIDRe.MatchString(runID)) || (bundleSHA256 != "" && !sha256Re.MatchString(bundleSHA256)) || c.cfg.token == "" || c.cfg.now.IsZero() {
		return "", "", nil, fmt.Errorf("invalid lookup configuration or arguments")
	}
	rels, err := c.listReleases(ctx)
	if err != nil {
		return "", "", nil, err
	}
	var cands []parsedRel
	for _, r := range rels {
		p, ok := parseReleaseName(r.Name)
		if ok && !r.Draft && !r.Prerelease && r.Name == r.TagName && p.SHA == sha && !p.Start.After(c.cfg.now.Add(24*time.Hour)) && (runID == "" || strconv.FormatUint(p.RunID, 10) == runID) {
			cands = append(cands, parsedRel{r, p})
		}
	}
	if len(cands) == 0 {
		return "", "", nil, fmt.Errorf("no published release found for %s", sha)
	}
	slices.SortFunc(cands, func(a, b parsedRel) int {
		return cmp.Or(compareOrder(b.parsed, a.parsed), cmp.Compare(b.rel.ID, a.rel.ID))
	})
	var lastErr error
candLoop:
	for _, cand := range cands {
		best, p := cand.rel, cand.parsed
		assets := make(map[string]ghAsset, len(best.Assets))
		for _, a := range best.Assets {
			if !validAsset(a) {
				lastErr = fmt.Errorf("invalid asset %q in %q", a.Name, best.Name)
				continue candLoop
			}
			assets[a.Name] = a
		}
		idxAsset, ok := assets["index.json"]
		if !ok {
			lastErr = fmt.Errorf("missing index.json in %q", best.Name)
			continue
		}
		code, idxBytes, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/assets/%d", c.cfg.storeRepo, idxAsset.ID), "application/octet-stream", nil, nil)
		var m manifest
		if err != nil || code != 200 || int64(len(idxBytes)) != idxAsset.Size || json.Unmarshal(idxBytes, &m) != nil || m.Commit != p.SHA || m.RunID != strconv.FormatUint(p.RunID, 10) || len(m.Entries) == 0 || len(assets) != 1+len(m.Entries) {
			lastErr = fmt.Errorf("reading or verifying index.json from %q: status %d (%v)", best.Name, code, err)
			continue
		}
		seen := make(map[string]bool, len(m.Entries)*2)
		for _, k := range slices.Sorted(maps.Keys(m.Entries)) {
			e := m.Entries[k]
			if !validBundleEntry(e) || seen[e.File] || seen[e.Map] || assets[e.Map].ID == 0 {
				lastErr = fmt.Errorf("incomplete or invalid manifest entry %q in %q", e.Map, best.Name)
				continue candLoop
			}
			seen[e.File], seen[e.Map] = true, true
		}
		if bundleSHA256 == "" {
			return best.Name, "index.json", idxBytes, nil
		}
		for _, k := range slices.Sorted(maps.Keys(m.Entries)) {
			if e := m.Entries[k]; e.SHA256 == bundleSHA256 {
				ma := assets[e.Map]
				code, mapBytes, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/assets/%d", c.cfg.storeRepo, ma.ID), "application/octet-stream", nil, nil)
				if err != nil || code != 200 || int64(len(mapBytes)) != ma.Size || !bytes.Contains(mapBytes, []byte(`"sourcesContent"`)) {
					return "", "", nil, fmt.Errorf("downloading map %q: status %d (%v)", e.Map, code, err)
				}
				return best.Name, e.Map, mapBytes, nil
			}
		}
		return "", "", nil, fmt.Errorf("bundle hash %s not found in %q", bundleSHA256, best.Name)
	}
	return "", "", nil, lastErr
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
		cfg.now = time.Now().UTC()
		a := append(args, "", "")
		rel, name, data, lerr := runLookup(context.Background(), cfg, a[0], a[1], a[2])
		if err = lerr; err == nil {
			_, _ = fmt.Fprintf(os.Stdout, "%q %q (%d bytes)\n", rel, name, len(data))
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
