package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
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
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	data []byte
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
	token, tamperManifest, failTagDel, tagDel404 string
	releases                                     []mockRelease
	tags, events                                 []string
	failUpload                                   bool
	failRelDelID, relDel404ID, wrongLookupID     int64
	nextID                                       int64
}

func (s *mockStore) server(t *testing.T) *httptest.Server {
	t.Helper()
	s.nextID = max(s.nextID, 100)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "unauthorized", 401)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/repos/tkakkie/ribbitto-sourcemaps")
		switch {
		case r.Method == "GET" && path == "/releases":
			_ = json.NewEncoder(w).Encode(s.releases)
		case r.Method == "POST" && path == "/releases":
			var req mockRelease
			_ = json.NewDecoder(r.Body).Decode(&req)
			if !req.Draft || req.Prerelease || req.TargetCommitish == "" {
				http.Error(w, "bad draft", 400)
				return
			}
			s.nextID++
			req.ID = s.nextID
			s.releases, s.events = append(s.releases, req), append(s.events, "draft:"+req.Name)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(req)
		case r.Method == "POST" && strings.HasPrefix(path, "/releases/") && strings.HasSuffix(path, "/assets"):
			if s.failUpload {
				http.Error(w, "upload refused", 500)
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
					_ = json.NewEncoder(w).Encode(s.releases[i])
				case "PATCH":
					var p struct{ Draft, Prerelease bool }
					_ = json.NewDecoder(r.Body).Decode(&p)
					s.releases[i].Draft, s.releases[i].Prerelease = p.Draft, p.Prerelease
					if !p.Draft && !slices.Contains(s.tags, s.releases[i].TagName) {
						s.tags = append(s.tags, s.releases[i].TagName)
					}
					s.events = append(s.events, "publish:"+s.releases[i].Name)
					_ = json.NewEncoder(w).Encode(s.releases[i])
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
		case r.Method == "DELETE" && strings.HasPrefix(path, "/git/refs/tags/"):
			tag := strings.TrimPrefix(path, "/git/refs/tags/")
			if tag == s.failTagDel {
				http.Error(w, "refused", 500)
				return
			}
			s.events = append(s.events, "del-tag:"+tag)
			if idx := slices.Index(s.tags, tag); idx >= 0 {
				s.tags = slices.Delete(s.tags, idx, idx+1)
				if tag != s.tagDel404 {
					w.WriteHeader(204)
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
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
	store := &mockStore{
		token: "test-token",
		releases: []mockRelease{
			{ID: 11, Name: oldTag, TagName: oldTag},
			{ID: 12, Name: oldTag404, TagName: oldTag404},
			{ID: 13, Name: oldTag, TagName: oldTag},
		},
		tags:        []string{oldTag, oldTag404},
		relDel404ID: 12, tagDel404: oldTag404, wrongLookupID: 13,
	}
	srv := store.server(t)
	defer srv.Close()

	var out bytes.Buffer
	cfg := config{apiBase: srv.URL, storeRepo: "tkakkie/ribbitto-sourcemaps", token: "test-token", now: now, stdout: &out}
	if err := runStore(t.Context(), cfg, dir, currSHA, "500", "2"); err != nil {
		t.Fatalf("runStore failed: %v", err)
	}
	wantRel := relNameAt(now, currSHA, 500, 2)
	draftIdx, pubIdx := slices.Index(store.events, "draft:"+wantRel), slices.Index(store.events, "publish:"+wantRel)
	if !strings.Contains(out.String(), wantRel) || draftIdx < 0 || pubIdx <= draftIdx {
		t.Fatalf("unexpected output or lifecycle order: %s / %v", out.String(), store.events)
	}
	for _, run := range []struct{ id, att int }{{9, 1}, {10, 1}, {510, 9}, {510, 10}} {
		dirN, _, _ := writeMapDir(t, currSHA, strconv.Itoa(run.id))
		if err := runStore(t.Context(), cfg, dirN, currSHA, strconv.Itoa(run.id), strconv.Itoa(run.att)); err != nil {
			t.Fatalf("rerun store %v failed: %v", run, err)
		}
	}
	if gotRel, gotIdx, _, err := runLookup(t.Context(), cfg, currSHA, "", ""); err != nil || gotRel != relNameAt(now, currSHA, 510, 10) || gotIdx != "index.json" {
		t.Fatalf("lookup by SHA alone failed: rel=%s asset=%s err=%v", gotRel, gotIdx, err)
	}
	if gotRel, gotMapName, gotMap, err := runLookup(t.Context(), cfg, currSHA, bundleHash, ""); err != nil || gotRel != relNameAt(now, currSHA, 510, 10) || gotMapName != "message-time-ABCDEF12.js.map" || !bytes.Equal(gotMap, wantMap) {
		t.Fatalf("lookup failed: rel=%s map=%s err=%v", gotRel, gotMapName, err)
	}
	if gotRel9, _, _, err := runLookup(t.Context(), cfg, currSHA, bundleHash, "9"); err != nil || gotRel9 != relNameAt(now, currSHA, 9, 1) {
		t.Fatalf("lookup with runID=9 failed: rel=%s err=%v", gotRel9, err)
	}
	c := &apiClient{cfg: cfg}
	if err := cmp.Or(c.deleteReleaseByID(t.Context(), ghRelease{ID: 11, Name: oldTag, TagName: oldTag}), c.deleteTagByRef(t.Context(), oldTag), c.deleteReleaseByID(t.Context(), ghRelease{ID: 12, Name: oldTag404, TagName: oldTag404}), c.deleteTagByRef(t.Context(), oldTag404)); err != nil {
		t.Fatalf("exact-ID deletion failed: %v", err)
	}
	if c.deleteReleaseByID(t.Context(), ghRelease{ID: 13, Name: oldTag, TagName: oldTag}) == nil ||
		c.deleteReleaseByID(t.Context(), ghRelease{ID: 40, Name: oldTag, TagName: oldTag, Prerelease: true}) == nil ||
		c.deleteReleaseByID(t.Context(), ghRelease{ID: 41, Name: "prefix-" + oldTag, TagName: "prefix-" + oldTag}) == nil ||
		c.deleteReleaseByID(t.Context(), ghRelease{ID: 42, Name: oldTag, TagName: oldTag404}) == nil ||
		c.deleteTagByRef(t.Context(), "non-canonical") == nil {
		t.Fatal("expected exact-ID deletion guard to refuse invalid release or tag")
	}
}

func TestStoreCoreFailures(t *testing.T) {
	now, currSHA := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), shaOf(1)
	dir, _, _ := writeMapDir(t, currSHA, "10")
	for _, tc := range []struct {
		name, token, wantErr string
		mod                  func(*mockStore)
	}{
		{"rejected token", "wrong", "401", func(*mockStore) {}},
		{"upload failure", "tok", "500", func(s *mockStore) { s.failUpload = true }},
		{"manifest mismatch extra asset", "tok", "manifest", func(s *mockStore) { s.tamperManifest = "extra" }},
		{"manifest mismatch missing asset", "tok", "manifest", func(s *mockStore) { s.tamperManifest = "drop" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockStore{token: "tok"}
			tc.mod(s)
			srv := s.server(t)
			defer srv.Close()
			err := runStore(t.Context(), config{apiBase: srv.URL, storeRepo: "tkakkie/ribbitto-sourcemaps", token: tc.token, now: now, stdout: &bytes.Buffer{}}, dir, currSHA, "10", "1")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "publish:") }) {
				t.Fatalf("want error %q and no publish, got err=%v events=%v", tc.wantErr, err, s.events)
			}
		})
	}
}

