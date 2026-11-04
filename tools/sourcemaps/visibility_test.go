package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestUploadResponses(t *testing.T) {
	for _, status := range []int{200, 201, 422} {
		for _, bad := range []string{"empty", "null", "malformed", "zero ID", "wrong name", "wrong size", "bad state", "already exists"} {
			t.Run(fmt.Sprintf("%d/%s", status, bad), func(t *testing.T) {
				s := &mockStore{token: "mock-credential"}
				s.before = func(w http.ResponseWriter, r *http.Request) bool {
					if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/assets") {
						return false
					}
					a := ghAsset{ID: 12, Name: r.URL.Query().Get("name"), Size: r.ContentLength, State: "uploaded"}
					w.WriteHeader(status)
					switch bad {
					case "empty":
						return true
					case "null":
						_, _ = w.Write([]byte("null"))
						return true
					case "malformed":
						_, _ = w.Write([]byte(`{"id":"PRIVATE-RESPONSE"}`))
						return true
					case "already exists":
						_, _ = w.Write([]byte(`{"errors":[{"code":"already_exists"}]}`))
						return true
					case "zero ID":
						a.ID = 0
					case "wrong name":
						a.Name = "PRIVATE-RESPONSE"
					case "wrong size":
						a.Size++
					case "bad state":
						a.State = "PRIVATE-RESPONSE"
					}
					_ = json.NewEncoder(w).Encode(a)
					return true
				}
				srv := s.server(t)
				defer srv.Close()
				dir, _, _ := writeMapDir(t, shaOf(1), "10")
				var out bytes.Buffer
				err := runStore(t.Context(), testCfg(srv.URL, s.token, time.Now(), &out), dir, shaOf(1), "10", "1")
				if err == nil || !strings.Contains(err.Error(), `uploading asset "index.json"`) || len(s.events) != 1 {
					t.Fatalf("invalid upload accepted or continued: %v events=%v", err, s.events)
				}
				for _, private := range []string{s.token, "PRIVATE-RESPONSE", "sourcesContent"} {
					if strings.Contains(err.Error()+out.String(), private) {
						t.Fatal("private response or credential reached diagnostics")
					}
				}
			})
		}
	}
}

func TestDraftAssetVisibility(t *testing.T) {
	for _, source := range []string{"release", "assets list"} {
		for _, mode := range []string{"transient", "persistent 200", "persistent 201", "duplicate", "extra", "wrong size", "bad state", "wrong ID"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				t.Parallel()
				s := &mockStore{token: "mock-credential", uploadStatus: 201, uploadState: "uploaded"}
				if mode == "persistent 200" {
					s.uploadStatus = 200
				}
				reads := 0
				s.before = func(w http.ResponseWriter, r *http.Request) bool {
					path := "/repos/" + prodStoreRepo + "/releases/101"
					if source == "assets list" {
						path += "/assets"
					}
					if r.Method != "GET" || r.URL.Path != path {
						return false
					}
					reads++
					rel := s.releases[0]
					rel.Assets = slices.Clone(rel.Assets)
					switch mode {
					case "transient", "persistent 200", "persistent 201":
						if mode != "transient" || reads < 3 {
							rel.Assets = rel.Assets[1:]
						}
					case "duplicate":
						rel.Assets = append(rel.Assets, rel.Assets[0])
					case "extra":
						rel.Assets = append(rel.Assets, mockAsset{ID: 999, Name: "extra\n::error::##[forged]"})
					case "wrong size":
						rel.Assets[0].Size++
					case "bad state":
						rel.Assets[0].State = "starter"
					case "wrong ID":
						rel.Assets[0].ID++
					}
					if source == "release" {
						_ = json.NewEncoder(w).Encode(rel)
					} else {
						_ = json.NewEncoder(w).Encode(rel.Assets)
					}
					return true
				}
				srv := s.server(t)
				defer srv.Close()
				dir, _, _ := writeMapDir(t, shaOf(1), "10")
				var out bytes.Buffer
				err := runStore(t.Context(), testCfg(srv.URL, s.token, time.Now(), &out), dir, shaOf(1), "10", "1")
				success := mode == "transient"
				published := slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "publish:") })
				if success != (err == nil) || success != published || reads > assetReadAttempts || (mode == "duplicate" && reads != 1) || (!success && mode != "duplicate" && reads != assetReadAttempts) {
					t.Fatalf("unexpected verification: %v reads=%d events=%v", err, reads, s.events)
				}
				if strings.Count(strings.Join(s.events, "\n"), "upload:") != 2 {
					t.Fatal("an acknowledged upload was repeated")
				}
				logs := out.String()
				if err != nil {
					logs += err.Error()
				}
				for _, want := range []string{"id=", "size=", "state=", "expected=2 actual=", "missing=", "extra=", "duplicate=", "invalid="} {
					if !strings.Contains(logs, want) {
						t.Errorf("missing diagnostic %s", want)
					}
				}
				if strings.HasPrefix(mode, "persistent") && !strings.Contains(logs, `missing=["index.json"]`) {
					t.Fatal("missing filename absent from diagnostics")
				}
				for _, forbidden := range []string{s.token, "sourcesContent", "::", "##[", "\r"} {
					if strings.Contains(logs, forbidden) {
						t.Fatal("unsafe diagnostic")
					}
				}
			})
		}
	}
}

func TestDraftAssetsPagination(t *testing.T) {
	now := time.Now()
	name := relNameAt(now, shaOf(1), 10, 1)
	s := &mockStore{token: "mock", releases: []mockRelease{{ID: 1, Name: name, TagName: name, Draft: true}}}
	want, uploaded := map[string][]byte{}, map[string]int64{}
	for i := range 101 {
		a := mockAsset{ID: int64(i + 1), Name: fmt.Sprintf("%d.js.map", i), Size: 1}
		s.releases[0].Assets = append(s.releases[0].Assets, a)
		want[a.Name], uploaded[a.Name] = []byte("x"), a.ID
	}
	srv := s.server(t)
	defer srv.Close()
	c := &apiClient{cfg: testCfg(srv.URL, s.token, now, nil)}
	if got, err := c.readDraftAssets(t.Context(), ghRelease{ID: 1}, name, want, uploaded); err != nil || len(got.Assets) != 101 {
		t.Fatalf("paginated verification failed: %v", err)
	}
	pages := 0
	s.before = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/assets") {
			return false
		}
		pages++
		_ = json.NewEncoder(w).Encode(s.releases[0].Assets[:100])
		return true
	}
	if _, err := c.readDraftAssets(t.Context(), ghRelease{ID: 1}, name, want, uploaded); err == nil || pages != maxReleasePages {
		t.Fatalf("verification did not respect page cap: %v pages=%d", err, pages)
	}
}

type deadlineTransport struct{}

func (deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestDraftAssetsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testCfg("http://mock.invalid", "mock", time.Now(), nil)
		cfg.transport = deadlineTransport{}
		c, start := &apiClient{cfg: cfg}, time.Now()
		if _, err := c.readDraftAssets(t.Context(), ghRelease{ID: 1}, "", nil, nil); err == nil || time.Since(start) != assetReadTimeout {
			t.Fatalf("verification did not respect total timeout: %v elapsed=%s", err, time.Since(start))
		}
	})
}
