package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type mockAsset struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	State string `json:"state,omitempty"`
	data  []byte
}

type mockRelease struct {
	ID              int64       `json:"id"`
	TagName         string      `json:"tag_name"`
	Name            string      `json:"name"`
	TargetCommitish string      `json:"target_commitish"`
	Draft           bool        `json:"draft"`
	Prerelease      bool        `json:"prerelease"`
	Assets          []mockAsset `json:"assets"`
}

type mockStore struct {
	token, expiryHeader, tamperManifest, failTagDel, failTagDel422, tagDel404, tagDel422, recheckTagRel, failRunsSHA string
	releases                                                                                                         []mockRelease
	tags, events                                                                                                     []string
	activeSHAs                                                                                                       map[string]bool
	runCounts                                                                                                        map[string]int
	runsTotalCount                                                                                                   int
	failUpload                                                                                                       bool
	failRelDelID, relDel404ID, wrongLookupID, nextID                                                                 int64
}

func (s *mockStore) server(t *testing.T) *httptest.Server {
	t.Helper()
	s.nextID = max(s.nextID, 100)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/tkakkie/ribbitto/actions/runs") {
			if r.Header.Get("Authorization") != "" {
				t.Errorf("store token leaked to source repo: %s", r.URL.Path)
			}
			headSHA := r.URL.Query().Get("head_sha")
			if headSHA == s.failRunsSHA {
				http.Error(w, "runs check failed", 500)
				return
			}
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			nRuns := cmp.Or(s.runCounts[headSHA], 100)
			var allRuns []map[string]string
			for i := range nRuns {
				if i%2 == 0 {
					allRuns = append(allRuns, map[string]string{"status": "in_progress", "path": ".github/workflows/ci.yml"})
				} else {
					allRuns = append(allRuns, map[string]string{"status": "completed", "path": ".github/workflows/sourcemaps-store.yml"})
				}
			}
			if s.activeSHAs[headSHA] {
				allRuns = append(allRuns, map[string]string{"status": "in_progress", "path": ".github/workflows/sourcemaps-store.yml"})
			}
			capped := allRuns[:min(len(allRuns), 1000)]
			start := min((max(p, 1)-1)*100, len(capped))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count":   cmp.Or(s.runsTotalCount, len(allRuns)),
				"workflow_runs": capped[start:min(start+100, len(capped))],
			})
			return
		}
		if s.expiryHeader != "" {
			w.Header().Set("github-authentication-token-expiration", s.expiryHeader)
		}
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "unauthorized", 401)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/repos/tkakkie/ribbitto-sourcemaps")
		switch {
		case r.Method == "GET" && path == "/releases":
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			start := min((max(p, 1)-1)*100, len(s.releases))
			_ = json.NewEncoder(w).Encode(s.releases[start:min(start+100, len(s.releases))])
		case r.Method == "POST" && path == "/releases":
			var req mockRelease
			_ = json.NewDecoder(r.Body).Decode(&req)
			if r.Header.Get("Content-Type") != "application/json" || !req.Draft || req.Prerelease || req.TargetCommitish != "main" {
				http.Error(w, "bad draft or unknown target_commitish", 422)
				return
			}
			s.nextID++
			req.ID = s.nextID
			s.releases, s.events = append(s.releases, req), append(s.events, "draft:"+req.Name)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(req)
		case r.Method == "POST" && strings.HasPrefix(path, "/releases/") && strings.HasSuffix(path, "/assets"):
			if r.Header.Get("Content-Type") != "application/octet-stream" || s.failUpload {
				http.Error(w, "upload refused", cmp.Or(map[bool]int{true: 415}[r.Header.Get("Content-Type") != "application/octet-stream"], 500))
				return
			}
			id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/releases/"), "/assets"), 10, 64)
			name := r.URL.Query().Get("name")
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r.Body)
			for i := range s.releases {
				if s.releases[i].ID == id {
					s.nextID++
					s.releases[i].Assets = append(s.releases[i].Assets, mockAsset{ID: s.nextID, Name: name, Size: int64(buf.Len()), data: buf.Bytes()})
					if s.tamperManifest == "extra" && name == "index.json" {
						s.nextID++
						s.releases[i].Assets = append(s.releases[i].Assets, mockAsset{ID: s.nextID, Name: "extra.js.map", Size: 2, data: []byte("{}")})
					} else if s.tamperManifest == "drop" && strings.HasSuffix(name, ".js.map") {
						s.releases[i].Assets = s.releases[i].Assets[:len(s.releases[i].Assets)-1]
					} else if s.tamperManifest == "zero-second" && name == "b-second.js.map" {
						s.releases[i].Assets[len(s.releases[i].Assets)-1] = mockAsset{ID: s.nextID, Name: name, Size: 0}
					} else if s.tamperManifest == "sub-index" && name == "index.json" {
						last := &s.releases[i].Assets[len(s.releases[i].Assets)-1]
						last.data = bytes.ReplaceAll(last.data, []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), []byte(strings.Repeat("f", 64)))
					}
					s.events = append(s.events, "upload:"+name)
					w.WriteHeader(201)
					_ = json.NewEncoder(w).Encode(mockAsset{ID: s.nextID, Name: name, Size: int64(buf.Len())})
					return
				}
			}
			http.NotFound(w, r)
		case r.Method == "GET" && strings.HasPrefix(path, "/releases/assets/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/releases/assets/"), 10, 64)
			for _, rel := range s.releases {
				for _, a := range rel.Assets {
					if a.ID == id {
						_, _ = w.Write(a.data)
						return
					}
				}
			}
			http.NotFound(w, r)
		case r.Method == "GET" && strings.HasPrefix(path, "/releases/tags/"):
			tag := strings.TrimPrefix(path, "/releases/tags/")
			if tag == s.recheckTagRel {
				_ = json.NewEncoder(w).Encode(mockRelease{ID: 999, Name: tag, TagName: tag})
				return
			}
			for _, rel := range s.releases {
				if !rel.Draft && rel.TagName == tag {
					_ = json.NewEncoder(w).Encode(rel)
					return
				}
			}
			http.NotFound(w, r)
		case strings.HasPrefix(path, "/releases/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/releases/"), 10, 64)
			if r.Method == "GET" && id == s.wrongLookupID {
				_ = json.NewEncoder(w).Encode(mockRelease{ID: id + 999, Name: "wrong", TagName: "wrong"})
				return
			}
			for i := range s.releases {
				if s.releases[i].ID != id {
					continue
				}
				switch r.Method {
				case "GET":
					rel := s.releases[i]
					switch s.tamperManifest {
					case "draft-not-draft":
						rel.Draft = false
					case "draft-prerelease":
						rel.Prerelease = true
					case "draft-bad-name":
						rel.Name = "wrong"
					case "draft-bad-tag":
						rel.TagName = "wrong"
					}
					_ = json.NewEncoder(w).Encode(rel)
				case "PATCH":
					if r.Header.Get("Content-Type") != "application/json" {
						http.Error(w, "expected application/json", 415)
						return
					}
					var p struct{ Draft, Prerelease bool }
					_ = json.NewDecoder(r.Body).Decode(&p)
					s.releases[i].Draft, s.releases[i].Prerelease = p.Draft || s.tamperManifest == "pub-still-draft", p.Prerelease || s.tamperManifest == "pub-prerelease"
					if s.tamperManifest == "pub-bad-name" {
						s.releases[i].Name = "wrong"
					} else if s.tamperManifest == "pub-bad-tag" {
						s.releases[i].TagName = "wrong"
					} else if s.tamperManifest == "corrupt-second-after-publish" {
						for j := range s.releases[i].Assets {
							if s.releases[i].Assets[j].Name == "b-second.js.map" {
								s.releases[i].Assets[j].data = []byte(`{"sourcesContent":["X"]}`)
							}
						}
					}
					if !p.Draft && !slices.Contains(s.tags, s.releases[i].TagName) {
						s.tags = append(s.tags, s.releases[i].TagName)
					}
					s.events = append(s.events, "publish:"+s.releases[i].Name)
					_ = json.NewEncoder(w).Encode(s.releases[i])
					if s.tamperManifest == "post-publish-tag-mismatch" {
						s.releases[i].TagName = "wrong"
					}
				case "DELETE":
					if id == s.failRelDelID {
						http.Error(w, "refused", 500)
						return
					}
					s.events, s.releases = append(s.events, fmt.Sprintf("del-rel:%d", id)), slices.Delete(s.releases, i, i+1)
					if id == s.relDel404ID {
						http.NotFound(w, r)
						return
					}
					w.WriteHeader(204)
				}
				return
			}
			http.NotFound(w, r)
		case r.Method == "GET" && path == "/tags":
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			start := min((max(p, 1)-1)*100, len(s.tags))
			var out []map[string]string
			for _, tag := range s.tags[start:min(start+100, len(s.tags))] {
				out = append(out, map[string]string{"name": tag})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == "GET" && strings.HasPrefix(path, "/git/ref/tags/"):
			if tag := strings.TrimPrefix(path, "/git/ref/tags/"); slices.Contains(s.tags, tag) {
				_ = json.NewEncoder(w).Encode(map[string]string{"ref": "refs/tags/" + tag})
				return
			}
			http.NotFound(w, r)
		case r.Method == "DELETE" && strings.HasPrefix(path, "/git/refs/tags/"):
			tag := strings.TrimPrefix(path, "/git/refs/tags/")
			if tag == s.failTagDel || tag == s.failTagDel422 {
				http.Error(w, "refused", cmp.Or(map[bool]int{true: 422}[tag == s.failTagDel422], 500))
				return
			}
			s.events = append(s.events, "del-tag:"+tag)
			if idx := slices.Index(s.tags, tag); idx >= 0 {
				s.tags = slices.Delete(s.tags, idx, idx+1)
				if tag == s.tagDel404 {
					http.NotFound(w, r)
					return
				}
				if tag != s.tagDel422 {
					w.WriteHeader(204)
					return
				}
			}
			http.Error(w, `{"message":"Reference does not exist"}`, 422)
		default:
			http.NotFound(w, r)
		}
	}))
}