func TestHTTPClientHardening(t *testing.T) {
	tag, pages, leakedAuth := relNameAt(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), shaOf(1), 1, 1), 0, ""
	batch := make([]ghRelease, 100)
	tlsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("asset-bytes"))
	}))
	defer tlsTarget.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/11"):
			_ = json.NewEncoder(w).Encode(ghRelease{ID: 11, Name: tag, TagName: tag})
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/releases/11"):
			http.Redirect(w, r, "/repos/tkakkie/ribbitto-sourcemaps/releases/11", http.StatusMovedPermanently)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/redir-http"):
			http.Redirect(w, r, "http://"+r.Host+"/target", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/target"):
			leakedAuth = r.Header.Get("Authorization")
		case strings.HasSuffix(r.URL.Path, "/redir-https"):
			http.Redirect(w, r, tlsTarget.URL+"/blob", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/oversized"):
			_, _ = w.Write(make([]byte, (64<<20)+1))
		case strings.HasSuffix(r.URL.Path, "/releases"):
			if pages++; pages > 105 {
				http.Error(w, "infinite loop", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(batch)
		}
	}))
	defer srv.Close()
	c := &apiClient{cfg: config{apiBase: srv.URL, storeRepo: "tkakkie/ribbitto-sourcemaps", token: "secret-tok", transport: tlsTarget.Client().Transport}}
	if c.deleteReleaseByID(t.Context(), ghRelease{ID: 11, Name: tag, TagName: tag}) == nil || c.deleteTagByRef(t.Context(), tag) == nil {
		t.Fatal("expected 301-to-GET 200 and 200 on DELETE to fail")
	}
	if _, _, err := c.do(t.Context(), true, "GET", "/redir-http", "application/octet-stream", nil, nil); err == nil || leakedAuth != "" {
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
}
