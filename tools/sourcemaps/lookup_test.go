package main

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func completeLookupRelease(start time.Time, sha, hash string, run int) mockRelease {
	name := relNameAt(start, sha, run, 1)
	idx := []byte(fmt.Sprintf(`{"commit":%q,"run_id":"%d","entries":{"a":{"file":"a.js","sha256":%q,"map":"a.js.map"}}}`, sha, run, hash))
	data := []byte(fmt.Sprintf(`{"version":3,"sourcesContent":["run %d"]}`, run))
	return mockRelease{ID: int64(run), Name: name, TagName: name, Assets: []mockAsset{
		{ID: int64(run*10 + 1), Name: "index.json", Size: int64(len(idx)), State: "uploaded", data: idx},
		{ID: int64(run*10 + 2), Name: "a.js.map", Size: int64(len(data)), State: "uploaded", data: data},
	}}
}

func assertLookupRelease(t *testing.T, cfg config, sha, hash string, want mockRelease) {
	t.Helper()
	for _, tc := range []struct {
		name, hash string
		asset      mockAsset
	}{
		{"index", "", want.Assets[0]},
		{"map", hash, want.Assets[1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel, asset, data, err := runLookup(t.Context(), cfg, sha, tc.hash, "", "")
			if err != nil || rel != want.Name || asset != tc.asset.Name || !bytes.Equal(data, tc.asset.data) {
				t.Fatalf("lookup = %q/%q %q (%v), want %q/%q %q", rel, asset, data, err, want.Name, tc.asset.Name, tc.asset.data)
			}
		})
	}
}

func TestLookupReleaseFilters(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	sha, hash := shaOf(1), strings.Repeat("a", 64)
	for _, tc := range []struct {
		name                       string
		startOffset                time.Duration
		draft, prerelease, wantNew bool
	}{
		{name: "published", wantNew: true},
		{name: "draft", draft: true},
		{name: "prerelease", prerelease: true},
		{name: "at now plus 24h", startOffset: 24 * time.Hour, wantNew: true},
		{name: "after now plus 24h", startOffset: 24*time.Hour + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			older := completeLookupRelease(now.Add(-time.Hour), sha, hash, 1)
			candidate := completeLookupRelease(now.Add(tc.startOffset), sha, hash, 2)
			candidate.Draft, candidate.Prerelease = tc.draft, tc.prerelease
			s := &mockStore{token: "tok", releases: []mockRelease{older, candidate}}
			srv := s.server(t)
			defer srv.Close()
			want := older
			if tc.wantNew {
				want = candidate
			}
			assertLookupRelease(t, testCfg(srv.URL, "tok", now, nil), sha, hash, want)
		})
	}
}

func TestLookupIndexAssetSize(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	sha, hash := shaOf(1), strings.Repeat("a", 64)
	for _, tc := range []struct {
		name                             string
		sizeDelta                        int64
		zero, wantValidMetadata, wantNew bool
		wantIndexFetches                 int64
	}{
		{name: "normal passes metadata and byte count guards", wantValidMetadata: true, wantNew: true, wantIndexFetches: 2},
		{name: "mismatch fails byte count guard", sizeDelta: 1, wantValidMetadata: true, wantIndexFetches: 2},
		{name: "zero fails validAsset metadata guard", zero: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			older := completeLookupRelease(now.Add(-time.Hour), sha, hash, 1)
			candidate := completeLookupRelease(now, sha, hash, 2)
			idx := &candidate.Assets[0]
			idx.Size += tc.sizeDelta
			if tc.zero {
				// Keep valid nonempty bytes: only the advertised size is zero.
				idx.Size = 0
			}
			if got := validAsset(ghAsset{ID: idx.ID, Name: idx.Name, Size: idx.Size, State: idx.State}); got != tc.wantValidMetadata {
				t.Errorf("validAsset = %v, want %v", got, tc.wantValidMetadata)
			}
			var indexFetches atomic.Int64
			s := &mockStore{token: "tok", releases: []mockRelease{older, candidate}}
			s.before = func(_ http.ResponseWriter, r *http.Request) bool {
				if r.Method == "GET" && r.URL.Path == fmt.Sprintf("/repos/%s/releases/assets/%d", prodStoreRepo, idx.ID) {
					indexFetches.Add(1)
				}
				return false
			}
			srv := s.server(t)
			defer srv.Close()
			want := older
			if tc.wantNew {
				want = candidate
			}
			assertLookupRelease(t, testCfg(srv.URL, "tok", now, nil), sha, hash, want)
			if got := indexFetches.Load(); got != tc.wantIndexFetches {
				t.Errorf("candidate index fetches = %d, want %d", got, tc.wantIndexFetches)
			}
		})
	}
}