func testCfg(apiBase, token string, now time.Time, stdout io.Writer) config {
	return config{apiBase: apiBase, uploadBase: apiBase, storeRepo: prodStoreRepo, sourceRepo: prodSourceRepo, token: token, now: now, stdout: stdout, allowTestEndpoint: true}
}

func shaOf(n int) string { return fmt.Sprintf("%040x", n) }

func relNameAt(t time.Time, sha string, run, attempt int) string {
	return fmt.Sprintf("map-%s-%s-%d.%d", t.UTC().Format("20060102T150405Z"), sha, run, attempt)
}

func writeMapDir(t *testing.T, sha, runID string) (string, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	const file, bundleHash = "message-time-ABCDEF12.js", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	mapBytes := []byte(`{"version":3,"sources":["message-time.ts"],"sourcesContent":["console.log(1)"]}`)
	idx := fmt.Sprintf(`{"commit":%q,"run_id":%q,"entries":{"message-time":{"file":%q,"sha256":%q,"map":%q}}}`, sha, runID, file, bundleHash, file+".map")
	if cmp.Or(os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0644), os.WriteFile(filepath.Join(dir, file+".map"), mapBytes, 0644)) != nil {
		t.Fatal("writing map dir")
	}
	return dir, bundleHash, mapBytes
}

func TestStoreCoreAndLookup(t *testing.T) {
	now, day, currSHA := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), 24*time.Hour, shaOf(1)
	dir, bundleHash, wantMap := writeMapDir(t, currSHA, "500")
	oldTag := relNameAt(now.Add(-95*day), shaOf(11), 9, 1)
	oldTag404 := relNameAt(now.Add(-100*day), shaOf(11), 8, 1)
	oldTag500 := relNameAt(now.Add(-105*day), shaOf(11), 7, 1)
	oldSharedDraftTag := relNameAt(now.Add(-110*day), shaOf(11), 6, 1)
	oldSharedPubTag := relNameAt(now.Add(-115*day), shaOf(11), 5, 1)
	var rels []mockRelease
	var tags []string
	addPub := func(id int64, ts time.Time, sha string, run, att int) string {
		name := relNameAt(ts, sha, run, att)
		rels, tags = append(rels, mockRelease{ID: id, Name: name, TagName: name}), append(tags, name)
		return name
	}
	for i := 2; i <= 10; i++ {
		addPub(int64(i), now.Add(-10*day), shaOf(i), 100+i, 1)
	}
	for i := range 95 {
		d := relNameAt(now.Add(-time.Duration(i+1)*time.Hour), shaOf(200+i), i+1, 1)
		rels, tags = append(rels, mockRelease{ID: int64(200 + i), Name: d, TagName: d, Draft: true}), append(tags, d)
	}
	dupOld, kept89d := addPub(20, now.Add(-100*day), shaOf(2), 50, 1), addPub(13, now.Add(-90*day), shaOf(12), 7, 1)
	dup12Old, oldBoundary := addPub(21, now.Add(-100*day), shaOf(12), 6, 1), addPub(18, now.Add(-90*day-time.Second), shaOf(13), 1, 1)
	draftDel, draftActive, draftYoung := relNameAt(now.Add(-7*day-time.Second), shaOf(50), 1, 1), relNameAt(now.Add(-9*day), shaOf(99), 1, 1), relNameAt(now.Add(-7*day), shaOf(51), 1, 1)
	draftCurr := relNameAt(now.Add(-8*day), currSHA, 1, 1)
	preTag, subName := relNameAt(now.Add(-120*day), shaOf(60), 1, 1), "prefix-"+relNameAt(now.Add(-120*day), shaOf(61), 1, 1)
	mismatchTag, futureTag := relNameAt(now.Add(-120*day), shaOf(62), 1, 1), relNameAt(now.Add(2*day), shaOf(64), 1, 1)
	orphanOld, orphanRecheck, orphanYoung := relNameAt(now.Add(-7*day-time.Second), shaOf(70), 1, 1), relNameAt(now.Add(-8*day), shaOf(71), 1, 1), relNameAt(now.Add(-7*day), shaOf(72), 1, 1)
	rels = append(rels,
		mockRelease{ID: 11, Name: oldTag, TagName: oldTag},
		mockRelease{ID: 12, Name: oldTag404, TagName: oldTag404},
		mockRelease{ID: 15, Name: oldTag500, TagName: oldTag500},
		mockRelease{ID: 16, Name: oldSharedDraftTag, TagName: oldSharedDraftTag},
		mockRelease{ID: 17, Name: oldSharedPubTag, TagName: oldSharedPubTag},
		mockRelease{ID: 30, Name: draftDel, TagName: draftDel, Draft: true},
		mockRelease{ID: 31, Name: draftDel, TagName: draftDel, Draft: true},
		mockRelease{ID: 32, Name: draftActive, TagName: draftActive, Draft: true},
		mockRelease{ID: 33, Name: draftYoung, TagName: draftYoung, Draft: true},
		mockRelease{ID: 34, Name: "draft-non-strict", TagName: "draft-non-strict", Draft: true},
		mockRelease{ID: 35, Name: "draft-sharing-tag", TagName: oldSharedDraftTag, Draft: true},
		mockRelease{ID: 36, Name: draftCurr, TagName: draftCurr, Draft: true},
		mockRelease{ID: 40, Name: preTag, TagName: preTag, Prerelease: true},
		mockRelease{ID: 41, Name: "map-bad-name", TagName: "map-bad-name"},
		mockRelease{ID: 42, Name: subName, TagName: subName},
		mockRelease{ID: 43, Name: relNameAt(now.Add(-120*day), shaOf(63), 1, 1), TagName: mismatchTag},
		mockRelease{ID: 44, Name: futureTag, TagName: futureTag},
		mockRelease{ID: 45, Name: relNameAt(now.Add(-5*day), shaOf(65), 1, 1), TagName: oldSharedPubTag},
		mockRelease{ID: 46, Name: "##[error]forged", TagName: "##[error]forged", Prerelease: true},
		mockRelease{ID: 47, Name: "##[add-mask]secret", TagName: "::error::forged\n::add-mask::secret\r::stop-commands::x"},
		mockRelease{ID: 48, Name: "###[error]overlap\n\r:::stop-commands:::x", TagName: "###[error]overlap\n\r:::stop-commands:::x", Draft: true},
	)
	tags = append(tags, oldTag, oldTag404, oldTag500, oldSharedDraftTag, oldSharedPubTag, draftActive, draftYoung, draftCurr, preTag, "map-bad-name", subName, mismatchTag, futureTag, orphanOld, orphanRecheck, orphanYoung)
	store := &mockStore{
		token: "test-token", expiryHeader: now.Add(14 * day).Format("2006-01-02 15:04:05 UTC"),
		releases: rels, tags: tags, activeSHAs: map[string]bool{shaOf(99): true},
		runCounts:   map[string]int{shaOf(50): 999},
		relDel404ID: 12, tagDel404: oldTag404, tagDel422: oldTag500, recheckTagRel: orphanRecheck,
	}
	srv := store.server(t)
	defer srv.Close()

	var out, summary bytes.Buffer
	cfg := testCfg(srv.URL, "test-token", now, &out)
	cfg.summary = &summary
	if err := runStore(t.Context(), cfg, dir, currSHA, "500", "2"); err != nil {
		t.Fatalf("runStore failed: %v", err)
	}
	wantRel := relNameAt(now, currSHA, 500, 2)
	draftIdx, pubIdx, delIdx := slices.Index(store.events, "draft:"+wantRel), slices.Index(store.events, "publish:"+wantRel), slices.Index(store.events, "del-rel:11")
	delTag11, delRel12, delTag12, delRel15, delTag15 := slices.Index(store.events, "del-tag:"+oldTag), slices.Index(store.events, "del-rel:12"), slices.Index(store.events, "del-tag:"+oldTag404), slices.Index(store.events, "del-rel:15"), slices.Index(store.events, "del-tag:"+oldTag500)
	if !strings.Contains(out.String(), wantRel) || draftIdx < 0 || pubIdx <= draftIdx || delIdx <= pubIdx || delTag11 <= delIdx || delRel12 <= pubIdx || delTag12 <= delRel12 || delRel15 <= pubIdx || delTag15 <= delRel15 {
		t.Fatalf("unexpected output or lifecycle order: %s / %v", out.String(), store.events)
	}
	hasRel := func(id int64) bool {
		return slices.ContainsFunc(store.releases, func(r mockRelease) bool { return r.ID == id })
	}
	for _, gone := range []int64{11, 12, 15, 16, 17, 18, 30, 31} {
		if hasRel(gone) {
			t.Errorf("release ID %d was not deleted", gone)
		}
	}
	for _, kept := range []int64{2, 20, 13, 21, 32, 33, 34, 35, 36, 40, 41, 42, 43, 44, 45, 46, 47, 48} {
		if !hasRel(kept) {
			t.Errorf("release ID %d was wrongly deleted", kept)
		}
	}
	for _, goneTag := range []string{oldTag, oldTag404, oldTag500, oldBoundary, orphanOld} {
		if slices.Contains(store.tags, goneTag) {
			t.Errorf("tag %s was not deleted", goneTag)
		}
	}
	for _, keptTag := range []string{wantRel, dupOld, kept89d, dup12Old, oldSharedDraftTag, oldSharedPubTag, draftActive, draftYoung, draftCurr, preTag, "map-bad-name", subName, mismatchTag, futureTag, orphanRecheck, orphanYoung} {
		if !slices.Contains(store.tags, keptTag) {
			t.Errorf("tag %s was wrongly deleted", keptTag)
		}
	}
	for _, warn := range []string{
		fmt.Sprintf("abandoned draft %q", draftDel),
		fmt.Sprintf("younger draft %q", draftYoung),
		fmt.Sprintf("prerelease %q", preTag),
		`unparsable release "map-bad-name"`,
		fmt.Sprintf("name/tag mismatch %q != %q", relNameAt(now.Add(-120*day), shaOf(63), 1, 1), mismatchTag),
		fmt.Sprintf("future-dated release %q", futureTag),
	} {
		if !strings.Contains(summary.String(), warn) {
			t.Errorf("job summary missing %q warning:\n%s", warn, summary.String())
		}
	}
	wfBytes, err := os.ReadFile("../../.github/workflows/sourcemaps-store.yml")
	wfText := string(wfBytes)
	wfSteps := strings.Split(wfText, "\n      - ")
	if err != nil || len(wfSteps) < 2 ||
		!strings.Contains(wfText, "on:\n  push:\n    branches: [main]\n\npermissions:\n  contents: read\n") ||
		!strings.Contains(wfText, "group: sourcemaps-${{ github.sha }}\n  cancel-in-progress: false\n") ||
		!strings.Contains(wfText, "if: github.repository == 'tkakkie/ribbitto'\n") ||
		!strings.Contains(wfText, "environment: sourcemaps\n") ||
		!strings.Contains(wfText, "persist-credentials: false\n") ||
		!strings.Contains(wfText, "cache: false\n") ||
		strings.Contains(wfText, "cache-dependency-path") ||
		strings.Contains(wfText, "pull_request") ||
		strings.Contains(wfText, "workflow_dispatch") ||
		strings.Count(wfText, "secrets.") != 1 ||
		strings.Count(wfText, "SOURCEMAPS_TOKEN") != 2 ||
		strings.Contains(strings.Join(wfSteps[:len(wfSteps)-1], "\n"), "SOURCEMAPS_TOKEN") ||
		strings.Contains(strings.Join(wfSteps[:len(wfSteps)-1], "\n"), "secrets.") ||
		!strings.Contains(wfSteps[len(wfSteps)-1], "SOURCEMAPS_TOKEN: ${{ secrets.SOURCEMAPS_TOKEN }}") ||
		!strings.Contains(wfSteps[len(wfSteps)-1], "run: make sourcemaps-store") {
		t.Fatalf("workflow shape check failed:\n%s", wfText)
	}
	for _, wantLog := range []string{
		fmt.Sprintf("deleted release %q (id 11, sha %s, age 95d)\n", oldTag, shaOf(11)),
		fmt.Sprintf("deleted release %q (id 30, sha %s, age 7d)\n", draftDel, shaOf(50)),
		fmt.Sprintf("deleted tag %q (sha %s, age 95d)\n", oldTag, shaOf(11)),
		fmt.Sprintf("deleted tag %q (sha %s, age 7d)\n", orphanOld, shaOf(70)),
		"retention summary: deleted 7 releases, 3 tags\n",
	} {
		if !strings.Contains(out.String(), wantLog) {
			t.Errorf("stdout missing deletion log %q:\n%s", wantLog, out.String())
		}
	}
	for _, absentLog := range []string{
		fmt.Sprintf("deleted release %q", oldTag404),
		fmt.Sprintf("deleted tag %q", oldTag404),
		fmt.Sprintf("deleted tag %q", oldTag500),
		store.token,
		store.expiryHeader,
	} {
		if strings.Contains(out.String(), absentLog) || strings.Contains(summary.String(), absentLog) {
			t.Errorf("unexpected log or leaked secret/header %q in output", absentLog)
		}
	}
	for _, run := range []struct{ id, att int }{{9, 1}, {10, 1}, {510, 9}, {510, 10}} {
		dirN, _, _ := writeMapDir(t, currSHA, strconv.Itoa(run.id))
		if err := runStore(t.Context(), cfg, dirN, currSHA, strconv.Itoa(run.id), strconv.Itoa(run.att)); err != nil {
			t.Fatalf("rerun store %v failed: %v", run, err)
		}
	}
	for i := range store.releases {
		if store.releases[i].Name == relNameAt(now, currSHA, 510, 9) {
			store.releases[i].ID = 9999
		}
	}
	store.releases = append([]mockRelease{
		{ID: 901, Name: relNameAt(now.Add(time.Hour), currSHA, 600, 1), TagName: relNameAt(now.Add(time.Hour), currSHA, 600, 1), Draft: true},
		{ID: 902, Name: relNameAt(now.Add(2*time.Hour), currSHA, 601, 1), TagName: relNameAt(now.Add(2*time.Hour), currSHA, 601, 1), Prerelease: true},
		{ID: 903, Name: relNameAt(now.Add(3*time.Hour), currSHA, 602, 1), TagName: relNameAt(now.Add(3*time.Hour), currSHA, 601, 1)},
		{ID: 904, Name: relNameAt(now.Add(25*time.Hour), currSHA, 603, 1), TagName: relNameAt(now.Add(25*time.Hour), currSHA, 603, 1)},
	}, store.releases...)
	if gotRel, gotIdx, _, err := runLookup(t.Context(), cfg, currSHA, "", "", ""); err != nil || gotRel != relNameAt(now, currSHA, 510, 10) || gotIdx != "index.json" {
		t.Fatalf("lookup by SHA alone failed: rel=%s asset=%s err=%v", gotRel, gotIdx, err)
	}
	if gotRel, gotMapName, gotMap, err := runLookup(t.Context(), cfg, currSHA, bundleHash, "", ""); err != nil || gotRel != relNameAt(now, currSHA, 510, 10) || gotMapName != "message-time-ABCDEF12.js.map" || !bytes.Equal(gotMap, wantMap) {
		t.Fatalf("lookup failed: rel=%s map=%s err=%v", gotRel, gotMapName, err)
	}
	if gotRel9, _, _, err := runLookup(t.Context(), cfg, currSHA, bundleHash, "9", ""); err != nil || gotRel9 != relNameAt(now, currSHA, 9, 1) {
		t.Fatalf("lookup with runID=9 failed: rel=%s err=%v", gotRel9, err)
	}
	c := &apiClient{cfg: cfg}
	tagInjecErr := c.deleteTagByRef(t.Context(), "##[error]bad\n::add-mask::secret\r::stop-commands::x")
	if tagInjecErr == nil ||
		c.deleteTagByRef(t.Context(), "map-20261102T120000Z-"+currSHA+"-01.1") == nil ||
		c.deleteTagByRef(t.Context(), "map-20261102T120000Z-abc-1.1") == nil ||
		c.deleteTagByRef(t.Context(), "non-canonical") == nil {
		t.Fatal("expected exact-ID deletion guard to refuse invalid tag")
	}
	var rawLog bytes.Buffer
	logf(&rawLog, "raw %s\n", "line1\n::error::nl\r##[add-mask]secret\r\n::stop-commands::x")
	combinedLogs := out.String() + summary.String() + tagInjecErr.Error() + rawLog.String()
	for _, wantID := range []string{"(id 46)\n", "(id 47)\n", "(id 48)\n"} {
		if !strings.Contains(summary.String(), wantID) {
			t.Errorf("summary missing warning for %q:\n%s", wantID, summary.String())
		}
	}
	if strings.Count(rawLog.String(), "\n") != 1 {
		t.Errorf("logf failed to collapse embedded newlines: %q", rawLog.String())
	}
	for _, line := range strings.Split(strings.TrimSuffix(summary.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "warning: ") {
			t.Errorf("embedded newline broke out of warning line: %q", line)
		}
	}
	for _, forbidden := range []string{"##[", "##[error]", "##[add-mask]secret", "::", "::error::", "::add-mask::", "::stop-commands::x", "\r"} {
		if strings.Contains(combinedLogs, forbidden) {
			t.Errorf("log or error output contains unsanitised workflow command introducer %q", forbidden)
		}
	}
	mainSrc, srcErr := os.ReadFile("main.go")
	if srcErr != nil || bytes.Contains(mainSrc, []byte("fmt.Print")) || bytes.Contains(mainSrc, []byte("fmt.Fprint")) || bytes.Contains(mainSrc, []byte("%q")) {
		t.Errorf("main.go must route all logs through logf and all quoted strings through safeQuote (err=%v)", srcErr)
	}
	baseRel := ghRelease{ID: 1, Name: oldTag, TagName: oldTag}
	for _, tc := range []struct {
		in, got ghRelease
	}{
		{ghRelease{ID: 1, Name: "prefix-" + oldTag, TagName: "prefix-" + oldTag}, ghRelease{ID: 1, Name: "prefix-" + oldTag, TagName: "prefix-" + oldTag}},
		{ghRelease{ID: 0, Name: oldTag, TagName: oldTag}, ghRelease{ID: 0, Name: oldTag, TagName: oldTag}},
		{ghRelease{ID: 1, Name: oldTag, TagName: oldTag, Prerelease: true}, baseRel},
		{ghRelease{ID: 1, Name: oldTag, TagName: oldTag404}, ghRelease{ID: 1, Name: oldTag, TagName: oldTag404}},
		{baseRel, ghRelease{ID: 2, Name: oldTag, TagName: oldTag}},
		{baseRel, ghRelease{ID: 1, Name: oldTag404, TagName: oldTag}},
		{baseRel, ghRelease{ID: 1, Name: oldTag, TagName: oldTag404}},
		{baseRel, ghRelease{ID: 1, Name: oldTag, TagName: oldTag, Draft: true}},
		{baseRel, ghRelease{ID: 1, Name: oldTag, TagName: oldTag, Prerelease: true}},
	} {
		delCalled := false
		gSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				delCalled = true
				w.WriteHeader(204)
				return
			}
			_ = json.NewEncoder(w).Encode(tc.got)
		}))
		gClient := &apiClient{cfg: testCfg(gSrv.URL, "tok", now, nil)}
		err := gClient.deleteReleaseByID(t.Context(), tc.in)
		gSrv.Close()
		if err == nil || delCalled {
			t.Fatalf("expected deleteReleaseByID(%+v) with got=%+v to refuse before DELETE, got err=%v delCalled=%v", tc.in, tc.got, err, delCalled)
		}
	}
	for _, nOrphans := range []int{100, 101} {
		var oTags []string
		for i := range nOrphans {
			oTags = append(oTags, relNameAt(now.Add(-8*day), shaOf(800+i), i+1, 1))
		}
		oStore := &mockStore{token: "test-token", expiryHeader: now.Add(20 * day).Format("2006-01-02 15:04:05 UTC"), tags: oTags}
		oSrv := oStore.server(t)
		defer oSrv.Close()
		oCfg := testCfg(oSrv.URL, "test-token", now, &bytes.Buffer{})
		if err := runRetention(t.Context(), &apiClient{cfg: oCfg}, currSHA); err != nil || len(oStore.tags) != 0 {
			t.Fatalf("orphan cleanup for %d tags: remaining=%d err=%v", nOrphans, len(oStore.tags), err)
		}
	}
	tenStore := &mockStore{token: "test-token", expiryHeader: now.Add(20 * day).Format("2006-01-02 15:04:05 UTC")}
	tenSrv := tenStore.server(t)
	defer tenSrv.Close()
	tenCfg := testCfg(tenSrv.URL, "test-token", now.Add(-100*day), &out)
	tenCfg.summary = &summary
	for i := 1; i <= 10; i++ {
		d, _, _ := writeMapDir(t, shaOf(i), strconv.Itoa(i))
		if err := runStore(t.Context(), tenCfg, d, shaOf(i), strconv.Itoa(i), "1"); err != nil {
			t.Fatal(err)
		}
	}
	futureSlot := relNameAt(now.Add(2*day), shaOf(999), 1, 1)
	tenStore.releases, tenStore.tags = append(tenStore.releases, mockRelease{ID: 999, Name: futureSlot, TagName: futureSlot}), append(tenStore.tags, futureSlot)
	tenCfg.now = now
	d1, _, _ := writeMapDir(t, shaOf(1), "99")
	d11, _, _ := writeMapDir(t, shaOf(11), "100")
	oldSHA1, oldSHA2 := relNameAt(now.Add(-100*day), shaOf(1), 1, 1), relNameAt(now.Add(-100*day), shaOf(2), 2, 1)
	if len(tenStore.releases) != 11 || runStore(t.Context(), tenCfg, d1, shaOf(1), "99", "1") != nil || len(tenStore.releases) != 12 || runStore(t.Context(), tenCfg, d11, shaOf(11), "100", "1") != nil || len(tenStore.releases) != 12 || !slices.Contains(tenStore.tags, oldSHA1) || !slices.Contains(tenStore.tags, futureSlot) || slices.Contains(tenStore.tags, oldSHA2) {
		t.Fatalf("unexpected distinct-SHA slot retention: %d releases, tags=%v", len(tenStore.releases), tenStore.tags)
	}
}

func TestStoreCoreFailures(t *testing.T) {
	now, day, currSHA := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), 24*time.Hour, shaOf(1)
	dir, _, _ := writeMapDir(t, currSHA, "10")
	oldExpired := func() ([]mockRelease, []string) {
		var r []mockRelease
		var tags []string
		for i := 2; i <= 12; i++ {
			name := relNameAt(now.Add(-100*day), shaOf(i), i, 1)
			r, tags = append(r, mockRelease{ID: int64(i), Name: name, TagName: name}), append(tags, name)
		}
		return r, tags
	}
	for _, tc := range []struct {
		name, token, wantErr string
		mod                  func(*mockStore)
	}{
		{"rejected token", "wrong", "401", func(*mockStore) {}},
		{"upload failure", "tok", "500", func(s *mockStore) { s.failUpload = true }},
		{"manifest mismatch extra asset", "tok", "manifest", func(s *mockStore) { s.tamperManifest = "extra" }},
		{"manifest mismatch missing asset", "tok", "manifest", func(s *mockStore) { s.tamperManifest = "drop" }},
		{"substituted manifest with altered hash", "tok", "manifest", func(s *mockStore) { s.tamperManifest = "sub-index" }},
		{"draft verification not draft", "tok", "verifying draft", func(s *mockStore) { s.tamperManifest = "draft-not-draft" }},
		{"draft verification prerelease", "tok", "verifying draft", func(s *mockStore) { s.tamperManifest = "draft-prerelease" }},
		{"draft verification bad name", "tok", "verifying draft", func(s *mockStore) { s.tamperManifest = "draft-bad-name" }},
		{"draft verification bad tag", "tok", "verifying draft", func(s *mockStore) { s.tamperManifest = "draft-bad-tag" }},
		{"publish response still draft", "tok", "publishing release", func(s *mockStore) { s.tamperManifest = "pub-still-draft" }},
		{"publish response prerelease", "tok", "publishing release", func(s *mockStore) { s.tamperManifest = "pub-prerelease" }},
		{"publish response bad name", "tok", "publishing release", func(s *mockStore) { s.tamperManifest = "pub-bad-name" }},
		{"publish response bad tag", "tok", "publishing release", func(s *mockStore) { s.tamperManifest = "pub-bad-tag" }},
		{"post-publish lookup verification", "tok", "verifying published release", func(s *mockStore) { s.tamperManifest = "post-publish-tag-mismatch" }},
		{"refused release deletion", "tok", "500", func(s *mockStore) { s.failRelDelID = 2 }},
		{"active run check fails closed", "tok", "checking concurrency group", func(s *mockStore) {
			d1, d2 := relNameAt(now.Add(-8*day), shaOf(97), 1, 1), relNameAt(now.Add(-8*day), shaOf(98), 1, 1)
			s.releases = append(s.releases, mockRelease{ID: 30, Name: d1, TagName: d1, Draft: true}, mockRelease{ID: 31, Name: d2, TagName: d2, Draft: true})
			s.failRunsSHA = shaOf(98)
		}},
		{"active run check fails closed on capped 1001 response", "tok", "checking concurrency group", func(s *mockStore) {
			d := relNameAt(now.Add(-8*day), shaOf(98), 1, 1)
			s.releases = append(s.releases, mockRelease{ID: 30, Name: d, TagName: d, Draft: true})
			s.runCounts, s.activeSHAs = map[string]int{shaOf(98): 1000}, map[string]bool{shaOf(98): true}
		}},
		{"active run check fails closed at 1000 cap boundary", "tok", "checking concurrency group", func(s *mockStore) {
			d := relNameAt(now.Add(-8*day), shaOf(98), 1, 1)
			s.releases = append(s.releases, mockRelease{ID: 30, Name: d, TagName: d, Draft: true})
			s.runCounts = map[string]int{shaOf(98): 1000}
		}},
		{"active run check fails closed on incomplete listing", "tok", "checking concurrency group", func(s *mockStore) {
			d := relNameAt(now.Add(-8*day), shaOf(98), 1, 1)
			s.releases = append(s.releases, mockRelease{ID: 30, Name: d, TagName: d, Draft: true})
			s.runsTotalCount = 250
		}},
		{"refused tag deletion 422 while present", "tok", "deleting tag", func(s *mockStore) { s.failTagDel422 = relNameAt(now.Add(-100*day), shaOf(2), 2, 1) }},
		{"wrong lookup release refused", "tok", "mismatch", func(s *mockStore) { s.wrongLookupID = 2 }},
		{"13 days expiry fails after upload and retention", "tok", "expires", func(s *mockStore) { s.expiryHeader = now.Add(14*day - time.Second).Format("2006-01-02 15:04:05 UTC") }},
		{"warn:missing github-authentication-token-expiration", "tok", "", func(s *mockStore) { s.expiryHeader = "" }},
		{"warn:unparsable github-authentication-token-expiration", "tok", "", func(s *mockStore) { s.expiryHeader = "not-a-date" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rels, tags := oldExpired()
			s := &mockStore{token: "tok", expiryHeader: now.Add(15 * day).Format("2006-01-02 15:04:05 UTC"), releases: rels, tags: tags}
			tc.mod(s)
			srv := s.server(t)
			defer srv.Close()
			var out, summary bytes.Buffer
			cfg := testCfg(srv.URL, tc.token, now, &out)
			cfg.summary = &summary
			err := runStore(t.Context(), cfg, dir, currSHA, "10", "1")
			if (tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr))) || (tc.wantErr == "" && err != nil) {
				t.Fatalf("want error %q, got %v", tc.wantErr, err)
			}
			published := slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "publish:") })
			deleted := slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "del-rel:") })
			delTag := slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "del-tag:") })
			wantWarn, hasWarn := strings.CutPrefix(tc.name, "warn:")
			if (published && !strings.Contains(tc.name, "publish") && !strings.Contains(tc.name, "deletion") && !strings.Contains(tc.name, "lookup") && !strings.Contains(tc.name, "active run") && !strings.Contains(tc.name, "expiry") && !hasWarn) ||
				(deleted && !strings.Contains(tc.name, "deletion") && !strings.Contains(tc.name, "expiry") && !hasWarn) ||
				(delTag && !strings.Contains(tc.name, "expiry") && !hasWarn) ||
				((strings.Contains(tc.name, "expiry") || hasWarn) && !slices.Contains(s.events, "del-rel:2")) ||
				(hasWarn && !strings.Contains(summary.String(), wantWarn)) {
				t.Fatalf("unexpected events %v or summary %q", s.events, summary.String())
			}
		})
	}
	rels, tags := oldExpired()
	failTag := rels[0].TagName
	s := &mockStore{token: "tok", expiryHeader: now.Add(15 * day).Format("2006-01-02 15:04:05 UTC"), releases: rels, tags: tags, failTagDel: failTag}
	srv := s.server(t)
	defer srv.Close()
	cfg := testCfg(srv.URL, "tok", now, &bytes.Buffer{})
	cfg.summary = &bytes.Buffer{}
	if runStore(t.Context(), cfg, dir, currSHA, "10", "1") == nil || !slices.Contains(s.tags, failTag) {
		t.Fatal("failed tag deletion must error and leave tag for next run")
	}
	s.failTagDel = ""
	if err := runStore(t.Context(), cfg, dir, currSHA, "10", "2"); err != nil || slices.Contains(s.tags, failTag) {
		t.Fatalf("next run failed to delete orphan tag: %v", err)
	}
}

func TestHTTPClientHardening(t *testing.T) {
	tag, pages, leakedAuth := relNameAt(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), shaOf(1), 1, 1), 0, ""
	batch := make([]ghRelease, 100)
	tlsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedAuth = r.Header.Get("Authorization")
		if r.URL.Path == "/204" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte("asset-bytes"))
	}))
	defer tlsTarget.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/11"):
			_ = json.NewEncoder(w).Encode(ghRelease{ID: 11, Name: tag, TagName: tag})
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/releases/11"):
			http.Redirect(w, r, tlsTarget.URL+"/204", http.StatusSeeOther)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/redir-http"):
			http.Redirect(w, r, "http://"+r.Host+"/target", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/target"):
			leakedAuth = r.Header.Get("Authorization")
		case strings.HasSuffix(r.URL.Path, "/redir-https"):
			http.Redirect(w, r, tlsTarget.URL+"/blob", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/oversized"):
			chunk := make([]byte, 1<<20)
			for {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		case strings.HasSuffix(r.URL.Path, "/releases"):
			if pages++; pages > 105 {
				http.Error(w, "infinite loop", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(batch)
		}
	}))
	defer srv.Close()
	cfg := testCfg(srv.URL, "secret-tok", time.Time{}, nil)
	cfg.transport = tlsTarget.Client().Transport
	c := &apiClient{cfg: cfg}
	if c.deleteReleaseByID(t.Context(), ghRelease{ID: 11, Name: tag, TagName: tag}) == nil || c.deleteTagByRef(t.Context(), tag) == nil {
		t.Fatal("expected 303-to-https 204 and 200 on DELETE to fail")
	}
	if _, _, err := c.do(t.Context(), true, "GET", "/redir-http", "application/octet-stream", nil, nil); err == nil || !strings.Contains(err.Error(), "redirect refused") || leakedAuth != "" {
		t.Fatalf("expected insecure redirect error and no leaked auth, got err=%v leakedAuth=%q", err, leakedAuth)
	}
	if code, body, err := c.do(t.Context(), true, "GET", "/redir-https", "application/octet-stream", nil, nil); err != nil || code != 200 || string(body) != "asset-bytes" || leakedAuth != "" {
		t.Fatalf("https redirect failed or leaked auth: code=%d body=%q err=%v leakedAuth=%q", code, body, err, leakedAuth)
	}
	if _, _, err := c.do(t.Context(), true, "GET", "/oversized", "application/octet-stream", nil, nil); err == nil {
		t.Fatal("expected oversized response body to fail")
	}
	if _, err := c.listReleases(t.Context()); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("expected page cap error, got err=%v (pages=%d)", err, pages)
	}
	t.Setenv("SOURCEMAPS_API_URL", srv.URL)
	t.Setenv("SOURCEMAPS_REPO", "evil/repo")
	if pc := prodConfig("secret-tok", &bytes.Buffer{}, &bytes.Buffer{}); pc.apiBase != prodAPIBase || pc.uploadBase != prodUploadBase || pc.storeRepo != prodStoreRepo || pc.allowTestEndpoint {
		t.Fatalf("prodConfig affected by env override: %+v", pc)
	}
	for _, bad := range []config{
		{apiBase: srv.URL, uploadBase: prodUploadBase, storeRepo: prodStoreRepo, sourceRepo: prodSourceRepo, token: "secret-tok"},
		{apiBase: "https://collector.example", uploadBase: prodUploadBase, storeRepo: prodStoreRepo, sourceRepo: prodSourceRepo, token: "secret-tok"},
		{apiBase: prodAPIBase, uploadBase: "https://collector.example", storeRepo: prodStoreRepo, sourceRepo: prodSourceRepo, token: "secret-tok"},
		{apiBase: prodAPIBase, uploadBase: prodUploadBase, storeRepo: "evil/repo", sourceRepo: prodSourceRepo, token: "secret-tok"},
		{apiBase: prodAPIBase, uploadBase: prodUploadBase, storeRepo: prodStoreRepo, sourceRepo: "evil/source", token: "secret-tok"},
	} {
		if _, _, err := (&apiClient{cfg: bad}).do(t.Context(), true, "GET", "/target", "", nil, nil); err == nil || leakedAuth != "" {
			t.Fatalf("expected unpinned/non-https config %+v to be rejected without sending token, got err=%v leakedAuth=%q", bad, err, leakedAuth)
		}
	}
}

func TestManifestPathAndHashValidation(t *testing.T) {
	now, sha, validHash := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), shaOf(1), strings.Repeat("a", 64)
	mapContent, root := []byte(`{"version":3,"sourcesContent":["x"]}`), t.TempDir()
	absBase := filepath.Join(root, "abs.js")
	for _, tc := range []struct{ name, file, mapName, hash, wantErr string }{
		{"parent", "../secret.js", "../secret.js.map", validHash, "invalid source map entry"},
		{"nested", "sub/app.js", "sub/app.js.map", validHash, "invalid source map entry"},
		{"absolute", absBase, absBase + ".map", validHash, "invalid source map entry"},
		{"bad-hash", "app.js", "app.js.map", "not-a-sha256", "invalid source map entry"},
		{"mismatched-map", "app.js", "other.js.map", validHash, "invalid source map entry"},
		{"dup-map", "app.js", "app.js.map", "dup", "invalid source map entry"},
		{"bad-commit", "app.js", "app.js.map", "bad-commit", "invalid map index"},
		{"bad-run", "app.js", "app.js.map", "bad-run", "invalid map index"},
		{"stray-file", "app.js", "app.js.map", "stray", "unexpected files"},
		{"bad-json", "app.js", "app.js.map", "{bad", "reading map index"},
		{"no-sources", "app.js", "app.js.map", "no-sources", "missing sourcesContent"},
	} {
		dir := filepath.Join(root, tc.name)
		target := filepath.Join(dir, tc.mapName)
		if filepath.IsAbs(tc.mapName) {
			target = tc.mapName
		}
		_ = os.MkdirAll(filepath.Dir(target), 0755)
		_ = os.MkdirAll(dir, 0755)
		mb, cmt, rid, h := mapContent, sha, "10", tc.hash
		switch tc.hash {
		case "no-sources":
			mb, h = []byte(`{"version":3}`), validHash
		case "bad-commit":
			cmt, h = shaOf(2), validHash
		case "bad-run":
			rid, h = "99", validHash
		case "stray":
			_ = os.WriteFile(filepath.Join(dir, "extra.txt"), mb, 0644)
			h = validHash
		}
		_ = os.WriteFile(target, mb, 0644)
		if !strings.HasPrefix(target, dir+string(filepath.Separator)) {
			_ = os.WriteFile(filepath.Join(dir, "pad"), mb, 0644)
		}
		idx := fmt.Sprintf(`{"commit":%q,"run_id":%q,"entries":{"e":{"file":%q,"sha256":%q,"map":%q}}}`, cmt, rid, tc.file, h, tc.mapName)
		if tc.hash == "dup" {
			idx = fmt.Sprintf(`{"commit":%q,"run_id":"10","entries":{"a":{"file":%q,"sha256":%q,"map":%q},"b":{"file":%q,"sha256":%q,"map":%q}}}`, sha, tc.file, validHash, tc.mapName, tc.file, validHash, tc.mapName)
		} else if tc.hash == "{bad" {
			idx = "{bad"
		}
		_ = os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0644)
		s := &mockStore{token: "tok"}
		srv := s.server(t)
		defer srv.Close()
		if err := runStore(t.Context(), testCfg(srv.URL, "tok", now, &bytes.Buffer{}), dir, sha, "10", "1"); err == nil || !strings.Contains(err.Error(), tc.wantErr) || len(s.events) != 0 || strings.Contains(err.Error(), "%!w(<nil>)") {
			t.Fatalf("expected %s to fail with %q before any API call, got err=%v events=%v", tc.name, tc.wantErr, err, s.events)
		}
	}
}

func TestZeroSizeAssetAndAllEntriesFetchBack(t *testing.T) {
	now, sha, dir, h1 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), shaOf(1), t.TempDir(), strings.Repeat("1", 64)
	idx := fmt.Sprintf(`{"commit":%q,"run_id":"10","entries":{"a":{"file":"a-first.js","sha256":%q,"map":"a-first.js.map"},"b":{"file":"b-second.js","sha256":%q,"map":"b-second.js.map"}}}`, sha, h1, h1)
	_ = cmp.Or(os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0644), os.WriteFile(filepath.Join(dir, "a-first.js.map"), []byte(`{"sourcesContent":["1"]}`), 0644), os.WriteFile(filepath.Join(dir, "b-second.js.map"), []byte(`{"sourcesContent":["2"]}`), 0644))
	for _, mode := range []string{"", "zero-second", "corrupt-second-after-publish"} {
		s := &mockStore{token: "tok", tamperManifest: mode}
		srv := s.server(t)
		defer srv.Close()
		err := runStore(t.Context(), testCfg(srv.URL, "tok", now, &bytes.Buffer{}), dir, sha, "10", "1")
		if (mode == "") != (err == nil) || (mode == "zero-second" && slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "publish:") })) {
			t.Fatalf("mode %q: unexpected result err=%v events=%v", mode, err, s.events)
		}
	}
}

func TestLookupHardening(t *testing.T) {
	now, sha, h, h2 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), shaOf(1), strings.Repeat("a", 64), strings.Repeat("b", 64)
	rel, futRel, mapData := relNameAt(now, sha, 10, 1), fmt.Sprintf("map-99990101T000000Z-%s-10.1", sha), []byte(`{"version":3,"sourcesContent":["ok"]}`)
	validIdx := []byte(fmt.Sprintf(`{"commit":%q,"run_id":"10","entries":{"e":{"file":"a.js","sha256":%q,"map":"a.js.map"}}}`, sha, h))
	wrongCommitIdx := bytes.ReplaceAll(validIdx, []byte(sha), []byte(shaOf(2)))
	wrongRunIdx := bytes.ReplaceAll(validIdx, []byte(`"10"`), []byte(`"99"`))
	missingAssetIdx := []byte(fmt.Sprintf(`{"commit":%q,"run_id":"10","entries":{"a":{"file":"a.js","sha256":%q,"map":"a.js.map"},"b":{"file":"b.js","sha256":%q,"map":"b.js.map"}}}`, sha, h, h2))
	assetOf := func(id int64, name, state string, d []byte) mockAsset {
		return mockAsset{ID: id, Name: name, Size: int64(len(d)), State: state, data: d}
	}
	for _, tc := range []struct {
		name, tok, relName string
		clk                time.Time
		assets             []mockAsset
	}{
		{"zero clock", "tok", futRel, time.Time{}, []mockAsset{assetOf(2, "index.json", "", validIdx), assetOf(3, "a.js.map", "", mapData)}},
		{"empty token", "", rel, now, []mockAsset{assetOf(2, "index.json", "", validIdx), assetOf(3, "a.js.map", "", mapData)}},
		{"wrong commit", "tok", rel, now, []mockAsset{assetOf(2, "index.json", "", wrongCommitIdx), assetOf(3, "a.js.map", "", mapData)}},
		{"wrong run_id", "tok", rel, now, []mockAsset{assetOf(2, "index.json", "", wrongRunIdx), assetOf(3, "a.js.map", "", mapData)}},
		{"missing asset", "tok", rel, now, []mockAsset{assetOf(2, "index.json", "", missingAssetIdx), assetOf(3, "a.js.map", "", mapData)}},
		{"unready state", "tok", rel, now, []mockAsset{assetOf(2, "index.json", "", validIdx), assetOf(3, "a.js.map", "starter", mapData)}},
		{"map size mismatch", "tok", rel, now, []mockAsset{assetOf(2, "index.json", "", validIdx), {ID: 3, Name: "a.js.map", Size: int64(len(mapData) + 5), data: mapData}}},
		{"map missing sourcesContent", "tok", rel, now, []mockAsset{assetOf(2, "index.json", "", validIdx), assetOf(3, "a.js.map", "", []byte(`{"version":3}`))}},
	} {
		s := &mockStore{token: "tok", releases: []mockRelease{{ID: 1, Name: tc.relName, TagName: tc.relName, Assets: tc.assets}}}
		srv := s.server(t)
		defer srv.Close()
		if _, _, _, err := runLookup(t.Context(), testCfg(srv.URL, tc.tok, tc.clk, nil), sha, h, "", ""); err == nil {
			t.Fatalf("expected %s to fail lookup", tc.name)
		}
	}
	dupIdx := []byte(fmt.Sprintf(`{"commit":%q,"run_id":"10","entries":{"a":{"file":"a.js","sha256":%q,"map":"a.js.map"},"z":{"file":"z.js","sha256":%q,"map":"z.js.map"}}}`, sha, h, h))
	idxFor := func(run int, entries string) []byte {
		return []byte(fmt.Sprintf(`{"commit":%q,"run_id":"%d","entries":{%s}}`, sha, run, entries))
	}
	rAt := func(run int) string { return relNameAt(now.Add(time.Duration(run)*time.Minute), sha, run, 1) }
	s := &mockStore{token: "tok", releases: []mockRelease{
		{ID: 26, Name: rAt(26), TagName: rAt(26), Assets: []mockAsset{assetOf(261, "index.json", "", idxFor(26, fmt.Sprintf(`"a":{"file":"a.js","sha256":%q,"map":"a.js.map"}`, h))), assetOf(262, "a.js.map", "starter", mapData)}},
		{ID: 25, Name: rAt(25), TagName: rAt(25), Assets: []mockAsset{assetOf(251, "a.js.map", "", mapData)}},
		{ID: 24, Name: rAt(24), TagName: rAt(24), Assets: []mockAsset{assetOf(241, "index.json", "", idxFor(24, fmt.Sprintf(`"a":{"file":"a.js","sha256":%q,"map":"a.js.map"},"b":{"file":"b.js","sha256":%q,"map":"b.js.map"}`, h, h2))), assetOf(242, "a.js.map", "", mapData)}},
		{ID: 23, Name: rAt(23), TagName: rAt(23), Assets: []mockAsset{assetOf(231, "index.json", "", idxFor(23, `"a":{"file":"a.js","sha256":"bad","map":"a.js.map"}`)), assetOf(232, "a.js.map", "", mapData)}},
		{ID: 22, Name: rAt(22), TagName: rAt(22), Assets: []mockAsset{assetOf(221, "index.json", "", idxFor(22, fmt.Sprintf(`"a":{"file":"a.js","sha256":%q,"map":"a.js.map"},"b":{"file":"a.js","sha256":%q,"map":"a.js.map"}`, h, h2))), assetOf(222, "a.js.map", "", mapData), assetOf(223, "extra.js.map", "", mapData)}},
		{ID: 21, Name: rAt(21), TagName: rAt(21), Assets: []mockAsset{assetOf(211, "index.json", "", idxFor(21, fmt.Sprintf(`"a":{"file":"a.js","sha256":%q,"map":"a.js.map"}`, h))), assetOf(212, "other.js.map", "", mapData)}},
		{ID: 1, Name: rel, TagName: rel, Assets: []mockAsset{assetOf(2, "index.json", "", dupIdx), assetOf(3, "a.js.map", "", mapData), assetOf(4, "z.js.map", "", mapData)}},
	}}
	srv := s.server(t)
	defer srv.Close()
	if gotRel, gotIdx, _, err := runLookup(t.Context(), testCfg(srv.URL, "tok", now, nil), sha, "", "", ""); err != nil || gotRel != rel || gotIdx != "index.json" {
		t.Fatalf("expected SHA-only lookup to skip incomplete newer releases and pick %s, got rel=%q asset=%q err=%v", rel, gotRel, gotIdx, err)
	}
	if gotRel, gotMap, _, err := runLookup(t.Context(), testCfg(srv.URL, "tok", now, nil), sha, h, "", ""); err != nil || gotRel != rel || gotMap != "a.js.map" {
		t.Fatalf("expected hash lookup to skip incomplete newer releases and pick %s/a.js.map, got rel=%q map=%q err=%v", rel, gotRel, gotMap, err)
	}
	for i := range 30 {
		if _, gotMap, _, err := runLookup(t.Context(), testCfg(srv.URL, "tok", now, nil), sha, h, "10", ""); err != nil || gotMap != "a.js.map" {
			t.Fatalf("iter %d: expected deterministic a.js.map, got %q (err=%v)", i, gotMap, err)
		}
	}
}

func TestExactBuildVerification(t *testing.T) {
	now, sha := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), shaOf(1)
	s := &mockStore{token: "tok"}
	srv := s.server(t)
	defer srv.Close()
	var dirs, names []string
	var hashes, wantMaps []string
	for i, identity := range [][2]string{{"10", "1"}, {"10", "2"}, {"11", "1"}} {
		dir, mapDir := t.TempDir(), t.TempDir()
		m := manifest{Commit: sha, RunID: identity[0], Entries: map[string]bundle{}}
		for _, key := range []string{"a", "z"} {
			data := []byte(fmt.Sprintf("console.log(%q, %d)", key, i))
			hash := fmt.Sprintf("%x", sha256.Sum256(data))
			mapData := fmt.Sprintf(`{"sourcesContent":[%q]}`, string(data))
			m.Entries[key] = bundle{key + ".js", hash, key + ".js.map"}
			if err := cmp.Or(os.WriteFile(filepath.Join(dir, key+".js"), data, 0644), os.WriteFile(filepath.Join(mapDir, key+".js.map"), []byte(mapData), 0644)); err != nil {
				t.Fatal(err)
			}
			if key == "z" {
				hashes, wantMaps = append(hashes, hash), append(wantMaps, mapData)
			}
		}
		idx, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(mapDir, "index.json"), idx, 0644); err != nil {
			t.Fatal(err)
		}
		if err := runStore(t.Context(), testCfg(srv.URL, "tok", now, io.Discard), mapDir, sha, identity[0], identity[1]); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
		names = append(names, s.releases[len(s.releases)-1].Name)
		var out bytes.Buffer
		cfg := testCfg(srv.URL, "tok", now, &out)
		if err := runVerify(t.Context(), cfg, dir, sha, identity[0], identity[1]); err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			manifest
			Release, Attempt string
		}
		if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.Commit != sha || receipt.RunID != identity[0] || receipt.Attempt != identity[1] || receipt.Release != names[i] || !maps.Equal(receipt.Entries, m.Entries) {
			t.Fatalf("wrong receipt: %s (%v)", out.String(), err)
		}
	}
	// The newest same-SHA output must not replace either earlier run/attempt.
	for i, identity := range [][2]string{{"10", "1"}, {"10", "2"}, {"11", "1"}} {
		cfg := testCfg(srv.URL, "tok", now, &bytes.Buffer{})
		if err := runVerify(t.Context(), cfg, dirs[i], sha, identity[0], identity[1]); err != nil {
			t.Fatal(err)
		}
		rel, name, data, err := runLookup(t.Context(), cfg, sha, hashes[i], identity[0], identity[1])
		if err != nil || rel != names[i] || name != "z.js.map" || string(data) != wantMaps[i] {
			t.Fatalf("wrong rebuild map: %s/%s %s (%v)", rel, name, data, err)
		}
	}
	for _, tc := range []struct{ name, commit, run, attempt, change string }{
		{"wrong SHA", shaOf(2), "10", "1", ""},
		{"missing run", sha, "12", "1", ""},
		{"missing attempt", sha, "10", "3", ""},
		{"no run", sha, "", "1", ""},
		{"no attempt", sha, "11", "", ""},
		{"invalid attempt", sha, "10", "01", ""},
		{"other rebuild", sha, "10", "2", ""},
		{"changed last bundle", sha, "10", "1", "tamper"},
		{"renamed bundle", sha, "10", "1", "rename"},
		{"missing bundle", sha, "10", "1", "missing"},
		{"extra bundle", sha, "10", "1", "extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := dirs[0]
			if tc.name == "no attempt" {
				source = dirs[2]
			}
			for _, key := range []string{"a", "z"} {
				data, err := os.ReadFile(filepath.Join(source, key+".js"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, key+".js"), data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.change {
			case "tamper":
				_ = os.WriteFile(filepath.Join(dir, "z.js"), []byte("changed"), 0644)
			case "rename":
				_ = os.Rename(filepath.Join(dir, "z.js"), filepath.Join(dir, "renamed.js"))
			case "missing":
				_ = os.Remove(filepath.Join(dir, "z.js"))
			case "extra":
				_ = os.WriteFile(filepath.Join(dir, "extra.js"), []byte("extra"), 0644)
			}
			var out bytes.Buffer
			if err := runVerify(t.Context(), testCfg(srv.URL, "tok", now, &out), dir, tc.commit, tc.run, tc.attempt); err == nil || out.Len() != 0 {
				t.Fatalf("must refuse without a receipt: err=%v output=%s", err, out.String())
			}
		})
	}
	if len(s.events) != 15 {
		t.Fatalf("verification mutated store: %v", s.events)
	}
}
